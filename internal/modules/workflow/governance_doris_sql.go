package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The compiler accepts the validated graph only. Values remain indexed parameters
// until the MySQL driver escapes them; field names are JSON paths, not SQL text.
type governanceDorisSQL struct {
	governanceSQL
	blankTextMissing bool
}

func (q *governanceDorisSQL) raw(alias, name string) string {
	return "JSON_EXTRACT(" + alias + ".payload," + q.bind("$."+string(encode(name))) + ")"
}
func (q *governanceDorisSQL) field(alias, name string) string {
	raw := q.raw(alias, name)
	return "CASE WHEN JSON_TYPE(" + raw + ",'$')='null' THEN NULL ELSE JSON_UNQUOTE(" + raw + ") END"
}
func dorisFieldType(f Field) string {
	switch f.LogicalType {
	case "integer":
		return "LARGEINT"
	case "decimal":
		if f.Precision != nil && f.Scale != nil {
			return fmt.Sprintf("DECIMAL(%d,%d)", *f.Precision, *f.Scale)
		}
		return "DECIMAL(38,18)"
	case "boolean":
		return "BOOLEAN"
	case "date":
		return "DATEV2"
	case "datetime":
		return "DATETIMEV2(6)"
	default:
		return "STRING"
	}
}
func (q *governanceDorisSQL) value(alias string, f Field) string {
	value := q.field(alias, f.Name)
	if f.LogicalType == "datetime" {
		value = "REPLACE(REPLACE(" + value + ",'T',' '),'Z','')"
	}
	return "CAST((" + value + ") AS " + dorisFieldType(f) + ")"
}
func dorisJSONObject(pairs []string) string { return "JSON_OBJECT(" + strings.Join(pairs, ",") + ")" }
func (q *governanceDorisSQL) condition(c GovernanceFlowCondition, fields map[string]governanceFlowField) string {
	v := q.value("l", fields[c.Field].Field)
	if c.Operator == "is_null" {
		return v + " IS NULL"
	}
	if c.Operator == "not_null" {
		return v + " IS NOT NULL"
	}
	op := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[c.Operator]
	return v + op + "CAST(" + q.bind(flowLiteralText(c.Value)) + " AS " + dorisFieldType(fields[c.Field].Field) + ")"
}
func (q *governanceDorisSQL) quality(c Check, f Field) string {
	text := q.field("l", f.Name)
	raw := q.raw("l", f.Name)
	if q.blankTextMissing && f.LogicalType == "string" && (c.Kind == "required" || c.Kind == "business_key") {
		text = "NULLIF(TRIM(" + text + "),'')"
	}
	switch c.Kind {
	case "format":
		return text + " IS NULL OR REGEXP(" + text + "," + q.bind(qualityFormatPattern(c)) + ")"
	case "required":
		return text + " IS NOT NULL"
	case "business_key":
		return text + " IS NOT NULL AND ROW_NUMBER() OVER(PARTITION BY " + q.value("l", f) + " ORDER BY l.ordinal)=1"
	case "type":
		predicate := "JSON_TYPE(" + raw + ",'$')='string'"
		switch f.LogicalType {
		case "integer":
			predicate = "REGEXP(" + text + ",'^[+-]?[0-9]+$')"
		case "decimal":
			predicate = "REGEXP(" + text + ",'^[+-]?[0-9]+([.][0-9]+)?$')"
		case "boolean":
			predicate = "JSON_TYPE(" + raw + ",'$')='bool'"
		}
		return text + " IS NULL OR (" + predicate + ")"
	case "length", "range":
		expr := "CHAR_LENGTH(" + text + ")"
		if c.Kind == "range" {
			expr = q.value("l", f)
		}
		terms := []string{}
		for _, bound := range []string{"min", "max"} {
			if value, ok := c.Parameters[bound]; ok {
				op := ">="
				if bound == "max" {
					op = "<="
				}
				terms = append(terms, expr+op+"CAST("+q.bind(flowLiteralText(value))+" AS DECIMAL(38,18))")
			}
		}
		return text + " IS NULL OR (" + strings.Join(terms, " AND ") + ")"
	case "enum":
		var values []json.RawMessage
		_ = json.Unmarshal(c.Parameters["values"], &values)
		terms := []string{}
		for _, v := range values {
			terms = append(terms, q.value("l", f)+"=CAST("+q.bind(flowLiteralText(v))+" AS "+dorisFieldType(f)+")")
		}
		return text + " IS NULL OR (" + strings.Join(terms, " OR ") + ")"
	}
	return "FALSE"
}
func governanceDorisSelect(node GovernanceFlowNode, plan governanceFlowPlan) (string, []interface{}, error) {
	q := &governanceDorisSQL{blankTextMissing: plan.BlankTextMissing}
	if len(node.Inputs) == 0 {
		return "", nil, invalid
	}
	input := plan.Outputs[node.Inputs[0]]
	fields := flowFieldsByName(input)
	where := q.input("l", node.Inputs[0])
	from := "governance_flow_stage l"
	payload := "l.payload"
	ordinal := "l.ordinal"
	port := "''"
	violations := "JSON_ARRAY()"
	tail := ""
	switch node.Kind {
	case "project", "transform":
		pairs := []string{}
		for _, c := range node.Columns {
			value := q.raw("l", c.Source)
			if c.Rule != "" && c.Rule != "copy" {
				text := q.field("l", c.Source)
				switch c.Rule {
				case "trim":
					value = "REGEXP_REPLACE(" + text + "," + q.bind("^["+flowWhitespace+"]+|["+flowWhitespace+"]+$") + ",'')"
				case "upper":
					value = "UPPER(" + text + ")"
				case "lower":
					value = "LOWER(" + text + ")"
				default:
					return "", nil, invalid
				}
			}
			pairs = append(pairs, q.bind(c.Target), value)
		}
		payload = dorisJSONObject(pairs)
	case "filter":
		where += " AND (" + q.condition(*node.Condition, fields) + ")"
	case "deduplicate":
		keys := []string{}
		for _, k := range node.Keys {
			keys = append(keys, q.value("l", fields[k].Field))
		}
		return "SELECT payload,ordinal,'' AS port,JSON_ARRAY() AS violations FROM (SELECT l.payload,l.ordinal,ROW_NUMBER() OVER(PARTITION BY " + strings.Join(keys, ",") + " ORDER BY l.ordinal) AS rank_no FROM " + from + " WHERE " + where + ") ranked WHERE rank_no=1", q.args, nil
	case "join":
		right := flowFieldsByName(plan.Outputs[node.Inputs[1]])
		terms := []string{q.input("r", node.Inputs[1])}
		for i, k := range node.Join.LeftKeys {
			terms = append(terms, q.value("l", fields[k].Field)+"="+q.value("r", right[node.Join.RightKeys[i]].Field))
		}
		join := " JOIN "
		if node.Join.Kind == "left" {
			join = " LEFT JOIN "
		}
		from += join + "governance_flow_stage r ON " + strings.Join(terms, " AND ")
		pairs := []string{}
		for _, c := range node.Join.Columns {
			alias := "l"
			if c.Side == "right" {
				alias = "r"
			}
			pairs = append(pairs, q.bind(c.Target), q.raw(alias, c.Source))
		}
		payload = dorisJSONObject(pairs)
		ordinal = "ROW_NUMBER() OVER(ORDER BY l.ordinal,r.ordinal)"
	case "aggregate":
		pairs := []string{}
		groups := []string{}
		for _, k := range node.GroupBy {
			v := q.value("l", fields[k].Field)
			groups = append(groups, v)
			pairs = append(pairs, q.bind(k), v)
		}
		for _, a := range node.Aggregates {
			op := map[string]string{"count": "COUNT", "sum": "SUM", "min": "MIN", "max": "MAX"}[a.Operation]
			if op == "" {
				return "", nil, invalid
			}
			pairs = append(pairs, q.bind(a.Target), op+"("+q.value("l", fields[a.Source].Field)+")")
		}
		payload = dorisJSONObject(pairs)
		ordinal = "1"
		if len(groups) > 0 {
			ordinal = "ROW_NUMBER() OVER(ORDER BY " + strings.Join(groups, ",") + ")"
			tail = " GROUP BY " + strings.Join(groups, ",")
		}
	case "quality":
		blocking := []string{}
		reasons := []string{}
		for _, c := range node.Checks {
			predicate := q.quality(c, fields[c.Field].Field)
			if c.Kind == "compare_fields" {
				other := fields[flowLiteralText(c.Parameters["other_field"])].Field
				op := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[flowLiteralText(c.Parameters["operator"])]
				predicate = q.value("l", fields[c.Field].Field) + op + q.value("l", other)
			}
			if c.Kind == "reference_exists" {
				other := flowFieldsByName(plan.Outputs[node.Inputs[1]])[flowLiteralText(c.Parameters["other_field"])].Field
				v := q.value("l", fields[c.Field].Field)
				predicate = v + " IS NULL OR " + v + " IN (SELECT " + q.value("r", other) + " FROM governance_flow_stage r WHERE " + q.input("r", node.Inputs[1]) + ")"
			}
			predicate = "COALESCE((" + predicate + "),FALSE)"
			if c.Blocking {
				blocking = append(blocking, "NOT ("+predicate+")")
			}
			reasons = append(reasons, "CASE WHEN "+predicate+" THEN NULL ELSE "+q.bind(c.Kind+":"+c.Field)+" END")
		}
		bad := "FALSE"
		if len(blocking) > 0 {
			bad = strings.Join(blocking, " OR ")
		}
		port = "CASE WHEN " + bad + " THEN 'bad' ELSE 'good' END"
		violations = "JSON_ARRAY_IGNORE_NULL(" + strings.Join(reasons, ",") + ")"
	case "code_map":
		pairs := []string{}
		for _, f := range input {
			value := q.raw("l", f.Field.Name)
			if f.Field.Name == node.Code.Field {
				parts := []string{"CASE"}
				for _, v := range node.Code.Values {
					parts = append(parts, "WHEN "+q.field("l", f.Field.Name)+"="+q.bind(v.From)+" THEN JSON_QUOTE("+q.bind(v.To)+")")
				}
				fallback := "'null'"
				if node.Code.Unmatched == "keep" {
					fallback = "CAST(" + value + " AS STRING)"
				}
				value = "JSON_PARSE(" + strings.Join(parts, " ") + " ELSE " + fallback + " END)"
			}
			pairs = append(pairs, q.bind(f.Field.Name), value)
		}
		payload = dorisJSONObject(pairs)
	case "output":
		pairs := []string{}
		for _, f := range plan.Outputs[GovernanceFlowEdge{Node: node.ID}] {
			pairs = append(pairs, q.bind(f.Field.Name), q.raw("l", f.Field.Name))
		}
		payload = dorisJSONObject(pairs)
	default:
		return "", nil, invalid
	}
	return "SELECT " + payload + " AS payload," + ordinal + " AS ordinal," + port + " AS port," + violations + " AS violations FROM " + from + " WHERE " + where + tail, q.args, nil
}
