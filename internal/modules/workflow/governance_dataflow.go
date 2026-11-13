package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

var governanceQualityNumber = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

type GovernanceDataflow struct {
	Nodes []GovernanceFlowNode `json:"nodes"`
}
type GovernanceFlowEdge struct {
	Node string `json:"node"`
	Port string `json:"port,omitempty"`
}
type GovernanceFlowSource struct {
	ParameterBindings map[string]string `json:"parameter_bindings,omitempty"`
	Parameters        map[string]string `json:"parameters,omitempty"`
	Resource          Resource          `json:"resource"`
	Fields            []string          `json:"fields"`
}
type GovernanceFlowColumn struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Rule   string `json:"rule"`
}
type GovernanceFlowCondition struct {
	Parameter string          `json:"parameter,omitempty"`
	Field     string          `json:"field"`
	Operator  string          `json:"operator"`
	Value     json.RawMessage `json:"value"`
}
type GovernanceFlowJoinColumn struct {
	Side   string `json:"side"`
	Source string `json:"source"`
	Target string `json:"target"`
}
type GovernanceFlowJoin struct {
	Kind      string                     `json:"kind"`
	LeftKeys  []string                   `json:"left_keys"`
	RightKeys []string                   `json:"right_keys"`
	Columns   []GovernanceFlowJoinColumn `json:"columns"`
}
type GovernanceFlowAggregate struct {
	Operation string `json:"operation"`
	Source    string `json:"source"`
	Target    string `json:"target"`
}
type GovernanceFlowCode struct {
	Field     string                    `json:"field"`
	Standard  GovernanceRef             `json:"standard"`
	Values    []GovernanceFlowCodeValue `json:"values"`
	Unmatched string                    `json:"unmatched"`
}
type GovernanceFlowCodeValue struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type GovernanceFlowNode struct {
	DataLayer  string                    `json:"data_layer,omitempty"`
	Script     *GovernanceFlowScript     `json:"script,omitempty"`
	Template   *GovernanceRef            `json:"template,omitempty"`
	ID         string                    `json:"id"`
	Kind       string                    `json:"kind"`
	Inputs     []GovernanceFlowEdge      `json:"inputs"`
	Source     *GovernanceFlowSource     `json:"source,omitempty"`
	Columns    []GovernanceFlowColumn    `json:"columns,omitempty"`
	Condition  *GovernanceFlowCondition  `json:"condition,omitempty"`
	Keys       []string                  `json:"keys,omitempty"`
	Join       *GovernanceFlowJoin       `json:"join,omitempty"`
	GroupBy    []string                  `json:"group_by,omitempty"`
	Aggregates []GovernanceFlowAggregate `json:"aggregates,omitempty"`
	Checks     []Check                   `json:"checks,omitempty"`
	InferModel bool                      `json:"infer_model,omitempty"`
	Model      *GovernanceRef            `json:"model,omitempty"`
	Code       *GovernanceFlowCode       `json:"code,omitempty"`
}
type GovernanceFieldOrigin struct {
	Resource Resource `json:"resource"`
	Field    string   `json:"field"`
	Node     string   `json:"node"`
}
type governanceFlowField struct {
	Field   Field
	Origins []GovernanceFieldOrigin
}
type governanceFlowPlan struct {
	BlankTextMissing bool
	Order            []string
	Outputs          map[GovernanceFlowEdge][]governanceFlowField
	OutputNode       string
}

