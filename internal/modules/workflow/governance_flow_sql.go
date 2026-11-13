package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

type governanceSQL struct{ args []interface{} }

func (q *governanceSQL) bind(value interface{}) string {
	q.args = append(q.args, value)
	return fmt.Sprintf("$%d", len(q.args))
}
func (q *governanceSQL) field(alias, name string) string { return alias + ".payload->>" + q.bind(name) }
func (q *governanceSQL) value(alias string, field Field) string {
	value := "(" + q.field(alias, field.Name) + ")"
	switch field.LogicalType {
	case "integer", "decimal":
		return value + "::numeric"
	case "boolean":
		return value + "::boolean"
	case "date":
		return value + "::date"
	case "datetime":
		return value + "::timestamptz"
	default:
		return value + " COLLATE \"C\""
	}
}
func (q *governanceSQL) input(alias string, edge GovernanceFlowEdge) string {
	return alias + ".node_id=" + q.bind(edge.Node) + " AND " + alias + ".port=" + q.bind(edge.Port)
}
func flowJSONObject(pairs []string) string {
	if len(pairs) == 0 {
		return "'{}'::jsonb"
	}
	parts := []string{}
	for i := 0; i < len(pairs); i += 2 {
		pairs[i] += "::text"
	}
	for len(pairs) > 0 {
		n := min(50, len(pairs))
		parts = append(parts, "jsonb_build_object("+strings.Join(pairs[:n], ",")+")")
		pairs = pairs[n:]
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

const flowWhitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

func flowLiteralText(value json.RawMessage) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}
func (q *governanceSQL) condition(condition GovernanceFlowCondition, fields map[string]governanceFlowField) string {
	if condition.Operator == "is_null" {
		return "(" + q.field("l", condition.Field) + ") IS NULL"
	}
	if condition.Operator == "not_null" {
		return "(" + q.field("l", condition.Field) + ") IS NOT NULL"
	}
	value := q.value("l", fields[condition.Field].Field)
	operators := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}
	kind := fields[condition.Field].Field.LogicalType
	cast := "text"
	switch kind {
	case "integer", "decimal":
		cast = "numeric"
	case "boolean":
		cast = "boolean"
	case "date":
		cast = "date"
	case "datetime":
		cast = "timestamptz"
	}
	raw := flowLiteralText(condition.Value)
	if kind == "date" || kind == "datetime" {
		if parsed, e := parseImportValue(raw, DirectoryItem{LogicalType: kind}); e == nil {
			raw = fmt.Sprint(parsed)
		}
	}
	return value + " " + operators[condition.Operator] + " " + q.bind(raw) + "::" + cast
}
func (q *governanceSQL) quality(check Check, field Field) string {
	key := q.bind(field.Name)
	text := "(l.payload->>" + key + ")"
	raw := "(l.payload->" + key + ")"
	switch check.Kind {
	case "format":
		return text + " IS NULL OR (" + text + " ~ " + q.bind(qualityFormatPattern(check)) + ")"
	case "required":
		return text + " IS NOT NULL"
	case "business_key":
		return text + " IS NOT NULL AND row_number() OVER(PARTITION BY " + q.value("l", field) + " ORDER BY l.ordinal)=1"
	case "type":
		predicate := "jsonb_typeof(" + raw + ")='string'"
		switch field.LogicalType {
		case "integer":
			predicate = text + " ~ '^[+-]?[0-9]+$'"
		case "decimal":
			predicate = text + " ~ '^[+-]?[0-9]+(\\.[0-9]+)?$'"
		case "boolean":
			predicate = "jsonb_typeof(" + raw + ")='boolean'"
		}
		return text + " IS NULL OR (" + predicate + ")"
	case "length", "range":
		expression := "char_length(" + text + ")"
		terms := []string{}
		if check.Kind == "range" {
			expression = text + "::numeric"
		}
		for _, bound := range []string{"min", "max"} {
			if value, ok := check.Parameters[bound]; ok {
				op := ">="
				if bound == "max" {
					op = "<="
				}
				terms = append(terms, expression+op+q.bind(flowLiteralText(value))+"::numeric")
			}
		}
		predicate := strings.Join(terms, " AND ")
		if check.Kind == "range" {
			predicate = "CASE WHEN " + text + " ~ '^[+-]?[0-9]+(\\.[0-9]+)?$' THEN (" + predicate + ") ELSE false END"
		}
		return text + " IS NULL OR (" + predicate + ")"
	case "enum":
		var values []json.RawMessage
		_ = json.Unmarshal(check.Parameters["values"], &values)
		terms := []string{}
		for _, value := range values {
			cast := "text"
			switch field.LogicalType {
			case "integer", "decimal":
				cast = "numeric"
			case "boolean":
				cast = "boolean"
			case "date":
				cast = "date"
			case "datetime":
				cast = "timestamptz"
			}
			terms = append(terms, q.value("l", field)+"="+q.bind(flowLiteralText(value))+"::"+cast)
		}
		return text + " IS NULL OR (" + strings.Join(terms, " OR ") + ")"
	}
	return "false"
}

