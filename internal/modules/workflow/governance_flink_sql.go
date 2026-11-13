package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Only compiled definitions reach the gateway; neither SQL nor connector options come from the browser.
type governanceFlinkSQL struct {
	Setup  []string
	Insert string
}

var streamScriptJob = regexp.MustCompile(`^datamesh-stream-([a-f0-9]{32})-([1-9][0-9]*)$`)

// Numeric input stays textual, matching the sandbox contract without losing precision.
func flinkScriptJSON(fields []governanceFlowField) string {
	parts := []string{}
	for _, f := range fields {
		value := flinkField("l", f.Field.Name)
		switch f.Field.LogicalType {
		case "integer", "decimal":
			value = "CAST(" + value + " AS STRING)"
		case "datetime":
			value = "CONCAT(DATE_FORMAT(" + value + ", 'yyyy-MM-dd''T''HH:mm:ss.SSSSSS'), 'Z')"
		}
		parts = append(parts, "KEY "+flinkLiteral(f.Field.Name)+" VALUE "+value)
	}
	return "JSON_OBJECT(" + strings.Join(parts, ",") + " NULL ON NULL)"
}

func flinkIdentifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }
func flinkLiteral(value string) string    { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func flinkRelation(edge GovernanceFlowEdge) string {
	return flinkIdentifier("v_" + edge.Node + "_" + edge.Port)
}
func flinkField(alias, name string) string { return alias + "." + flinkIdentifier(name) }
func flinkType(field Field) (string, error) {
	switch field.LogicalType {
	case "string":
		return "STRING", nil
	case "integer":
		return "BIGINT", nil
	case "boolean":
		return "BOOLEAN", nil
	case "date":
		return "DATE", nil
	case "datetime":
		return "TIMESTAMP(6)", nil
	case "decimal":
		if field.Precision == nil || field.Scale == nil || *field.Precision > 38 {
			return "", problem(409, "STREAM_DECIMAL_PRECISION", "持续更新的数值精度最高为38位，请使用批量处理或调整字段精度")
		}
		return fmt.Sprintf("DECIMAL(%d,%d)", *field.Precision, *field.Scale), nil
	}
	return "", invalid
}
func flinkCast(value string, field Field) (string, error) {
	typ, err := flinkType(field)
	if err != nil {
		return "", err
	}
	if field.LogicalType == "datetime" {
		return "CAST(REPLACE(SUBSTRING(" + value + ",1,26),'T',' ') AS TIMESTAMP(6))", nil
	}
	return "CAST(" + value + " AS " + typ + ")", nil
}
func flinkJSON(fields []governanceFlowField, alias string) string {
	parts := []string{}
	for _, f := range fields {
		value := flinkField(alias, f.Field.Name)
		if f.Field.LogicalType == "decimal" {
			// Doris JSON numeric storage rounds decimals through floating point.
			value = "CAST(" + value + " AS STRING)"
		}
		if f.Field.LogicalType == "datetime" {
			value = "CONCAT(DATE_FORMAT(" + value + ", 'yyyy-MM-dd''T''HH:mm:ss.SSSSSS'), 'Z')"
		}
		parts = append(parts, "KEY "+flinkLiteral(f.Field.Name)+" VALUE "+value)
	}
	return "JSON_OBJECT(" + strings.Join(parts, ",") + " NULL ON NULL)"
}
func flinkCondition(c GovernanceFlowCondition, fields map[string]governanceFlowField) (string, error) {
	value := flinkField("l", c.Field)
	if c.Operator == "is_null" {
		return value + " IS NULL", nil
	}
	if c.Operator == "not_null" {
		return value + " IS NOT NULL", nil
	}
	literal, err := flinkCast(flinkLiteral(flowLiteralText(c.Value)), fields[c.Field].Field)
	if err != nil {
		return "", err
	}
	op, ok := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[c.Operator]
	if !ok {
		return "", invalid
	}
	return value + op + literal, nil
}
func flinkQuality(check Check, fields map[string]governanceFlowField) (string, error) {
	field := fields[check.Field].Field
	value := flinkField("l", check.Field)
	switch check.Kind {
	case "format":
		return value + " IS NULL OR (" + value + " <> '' AND REGEXP_REPLACE(" + value + ", " + flinkLiteral(qualityFormatPattern(check)) + ", '') = '')", nil
	case "required":
		return value + " IS NOT NULL", nil
	case "type":
		return "TRUE", nil // The typed source and views have already rejected invalid values.
	case "length", "range":
		expr := value
		if check.Kind == "length" {
			expr = "CHAR_LENGTH(" + value + ")"
		}
		terms := []string{}
		for _, bound := range []string{"min", "max"} {
			if raw, ok := check.Parameters[bound]; ok {
				op := ">="
				if bound == "max" {
					op = "<="
				}
				terms = append(terms, expr+op+"CAST("+flinkLiteral(flowLiteralText(raw))+" AS DECIMAL(38,18))")
			}
		}
		return value + " IS NULL OR (" + strings.Join(terms, " AND ") + ")", nil
	case "enum":
		var values []json.RawMessage
		if err := json.Unmarshal(check.Parameters["values"], &values); err != nil {
			return "", err
		}
		terms := []string{}
		for _, raw := range values {
			term, err := flinkCast(flinkLiteral(flowLiteralText(raw)), field)
			if err != nil {
				return "", err
			}
			terms = append(terms, term)
		}
		return value + " IS NULL OR " + value + " IN (" + strings.Join(terms, ",") + ")", nil
	case "compare_fields":
		op, ok := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[flowLiteralText(check.Parameters["operator"])]
		if !ok {
			return "", invalid
		}
		return value + op + flinkField("l", flowLiteralText(check.Parameters["other_field"])), nil
	}
	return "", problem(409, "STREAM_NODE_UNSUPPORTED", "该质量检查暂不支持持续更新，请使用批量处理")
}

func compileGovernanceFlink(def governanceFlowExecution, topics map[string]string, outputTopic, jobName string) (governanceFlinkSQL, error) {
	var out governanceFlinkSQL
	graph, plan, _, err := governanceExecutionPlan(def)
	if err != nil {
		return out, err
	}
	nodes := map[string]GovernanceFlowNode{}
	for _, n := range graph.Nodes {
		nodes[n.ID] = n
	}
	outputs := []string{}
	scriptRegistered := false
	for _, id := range plan.Order {
		n := nodes[id]
		fields := plan.Outputs[GovernanceFlowEdge{Node: id}]
		for _, fs := range plan.Outputs {
			for _, f := range fs {
				if _, err = flinkType(f.Field); err != nil {
					return out, err
				}
			}
		}
		if n.Kind == "source" {
			if topics[id] == "" {
				return out, invalid
			}
			columns := []string{"`_dm_key` STRING NOT NULL", "`_dm_seq` BIGINT NOT NULL", "`_dm_deleted` BOOLEAN NOT NULL"}
			// Keep a null identity on deletion markers so SQL projection pushdown
			// cannot call a script with the marker's deliberately null fields.
			selected := []string{"CASE WHEN `_dm_deleted` THEN CAST(NULL AS STRING) ELSE `_dm_key` END AS `_dm_key`"}
			for _, f := range def.Inputs[id].Fields {
				if strings.HasPrefix(f.Name, "_dm_") {
					return out, problem(409, "STREAM_RESERVED_FIELD", "来源字段使用了持续更新保留名称，请调整字段名称")
				}
				columns = append(columns, flinkIdentifier(f.Name)+" STRING")
				expr, e := flinkCast(flinkIdentifier(f.Name), f)
				if e != nil {
					return out, e
				}
				selected = append(selected, expr+" AS "+flinkIdentifier(f.Name))
			}

			out.Setup = append(out.Setup, "CREATE TEMPORARY TABLE "+flinkIdentifier("s_"+id)+" ("+strings.Join(columns, ",")+") WITH ('connector'='kafka','topic'="+flinkLiteral(topics[id])+",'properties.bootstrap.servers'='kafka:9092','properties.group.id'="+flinkLiteral(jobName+"_"+id)+",'scan.startup.mode'='earliest-offset','format'='json','json.fail-on-missing-field'='true','json.ignore-parse-errors'='false')")
			latest := flinkIdentifier("latest_" + id)
			out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+latest+" AS SELECT * FROM (SELECT *,ROW_NUMBER() OVER(PARTITION BY `_dm_key` ORDER BY `_dm_seq` DESC) AS `_dm_rank` FROM "+flinkIdentifier("s_"+id)+") ranked WHERE `_dm_rank`=1")
			out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+flinkRelation(GovernanceFlowEdge{Node: id})+" AS SELECT "+strings.Join(selected, ",")+" FROM "+latest+" WHERE NOT `_dm_deleted`")
			continue
		}
		input := plan.Outputs[n.Inputs[0]]
		byName := flowFieldsByName(input)
		from := flinkRelation(n.Inputs[0]) + " l"
		selectSQL := "l.*"
		tail := ""
		switch n.Kind {
		case "project", "transform":
			parts := []string{"l.`_dm_key`"}
			for _, c := range n.Columns {
				expr := flinkField("l", c.Source)
				switch c.Rule {
				case "", "copy":
				case "trim":
					expr = "REGEXP_REPLACE(" + expr + ", " + flinkLiteral("^["+flowWhitespace+"]+|["+flowWhitespace+"]+$") + ", '')"
				case "upper":
					expr = "UPPER(" + expr + ")"
				case "lower":
					expr = "LOWER(" + expr + ")"
				default:
					return out, invalid
				}
				parts = append(parts, expr+" AS "+flinkIdentifier(c.Target))
			}
			selectSQL = strings.Join(parts, ",")
		case "filter":
			predicate, e := flinkCondition(*n.Condition, byName)
			if e != nil {
				return out, e
			}
			tail = " WHERE " + predicate
		case "join":
			terms := []string{}
			for i, key := range n.Join.LeftKeys {
				terms = append(terms, flinkField("l", key)+"="+flinkField("r", n.Join.RightKeys[i]))
			}
			join := " INNER JOIN "
			if n.Join.Kind == "left" {
				join = " LEFT JOIN "
			}
			from += join + flinkRelation(n.Inputs[1]) + " r ON " + strings.Join(terms, " AND ")
			parts := []string{"SHA2(CONCAT(l.`_dm_key`,':',COALESCE(r.`_dm_key`,'null')),256) AS `_dm_key`"}
			for _, c := range n.Join.Columns {
				alias := "l"
				if c.Side == "right" {
					alias = "r"
				}
				parts = append(parts, flinkField(alias, c.Source)+" AS "+flinkIdentifier(c.Target))
			}
			selectSQL = strings.Join(parts, ",")
		case "aggregate":
			parts := []string{}
			groups := []string{}
			for _, key := range n.GroupBy {
				value := flinkField("l", key)
				parts = append(parts, value)
				groups = append(groups, value)
			}
			for _, a := range n.Aggregates {
				op, ok := map[string]string{"count": "COUNT", "sum": "SUM", "min": "MIN", "max": "MAX"}[a.Operation]
				if !ok {
					return out, invalid
				}
				parts = append(parts, op+"("+flinkField("l", a.Source)+") AS "+flinkIdentifier(a.Target))
			}
			key := "'global'"
			if len(groups) > 0 {
				key = "SHA2(JSON_ARRAY(" + strings.Join(groups, ",") + " NULL ON NULL),256)"
			}
			parts = append([]string{key + " AS `_dm_key`"}, parts...)
			selectSQL = strings.Join(parts, ",")
			if len(groups) > 0 {
				tail = " GROUP BY " + strings.Join(groups, ",")
			}
		case "quality":
			predicates := []string{}
			reasons := []string{}
			for _, c := range n.Checks {
				p, e := flinkQuality(c, byName)
				if e != nil {
					return out, problem(409, "STREAM_NODE_UNSUPPORTED", "节点 "+id+" 的质量检查暂不支持持续更新，请使用批量处理")
				}
				p = "COALESCE((" + p + "),FALSE)"
				if c.Blocking {
					predicates = append(predicates, p)
				}
				reasons = append(reasons, "CASE WHEN "+p+" THEN CAST(NULL AS STRING) ELSE "+flinkLiteral(string(encode(c.Kind+":"+c.Field)))+" END")
			}
			pass := "TRUE"
			if len(predicates) > 0 {
				pass = strings.Join(predicates, " AND ")
			}
			out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+flinkRelation(GovernanceFlowEdge{Node: id, Port: "good"})+" AS SELECT l.* FROM "+from+" WHERE "+pass)
			bad := flinkRelation(GovernanceFlowEdge{Node: id, Port: "bad"})
			reasonSQL := "'[]'"
			if len(reasons) > 0 {
				reasonSQL = "CONCAT('[',CONCAT_WS(','," + strings.Join(reasons, ",") + "),']')"
			}
			out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+bad+" AS SELECT l.*, "+reasonSQL+" AS `_dm_reasons` FROM "+from+" WHERE NOT ("+pass+")")
			outputs = append(outputs, "SELECT "+flinkLiteral(id)+",'bad',l.`_dm_key`,"+flinkJSON(input, "l")+",l.`_dm_reasons` FROM "+bad+" l")
			outputs = append(outputs, "SELECT "+flinkLiteral(id)+",'good',l.`_dm_key`,"+flinkJSON(input, "l")+",'[]' FROM "+flinkRelation(GovernanceFlowEdge{Node: id, Port: "good"})+" l")
			continue
		case "deduplicate":
			keys := []string{}
			for _, key := range n.Keys {
				keys = append(keys, flinkField("l", key))
			}
			names := []string{"`_dm_key`"}
			for _, f := range fields {
				names = append(names, flinkIdentifier(f.Field.Name))
			}
			out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+flinkRelation(GovernanceFlowEdge{Node: id})+" AS SELECT "+strings.Join(names, ",")+" FROM (SELECT l.*,ROW_NUMBER() OVER(PARTITION BY "+strings.Join(keys, ",")+" ORDER BY l.`_dm_key`) AS `_dm_rank` FROM "+from+") ranked WHERE `_dm_rank`=1")
			continue
		case "code_map":
			if n.Code.Unmatched == "reject" {
				return out, problem(409, "STREAM_NODE_UNSUPPORTED", "节点 "+id+" 要求拒绝未知码值，请使用批量处理或改为质量检查隔离")
			}
			parts := []string{"l.`_dm_key`"}
			for _, f := range input {
				expr := flinkField("l", f.Field.Name)
				if f.Field.Name == n.Code.Field {
					partsSQL := []string{"CASE"}
					for _, v := range n.Code.Values {
						partsSQL = append(partsSQL, "WHEN "+expr+"="+flinkLiteral(v.From)+" THEN "+flinkLiteral(v.To))
					}
					fallback := expr
					if n.Code.Unmatched != "keep" {
						fallback = "CAST(NULL AS STRING)"
					}
					expr = strings.Join(partsSQL, " ") + " ELSE " + fallback + " END"
				}
				parts = append(parts, expr+" AS "+flinkIdentifier(f.Field.Name))
			}
			selectSQL = strings.Join(parts, ",")
		case "output":
			parts := []string{"l.`_dm_key`"}
			for _, f := range fields {
				parts = append(parts, flinkField("l", f.Field.Name))
			}
			selectSQL = strings.Join(parts, ",")
		case "script":
			streamID, generation := "plan", 1
			if jobName != "plan" {
				match := streamScriptJob.FindStringSubmatch(jobName)
				if match == nil {
					return out, invalid
				}
				streamID = match[1]
				generation, err = strconv.Atoi(match[2])
				if err != nil || generation > 2147483647 {
					return out, invalid
				}
			}
			if !scriptRegistered {
				out.Setup = append(out.Setup, "ADD JAR 'file:///opt/flink/usrlib/datamesh-stream-script.jar'", "CREATE TEMPORARY SYSTEM FUNCTION `dm_stream_script` AS 'datamesh.governance.StreamScriptFunction'")
				scriptRegistered = true
			}
			call := "`dm_stream_script`(" + flinkLiteral(streamID) + "," + strconv.Itoa(generation) + "," + flinkLiteral(id) + ",l.`_dm_key`," + flinkScriptJSON(input) + ")"
			call = "CASE WHEN l.`_dm_key` IS NULL THEN CAST(NULL AS STRING) ELSE " + call + " END"
			mapped := flinkIdentifier("script_" + id)
			out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+mapped+" AS SELECT l.`_dm_key`,"+call+" AS `_dm_script_payload` FROM "+from)
			parts := []string{"l.`_dm_key`"}
			for _, f := range fields {
				if strings.HasPrefix(f.Field.Name, "_dm_") {
					return out, problem(409, "STREAM_RESERVED_FIELD", "代码输出字段使用了持续更新保留名称，请调整字段名称")
				}
				path, empty := "$."+f.Field.Name, "ERROR"
				if f.Field.Nullable {
					// Flink strict JSON paths reject explicit JSON null. The bridge
					// already validates exact keys and declared scalar/nullability types.
					path, empty = "lax "+path, "NULL"
				}
				expr, e := flinkCast("JSON_VALUE(l.`_dm_script_payload`,"+flinkLiteral(path)+" RETURNING STRING "+empty+" ON EMPTY ERROR ON ERROR)", f.Field)
				if e != nil {
					return out, e
				}
				parts = append(parts, expr+" AS "+flinkIdentifier(f.Field.Name))
			}
			selectSQL = strings.Join(parts, ",")
			from = mapped + " l"
		default:
			return out, invalid
		}
		view := flinkRelation(GovernanceFlowEdge{Node: id})
		out.Setup = append(out.Setup, "CREATE TEMPORARY VIEW "+view+" AS SELECT "+selectSQL+" FROM "+from+tail)
		if n.Kind == "output" {
			outputs = append(outputs, "SELECT "+flinkLiteral(id)+",'',l.`_dm_key`,"+flinkJSON(fields, "l")+",'[]' FROM "+view+" l")
		}
	}
	// Doris credentials stay in the controlled materializer; Flink publishes a
	// transactional changelog with stable identities for joins and retractions.
	out.Setup = append(out.Setup, "CREATE TEMPORARY TABLE `result_sink` (`node_id` STRING,`port` STRING,`row_key` STRING,`payload` STRING,`violations` STRING,PRIMARY KEY (`node_id`,`port`,`row_key`) NOT ENFORCED) WITH ('connector'='upsert-kafka','topic'="+flinkLiteral(outputTopic)+",'properties.bootstrap.servers'='kafka:9092','key.format'='json','value.format'='json','value.fields-include'='ALL','sink.delivery-guarantee'='exactly-once','sink.transactional-id-prefix'="+flinkLiteral(jobName)+",'properties.transaction.timeout.ms'='60000','sink.parallelism'='1')")
	out.Insert = "INSERT INTO `result_sink` " + strings.Join(outputs, " UNION ALL ")
	return out, nil
}
func streamResultLayer(def governanceFlowExecution) string {
	graph, plan, models, e := governanceExecutionPlan(def)
	if e != nil {
		return ""
	}
	for _, n := range graph.Nodes {
		if n.ID == plan.OutputNode {
			if n.Model != nil && models[*n.Model].DataLayer != "" {
				return models[*n.Model].DataLayer
			}
			return outputDataLayer(n.DataLayer)
		}
	}
	return ""
}
func streamResultTable(def governanceFlowExecution, id string) string {
	return dorisDatabase(streamResultLayer(def)) + ".live_" + id
}