func flowProblem(code, message string) error { return problem(400, code, message) }
func flowFieldsByName(fields []governanceFlowField) map[string]governanceFlowField {
	out := map[string]governanceFlowField{}
	for _, field := range fields {
		out[field.Field.Name] = field
	}
	return out
}
func flowLiteral(raw json.RawMessage, field Field) error {
	if len(raw) == 0 || len(raw) > 2048 || string(raw) == "null" {
		return invalid
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return invalid
	}
	switch field.LogicalType {
	case "string":
		if _, ok := value.(string); !ok {
			return invalid
		}
	case "integer", "decimal":
		text, ok := value.(string)
		if !ok {
			number, numberOK := value.(json.Number)
			if !numberOK {
				return invalid
			}
			text = number.String()
		}
		if _, err := parseImportValue(text, DirectoryItem{LogicalType: field.LogicalType, Precision: field.Precision, Scale: field.Scale}); err != nil {
			return invalid
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return invalid
		}
	case "date", "datetime":
		text, ok := value.(string)
		if !ok {
			return invalid
		}
		if _, err := parseImportValue(text, DirectoryItem{LogicalType: field.LogicalType}); err != nil {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}
func compileGovernanceDataflow(graph GovernanceDataflow, sourceSchemas map[string][]Field, models map[GovernanceRef]GovernanceModel, standards map[GovernanceRef]GovernanceStandard) (governanceFlowPlan, error) {
	return compileGovernanceFlow(graph, sourceSchemas, models, standards, "")
}
func compileGovernanceFlow(graph GovernanceDataflow, sourceSchemas map[string][]Field, models map[GovernanceRef]GovernanceModel, standards map[GovernanceRef]GovernanceStandard, preview string) (governanceFlowPlan, error) {
	return compileGovernanceFlowSources(graph, sourceSchemas, models, standards, preview, 4)
}
func compileGovernanceFlowSources(graph GovernanceDataflow, sourceSchemas map[string][]Field, models map[GovernanceRef]GovernanceModel, standards map[GovernanceRef]GovernanceStandard, preview string, maxSources int) (governanceFlowPlan, error) {
	plan := governanceFlowPlan{Order: []string{}, Outputs: map[GovernanceFlowEdge][]governanceFlowField{}}
	if len(graph.Nodes) < 2 || len(graph.Nodes) > 40 {
		return plan, flowProblem("GOVERNANCE_GRAPH_SIZE", "数据流需要2–40个节点")
	}
	nodes := map[string]GovernanceFlowNode{}
	sources, outputs := 0, 0
	for _, node := range graph.Nodes {
		if !validDataLayer(node.DataLayer) || (node.DataLayer != "" && node.Kind != "output") || (node.Kind == "output" && node.DataLayer == "ods") {
			return plan, flowProblem("GOVERNANCE_DATA_LAYER", "治理成果请选择标准明细、公共维度、主题汇总或应用服务层")
		}
		if node.Model != nil {
			m := models[*node.Model]
			if m.DataLayer == "ods" || (node.DataLayer != "" && m.DataLayer != "" && m.DataLayer != node.DataLayer) {
				return plan, flowProblem("GOVERNANCE_DATA_LAYER", "输出分层必须与结果模型一致，ODS 仅用于原始接入")
			}
		}
		if node.Template != nil && (node.Kind != "quality" || !validGovernanceRef(node.Template)) {
			return plan, invalid
		}
		if !governanceIdentifier.MatchString(node.ID) {
			return plan, flowProblem("GOVERNANCE_NODE_ID", "节点编码须为字母、数字和下划线，且以字母或下划线开头")
		}
		if _, exists := nodes[node.ID]; exists {
			return plan, flowProblem("GOVERNANCE_NODE_DUPLICATE", "节点编码不能重复")
		}
		nodes[node.ID] = node
		if node.Kind == "source" {
			sources++
		}
		if node.Kind == "output" {
			outputs++
			plan.OutputNode = node.ID
		}
		if (node.Script != nil) != (node.Kind == "script") || (node.Source != nil) != (node.Kind == "source") || (node.Join != nil) != (node.Kind == "join") || (node.Condition != nil) != (node.Kind == "filter") || (node.Model != nil || node.InferModel) != (node.Kind == "output") || (node.Model != nil && node.InferModel) || (node.Code != nil) != (node.Kind == "code_map") {
			return plan, invalid
		}
		if len(node.Columns) > 0 && node.Kind != "project" && node.Kind != "transform" || len(node.Keys) > 0 && node.Kind != "deduplicate" || len(node.Checks) > 0 && node.Kind != "quality" || (len(node.Aggregates) > 0 || len(node.GroupBy) > 0) && node.Kind != "aggregate" {
			return plan, invalid
		}
		inputs := 1
		if node.Kind == "source" {
			inputs = 0
		}
		if node.Kind == "join" {
			inputs = 2
		}
		if node.Kind == "quality" && len(node.Inputs) == 2 {
			inputs = 2
		}
		if len(node.Inputs) != inputs {
			return plan, flowProblem("GOVERNANCE_NODE_INPUTS", "节点输入数量与算子不符")
		}
	}
	if sources < 1 || sources > maxSources || (preview == "" && outputs != 1) || (preview != "" && outputs != 0) {
		return plan, flowProblem("GOVERNANCE_GRAPH_BOUNDARY", fmt.Sprintf("每个数据流须有1–%d个来源和一个输出", maxSources))
	}
	if preview != "" {
		plan.OutputNode = preview
	}
	for _, node := range graph.Nodes {
		for _, edge := range node.Inputs {
			if _, ok := nodes[edge.Node]; !ok {
				return plan, flowProblem("GOVERNANCE_NODE_MISSING", "输入节点不存在")
			}
		}
	}
	quarantined := map[GovernanceFlowEdge]bool{}
	remaining := len(nodes)
	for remaining > 0 {
		advanced := false
		for _, node := range graph.Nodes {
			if slices.Contains(plan.Order, node.ID) {
				continue
			}
			ready := true
			for _, edge := range node.Inputs {
				if !slices.Contains(plan.Order, edge.Node) {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			inputs := [][]governanceFlowField{}
			for _, edge := range node.Inputs {
				fields, ok := plan.Outputs[edge]
				if !ok {
					return plan, flowProblem("GOVERNANCE_PORT_INVALID", "质量节点须明确选择合格或不合格分支，其他节点使用数据端口")
				}
				inputs = append(inputs, fields)
			}
			tainted := false
			for _, edge := range node.Inputs {
				tainted = tainted || quarantined[edge]
			}
			if tainted && node.Kind == "output" {
				return plan, flowProblem("GOVERNANCE_QUARANTINE_OUTPUT", "不合格分支不能直接或通过中间节点进入模型成果输出")
			}
			result, err := compileGovernanceFlowNode(node, inputs, sourceSchemas[node.ID], models, standards)
			if err != nil {
				return plan, err
			}
			if len(result) == 0 || len(result) > 100 {
				return plan, flowProblem("GOVERNANCE_FIELDS_LIMIT", "每个节点须输出1–100个字段")
			}
			if node.Kind == "quality" {
				quarantined[GovernanceFlowEdge{Node: node.ID, Port: "bad"}] = true
				quarantined[GovernanceFlowEdge{Node: node.ID, Port: "good"}] = tainted
				plan.Outputs[GovernanceFlowEdge{Node: node.ID, Port: "bad"}] = slices.Clone(result)
				good := slices.Clone(result)
				for i := range good {
					for _, check := range node.Checks {
						if (check.Kind == "required" || check.Kind == "business_key") && check.Blocking && check.Field == good[i].Field.Name {
							good[i].Field.Nullable = false
						}
					}
				}
				plan.Outputs[GovernanceFlowEdge{Node: node.ID, Port: "good"}] = good
			} else {
				quarantined[GovernanceFlowEdge{Node: node.ID}] = tainted
				plan.Outputs[GovernanceFlowEdge{Node: node.ID}] = result
			}
			plan.Order = append(plan.Order, node.ID)
			remaining--
			advanced = true
		}
		if !advanced {
			return plan, flowProblem("GOVERNANCE_GRAPH_CYCLE", "数据流存在循环依赖")
		}
	}
	used := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if used[id] {
			return
		}
		used[id] = true
		for _, edge := range nodes[id].Inputs {
			visit(edge.Node)
		}
	}
	visit(plan.OutputNode)
	if len(used) != len(nodes) {
		return plan, flowProblem("GOVERNANCE_NODE_UNUSED", "存在未连接到模型输出的孤立节点")
	}
	return plan, nil
}
func compileGovernanceFlowNode(node GovernanceFlowNode, inputs [][]governanceFlowField, source []Field, models map[GovernanceRef]GovernanceModel, standards map[GovernanceRef]GovernanceStandard) ([]governanceFlowField, error) {
	result := []governanceFlowField{}
	var input []governanceFlowField
	if len(inputs) > 0 {
		input = inputs[0]
	}
	known := flowFieldsByName(input)
	appendField := func(field governanceFlowField, name string) error {
		if !governanceIdentifier.MatchString(name) || slices.ContainsFunc(result, func(f governanceFlowField) bool { return f.Field.Name == name }) {
			return flowProblem("GOVERNANCE_FIELD_DUPLICATE", "输出字段编码无效或重复")
		}
		field.Field.Name = name
		result = append(result, field)
		return nil
	}
	switch node.Kind {
	case "source":
		if node.Source == nil || !valid(node.Source.Resource.ID, 128) || node.Source.Resource.Version < 1 || !slices.Contains([]string{"raw_object", "table_resource", "governed_dataset", "file_resource", "api_resource", "master_snapshot"}, node.Source.Resource.Type) || !validFields(node.Source.Fields, source) {
			return nil, flowProblem("GOVERNANCE_SOURCE_INVALID", "请选择明确版本的结构化资源和有效字段")
		}
		for _, name := range node.Source.Fields {
			for _, field := range source {
				if field.Name == name {
					if field.LogicalType == "unsupported" {
						return nil, invalid
					}
					result = append(result, governanceFlowField{field, []GovernanceFieldOrigin{{node.Source.Resource, name, node.ID}}})
				}
			}
		}
	case "script":
		return scriptOutputFields(node, input)
	case "project", "transform":
		if len(node.Columns) == 0 || len(node.Columns) > 100 {
			return nil, invalid
		}
		for _, column := range node.Columns {
			field, ok := known[column.Source]
			if !ok {
				return nil, flowProblem("GOVERNANCE_FIELD_MISSING", "所选来源字段不存在")
			}
			rule := column.Rule
			if rule == "" {
				rule = "copy"
			}
			if !slices.Contains([]string{"copy", "trim", "upper", "lower"}, rule) || (node.Kind == "project" && rule != "copy") || (rule != "copy" && field.Field.LogicalType != "string") {
				return nil, flowProblem("GOVERNANCE_TRANSFORM_INVALID", "文本转换仅支持文本字段；投影只能直接映射")
			}
			if err := appendField(field, column.Target); err != nil {
				return nil, err
			}
		}
	case "filter":
		if node.Condition == nil {
			return nil, invalid
		}
		condition := node.Condition
		field, ok := known[condition.Field]
		if !ok {
			return nil, invalid
		}
		if condition.Operator == "is_null" || condition.Operator == "not_null" {
			if len(condition.Value) > 0 && string(condition.Value) != "null" {
				return nil, invalid
			}
		} else {
			if !slices.Contains([]string{"eq", "ne", "gt", "gte", "lt", "lte"}, condition.Operator) || flowLiteral(condition.Value, field.Field) != nil {
				return nil, flowProblem("GOVERNANCE_CONDITION_INVALID", "筛选条件的运算符或值类型不匹配")
			}
			if field.Field.LogicalType == "boolean" && condition.Operator != "eq" && condition.Operator != "ne" {
				return nil, invalid
			}
		}
		result = slices.Clone(input)
		if condition.Operator == "not_null" {
			for i := range result {
				if result[i].Field.Name == condition.Field {
					result[i].Field.Nullable = false
				}
			}
		}
	case "deduplicate":
		if len(node.Keys) == 0 || len(node.Keys) > len(input) {
			return nil, invalid
		}
		seen := map[string]bool{}
		for _, key := range node.Keys {
			if _, ok := known[key]; !ok || seen[key] {
				return nil, invalid
			}
			seen[key] = true
		}
		result = slices.Clone(input)
	case "join":
		join := node.Join
		if join == nil || len(inputs) != 2 || !slices.Contains([]string{"inner", "left"}, join.Kind) {
			return nil, flowProblem("GOVERNANCE_JOIN_INVALID", "关联节点 "+node.ID+"：请连接左、右两份数据，并选择关联方式")
		}
		if len(join.LeftKeys) == 0 || len(join.LeftKeys) > 10 || len(join.LeftKeys) != len(join.RightKeys) {
			return nil, flowProblem("GOVERNANCE_JOIN_INVALID", "关联节点 "+node.ID+"：请为左右两侧选择数量相同、按顺序配对的对应字段，每次最多10组")
		}
		if len(join.Columns) == 0 || len(join.Columns) > 100 {
			return nil, flowProblem("GOVERNANCE_JOIN_INVALID", "关联节点 "+node.ID+"：请选择关联结果要保留的字段，每次最多100个；可添加左侧或右侧输出字段")
		}
		right := flowFieldsByName(inputs[1])
		seen := map[string]bool{}
		for i, key := range join.LeftKeys {
			a, ok := known[key]
			b, other := right[join.RightKeys[i]]
			if !ok || !other || a.Field.LogicalType != b.Field.LogicalType || seen[key] {
				return nil, flowProblem("GOVERNANCE_JOIN_KEY_TYPE", "关联键不存在、重复或类型不匹配")
			}
			seen[key] = true
		}
		for _, column := range join.Columns {
			fields := known
			if column.Side == "right" {
				fields = right
			} else if column.Side != "left" {
				return nil, invalid
			}
			field, ok := fields[column.Source]
			if !ok {
				return nil, invalid
			}
			if column.Side == "right" && join.Kind == "left" {
				field.Field.Nullable = true
			}
			field.Field.PrimaryKey = false
			if err := appendField(field, column.Target); err != nil {
				return nil, err
			}
		}
	case "aggregate":
		if len(node.Aggregates) == 0 || len(node.GroupBy) > 20 || len(node.Aggregates) > 100 {
			return nil, invalid
		}
		for _, key := range node.GroupBy {
			field, ok := known[key]
			if !ok {
				return nil, invalid
			}
			field.Field.PrimaryKey = false
			if err := appendField(field, key); err != nil {
				return nil, err
			}
		}
		for _, aggregate := range node.Aggregates {
			field, ok := known[aggregate.Source]
			if !ok || !slices.Contains([]string{"count", "sum", "min", "max"}, aggregate.Operation) {
				return nil, flowProblem("GOVERNANCE_AGGREGATE_INVALID", "聚合仅支持计数、求和和最值，并须选择有效字段")
			}
			field.Field.PrimaryKey = false
			if aggregate.Operation == "count" {
				field.Field.LogicalType = "integer"
				field.Field.Nullable = false
				field.Field.Precision = nil
				field.Field.Scale = nil
			} else {
				if aggregate.Operation == "sum" && field.Field.LogicalType != "integer" && field.Field.LogicalType != "decimal" {
					return nil, invalid
				}
				field.Field.Nullable = true
			}
			if err := appendField(field, aggregate.Target); err != nil {
				return nil, err
			}
		}
	case "quality":
		fields := []Field{}
		checks := slices.Clone(node.Checks)
		if len(checks) < 1 || len(checks) > 100 {
			return nil, invalid
		}
		basic := []Check{}
		for i, check := range checks {
			if check.Kind == "compare_fields" || check.Kind == "reference_exists" {
				if err := validateGovernanceCrossCheck(check, input, inputs); err != nil {
					return nil, err
				}
				continue
			}
			if check.Kind == "enum" {
				var values []json.RawMessage
				if json.Unmarshal(check.Parameters["values"], &values) != nil {
					return nil, invalid
				}
				field, ok := known[check.Field]
				if !ok {
					return nil, invalid
				}
				for _, value := range values {
					if err := flowLiteral(value, field.Field); err != nil {
						return nil, err
					}
				}
			}
			if check.Kind == "length" || check.Kind == "range" {
				parameters := map[string]json.RawMessage{}
				for key, raw := range check.Parameters {
					text := flowLiteralText(raw)
					if len(text) > 128 || !governanceQualityNumber.MatchString(text) {
						return nil, invalid
					}
					parameters[key] = json.RawMessage(text)
				}
				checks[i].Parameters = parameters
			}
			basic = append(basic, checks[i])
		}
		for _, field := range input {
			candidate := field.Field
			// Derived keys are explicitly checked at this node; they need not be a source-table primary key.
			if slices.ContainsFunc(checks, func(check Check) bool { return check.Kind == "business_key" && check.Field == candidate.Name }) {
				candidate.PrimaryKey = true
			}
			fields = append(fields, candidate)
		}
		if err := validateRule(RuleInput{Name: node.ID, SchemaVersion: 1, Checks: basic}, Object{SchemaVersion: 1, Fields: fields}); len(basic) > 0 && err != nil {
			return nil, err
		}
		result = slices.Clone(input)
		for _, check := range checks {
			if check.Kind == "reference_exists" {
				right := flowFieldsByName(inputs[1])[flowLiteralText(check.Parameters["other_field"])]
				for i := range result {
					result[i].Origins = append(slices.Clone(result[i].Origins), right.Origins...)
					result[i].Field.Sensitive = result[i].Field.Sensitive || right.Field.Sensitive
				}
			}
		}
	case "code_map":
		code := node.Code
		if code == nil || code.Standard.Version < 1 || len(code.Values) == 0 || len(code.Values) > 1000 || !slices.Contains([]string{"keep", "null", "reject"}, code.Unmatched) {
			return nil, invalid
		}
		field, ok := known[code.Field]
		standard, found := standards[code.Standard]
		if !ok || !found || field.Field.LogicalType != "string" || standard.LogicalType != "string" || standard.Sensitive && !field.Field.Sensitive {
			return nil, flowProblem("GOVERNANCE_CODE_STANDARD", "码表映射须引用已发布的文本标准，并保留敏感性")
		}
		seen := map[string]bool{}
		for _, value := range code.Values {
			if len(value.From) > 512 || len(value.To) > 512 || seen[value.From] || !slices.Contains(standard.Values, value.To) {
				return nil, invalid
			}
			seen[value.From] = true
		}
		result = slices.Clone(input)
		if code.Unmatched == "null" {
			for i := range result {
				if result[i].Field.Name == code.Field {
					result[i].Field.Nullable = true
				}
			}
		}
	case "output":
		if node.InferModel {
			result = slices.Clone(input)
			for i := range result {
				result[i].Field.PrimaryKey = false
			}
			return result, nil
		}
		if node.Model == nil {
			return nil, invalid
		}
		model, ok := models[*node.Model]
		if !ok || model.Layer == "conceptual" || len(model.Fields) != len(input) {
			return nil, flowProblem("GOVERNANCE_OUTPUT_MODEL", "请选择字段结构匹配的已发布逻辑或物理模型")
		}
		for _, modelField := range model.Fields {
			field, exists := known[modelField.Name]
			if !exists || field.Field.LogicalType != modelField.LogicalType || field.Field.Sensitive && !modelField.Sensitive || field.Field.Nullable && !modelField.Nullable {
				return nil, flowProblem("GOVERNANCE_OUTPUT_CONTRACT", "输出字段的类型、敏感性或可空约束不符合目标模型；请先增加质量门禁或修订模型")
			}
			field.Field.PrimaryKey = modelField.PrimaryKey
			result = append(result, field)
		}
	default:
		return nil, flowProblem("GOVERNANCE_OPERATOR_UNAVAILABLE", "所选节点尚无受控执行器，不允许保存为可执行数据流")
	}
	return result, nil
}