// All identifiers below are internal relation/alias names. Fields and literals are bound values, never user SQL.
func governanceFlowSelect(node GovernanceFlowNode, plan governanceFlowPlan) (string, []interface{}, error) {
	q := &governanceSQL{}
	if len(node.Inputs) == 0 {
		return "", nil, invalid
	}
	input := plan.Outputs[node.Inputs[0]]
	fields := flowFieldsByName(input)
	where := q.input("l", node.Inputs[0])
	from := "governance_flow_stage l"
	payload := "l.payload"
	ordinal := "l.ordinal"
	port := "''::text"
	violations := "'[]'::jsonb"
	tail := ""
	switch node.Kind {
	case "project", "transform":
		pairs := []string{}
		for _, column := range node.Columns {
			key := q.bind(column.Source)
			value := "l.payload->" + key
			if column.Rule != "" && column.Rule != "copy" {
				text := "(l.payload->>" + key + ")"
				switch column.Rule {
				case "trim":
					value = "to_jsonb(btrim(" + text + "," + q.bind(flowWhitespace) + "))"
				case "upper":
					value = "to_jsonb(upper(" + text + "))"
				case "lower":
					value = "to_jsonb(lower(" + text + "))"
				default:
					return "", nil, invalid
				}
			}
			pairs = append(pairs, q.bind(column.Target), value)
		}
		payload = flowJSONObject(pairs)
	case "filter":
		where += " AND (" + q.condition(*node.Condition, fields) + ")"
	case "deduplicate":
		keys := []string{}
		for _, key := range node.Keys {
			keys = append(keys, q.value("l", fields[key].Field))
		}
		query := "SELECT DISTINCT ON (" + strings.Join(keys, ",") + ") l.ordinal,l.payload FROM " + from + " WHERE " + where + " ORDER BY " + strings.Join(keys, ",") + ",l.ordinal"
		return "SELECT payload,ordinal,''::text AS port,'[]'::jsonb AS violations FROM (" + query + ") deduplicated", q.args, nil
	case "join":
		right := flowFieldsByName(plan.Outputs[node.Inputs[1]])
		terms := []string{q.input("r", node.Inputs[1])}
		for i, key := range node.Join.LeftKeys {
			terms = append(terms, q.value("l", fields[key].Field)+"="+q.value("r", right[node.Join.RightKeys[i]].Field))
		}
		join := "JOIN"
		if node.Join.Kind == "left" {
			join = "LEFT JOIN"
		}
		from += " " + join + " governance_flow_stage r ON " + strings.Join(terms, " AND ")
		pairs := []string{}
		for _, column := range node.Join.Columns {
			alias := "l"
			if column.Side == "right" {
				alias = "r"
			}
			pairs = append(pairs, q.bind(column.Target), alias+".payload->"+q.bind(column.Source))
		}
		payload = flowJSONObject(pairs)
		ordinal = "row_number() OVER(ORDER BY l.ordinal,r.ordinal)"
	case "aggregate":
		pairs := []string{}
		groups := []string{}
		for _, key := range node.GroupBy {
			expression := q.value("l", fields[key].Field)
			groups = append(groups, expression)
			pairs = append(pairs, q.bind(key), "to_jsonb("+expression+")")
		}
		for _, aggregate := range node.Aggregates {
			op := map[string]string{"count": "count", "sum": "sum", "min": "min", "max": "max"}[aggregate.Operation]
			if op == "" {
				return "", nil, invalid
			}
			expression := ""
			if fields[aggregate.Source].Field.LogicalType == "boolean" && op != "count" {
				expression = "(" + q.field("l", aggregate.Source) + ")"
			} else {
				expression = q.value("l", fields[aggregate.Source].Field)
			}
			pairs = append(pairs, q.bind(aggregate.Target), "to_jsonb("+op+"("+expression+"))")
		}
		payload = flowJSONObject(pairs)
		ordinal = "1::bigint"
		if len(groups) > 0 {
			ordinal = "row_number() OVER(ORDER BY " + strings.Join(groups, ",") + ")"
			tail = " GROUP BY " + strings.Join(groups, ",")
		}
	case "quality":
		blocking := []string{}
		reasons := []string{}
		for _, check := range node.Checks {
			predicate := "false"
			if check.Kind != "compare_fields" && check.Kind != "reference_exists" {
				predicate = "COALESCE((" + q.quality(check, fields[check.Field].Field) + "),false)"
			}
			if check.Kind == "compare_fields" {
				other := fields[flowLiteralText(check.Parameters["other_field"])].Field
				operator := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[flowLiteralText(check.Parameters["operator"])]
				predicate = "COALESCE((" + q.value("l", fields[check.Field].Field) + " " + operator + " " + q.value("l", other) + "),false)"
			}
			if check.Kind == "reference_exists" {
				other := flowFieldsByName(plan.Outputs[node.Inputs[1]])[flowLiteralText(check.Parameters["other_field"])].Field
				value := q.value("l", fields[check.Field].Field)
				predicate = "(" + value + " IS NULL OR " + value + " IN (SELECT " + q.value("r", other) + " FROM governance_flow_stage r WHERE " + q.input("r", node.Inputs[1]) + "))"
			}
			if check.Blocking {
				blocking = append(blocking, "NOT ("+predicate+")")
			}
			reasons = append(reasons, "CASE WHEN "+predicate+" THEN '[]'::jsonb ELSE jsonb_build_array("+q.bind(check.Kind+":"+check.Field)+"::text) END")
		}
		bad := "false"
		if len(blocking) > 0 {
			bad = "(" + strings.Join(blocking, " OR ") + ")"
		}
		port = "CASE WHEN " + bad + " THEN 'bad' ELSE 'good' END"
		violations = "(" + strings.Join(reasons, " || ") + ")"
	case "code_map":
		code := node.Code
		pairs := []string{}
		for _, field := range input {
			key := q.bind(field.Field.Name)
			value := "l.payload->" + key
			if field.Field.Name == code.Field {
				text := "(l.payload->>" + key + ")"
				parts := []string{"CASE"}
				for _, entry := range code.Values {
					parts = append(parts, "WHEN "+text+"="+q.bind(entry.From)+" THEN to_jsonb("+q.bind(entry.To)+"::text)")
				}
				fallback := value
				if code.Unmatched != "keep" {
					fallback = "'null'::jsonb"
				}
				value = strings.Join(parts, " ") + " ELSE " + fallback + " END"
				if code.Unmatched == "reject" {
					allowed := []string{}
					for _, entry := range code.Values {
						allowed = append(allowed, text+"="+q.bind(entry.From))
					}
					where += " AND (" + text + " IS NULL OR " + strings.Join(allowed, " OR ") + ")"
				}
			}
			pairs = append(pairs, q.bind(field.Field.Name), value)
		}
		payload = flowJSONObject(pairs)
	case "output":
		pairs := []string{}
		for _, field := range plan.Outputs[GovernanceFlowEdge{Node: node.ID}] {
			pairs = append(pairs, q.bind(field.Field.Name), "l.payload->"+q.bind(field.Field.Name))
		}
		payload = flowJSONObject(pairs)
	default:
		return "", nil, invalid
	}
	return "SELECT " + payload + " AS payload," + ordinal + " AS ordinal," + port + " AS port," + violations + " AS violations FROM " + from + " WHERE " + where + tail, q.args, nil
}
