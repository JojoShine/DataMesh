package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"slices"
	"time"
)

// These definitions use the catalog's immutable versions and workspace permissions.
type GovernanceMetric struct {
	Source     GovernanceFlowSource      `json:"source"`
	Dimensions []string                  `json:"dimensions"`
	Filters    []GovernanceFlowCondition `json:"filters"`
	Aggregates []GovernanceFlowAggregate `json:"aggregates"`
	Period     string                    `json:"period"`
	TimeType   string                    `json:"time_type,omitempty"`
	TimeField  string                    `json:"time_field"`
	From       string                    `json:"from"`
	Until      string                    `json:"until"`
	Model      *GovernanceRef            `json:"model,omitempty"`
}
type GovernanceMaster struct {
	ModelRef *GovernanceRef           `json:"model_ref,omitempty"`
	Keys     []string                 `json:"keys"`
	Model    GovernanceModel          `json:"model"`
	Sources  []GovernanceMasterSource `json:"sources"`
}
type GovernanceMasterSource struct {
	ID       string            `json:"id"`
	Resource Resource          `json:"resource"`
	RecordID string            `json:"record_id"`
	Mapping  map[string]string `json:"mapping"`
}
type GovernanceScenario struct {
	Kind       string                    `json:"kind"`
	Reference  GovernanceRef             `json:"reference"`
	Acceptance string                    `json:"acceptance"`
	Inputs     []GovernanceScenarioInput `json:"inputs"`
}
type GovernanceScenarioInput struct {
	TaskID string  `json:"task_id"`
	NodeID string  `json:"node_id"`
	Fields []Field `json:"fields"`
}

func metricGraph(metric GovernanceMetric) (*GovernanceDataflow, error) {
	if !slices.Contains([]string{"all_time", "range"}, metric.Period) || len(metric.Aggregates) == 0 || len(metric.Filters) > 20 {
		return nil, invalid
	}
	if metric.Period == "range" {
		layout := time.RFC3339
		if metric.TimeType == "date" {
			layout = "2006-01-02"
		} else if metric.TimeType != "" && metric.TimeType != "datetime" {
			return nil, invalid
		}
		from, e := time.Parse(layout, metric.From)
		if e != nil {
			return nil, invalid
		}
		until, e := time.Parse(layout, metric.Until)
		if e != nil || !from.Before(until) || !valid(metric.TimeField, 128) {
			return nil, invalid
		}
	} else if metric.TimeType != "" || metric.TimeField != "" || metric.From != "" || metric.Until != "" {
		return nil, invalid
	}
	filters := slices.Clone(metric.Filters)
	for _, filter := range filters {
		if filter.Parameter != "" {
			return nil, invalid
		}
	}
	if metric.Period == "range" {
		filters = append(filters, GovernanceFlowCondition{Field: metric.TimeField, Operator: "gte", Value: encode(metric.From)}, GovernanceFlowCondition{Field: metric.TimeField, Operator: "lt", Value: encode(metric.Until)})
	}
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{{ID: "source", Kind: "source", Source: &metric.Source}}}
	last := "source"
	for i, filter := range filters {
		id := fmt.Sprintf("filter_%d", i+1)
		graph.Nodes = append(graph.Nodes, GovernanceFlowNode{ID: id, Kind: "filter", Inputs: []GovernanceFlowEdge{{Node: last}}, Condition: &filter})
		last = id
	}
	graph.Nodes = append(graph.Nodes, GovernanceFlowNode{ID: "metric", Kind: "aggregate", Inputs: []GovernanceFlowEdge{{Node: last}}, GroupBy: metric.Dimensions, Aggregates: metric.Aggregates}, GovernanceFlowNode{ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "metric"}}, InferModel: metric.Model == nil, Model: metric.Model, DataLayer: "dws"})
	return &graph, nil
}
func validateCapabilityDefinition(kind string, v GovernanceDefinition) error {
	if v.Metric != nil {
		if kind != "dataflow" {
			return invalid
		}
		graph, e := metricGraph(*v.Metric)
		if e != nil {
			return e
		}
		if fingerprint(encode(graph)) != fingerprint(encode(v.Dataflow)) {
			return problem(400, "METRIC_GRAPH_MISMATCH", "指标口径与处理步骤不一致，请从指标口径入口保存")
		}
	}
	if v.Master != nil && kind != "master" || v.Scenario != nil && kind != "scenario" {
		return invalid
	}
	if kind == "master" {
		m := v.Master
		if m == nil || v.Model != nil || v.Dataflow != nil || v.Workflow != nil || v.Standard != nil || v.Metadata != nil || v.Scenario != nil || len(m.Keys) == 0 || len(m.Keys) > 10 || len(m.Sources) < 1 || len(m.Sources) > 4 {
			return invalid
		}
		modelDefinition := v
		modelDefinition.Master = nil
		modelDefinition.Model = &m.Model
		if e := validateGovernanceDefinition("model", modelDefinition); e != nil {
			return e
		}
		names := map[string]bool{}
		for _, field := range m.Model.Fields {
			names[field.Name] = true
			if slices.Contains(m.Keys, field.Name) && (!slices.Contains([]string{"string", "integer"}, field.LogicalType) || field.Sensitive) {
				return problem(400, "MASTER_KEY_TYPE", "实体匹配键请选择非敏感的文本或整数业务编号")
			}
		}
		seenKeys := map[string]bool{}
		for _, key := range m.Keys {
			if !names[key] || seenKeys[key] {
				return invalid
			}
			seenKeys[key] = true
		}
		seen := map[string]bool{}
		for _, source := range m.Sources {
			if !governanceIdentifier.MatchString(source.ID) || seen[source.ID] || !valid(source.RecordID, 128) || !valid(source.Resource.ID, 128) || source.Resource.Version < 1 || len(source.Mapping) != len(names) {
				return invalid
			}
			seen[source.ID] = true
			for key := range names {
				if !valid(source.Mapping[key], 128) {
					return invalid
				}
			}
		}
	}
	if kind == "scenario" {
		scenario := v.Scenario
		if scenario == nil || !slices.Contains([]string{"dataflow", "workflow"}, scenario.Kind) || !validGovernanceRef(&scenario.Reference) || !valid(scenario.Acceptance, 4000) || v.Model != nil || v.Dataflow != nil || v.Workflow != nil || v.Standard != nil || v.Metadata != nil || v.Master != nil {
			return invalid
		}
	}
	return nil
}
func (s *Server) capabilityReferences(tx *gorm.DB, p identity.Principal, space string, v *GovernanceDefinition) error {
	if v.Master != nil {
		modelDefinition := *v
		modelDefinition.Master = nil
		modelDefinition.Model = &v.Master.Model
		if e := governanceValidateReferences(tx, p, p.DepartmentID, space, modelDefinition); e != nil {
			return e
		}
		for _, source := range v.Master.Sources {
			if e := governanceWorkspaceReference(tx, p, space, source.Resource); e != nil {
				return e
			}
			names := []string{source.RecordID}
			for _, field := range v.Master.Model.Fields {
				if !slices.Contains(names, source.Mapping[field.Name]) {
					names = append(names, source.Mapping[field.Name])
				}
			}
			basis, e := s.governanceInputBasis(tx, p, source.Resource, names, true)
			if e != nil {
				return e
			}
			actual := map[string]Field{}
			for _, field := range basis.Fields {
				actual[field.Name] = field
			}
			if !slices.Contains([]string{"string", "integer"}, actual[source.RecordID].LogicalType) {
				return problem(400, "MASTER_SOURCE_ID_TYPE", "来源内部编号请选择文本或整数业务编号")
			}
			if actual[source.RecordID].Sensitive {
				return problem(400, "MASTER_SENSITIVE_KEY", "来源编号不能使用敏感字段，请使用业务内部编号")
			}
			for _, field := range v.Master.Model.Fields {
				input, ok := actual[source.Mapping[field.Name]]
				if !ok || input.LogicalType != field.LogicalType {
					return problem(400, "MASTER_FIELD_CONTRACT", "主数据映射字段的类型必须与实体定义一致")
				}
				if input.Sensitive && !field.Sensitive {
					return problem(400, "MASTER_SENSITIVE_MAPPING", "敏感来源字段必须在实体中保持敏感标记")
				}
				if slices.Contains(v.Master.Keys, field.Name) && input.Sensitive {
					return problem(400, "MASTER_SENSITIVE_KEY", "精确匹配键不能使用需要脱敏的字段")
				}
			}
		}
	}
	if v.Scenario != nil {
		_, source, e := governanceRead(tx, p, v.Scenario.Kind, v.Scenario.Reference.ID, v.Scenario.Reference.Version)
		if e != nil {
			return e
		}
		if source.State != "published" {
			return conflict
		}
		definition, e := decode[GovernanceDefinition](source.Definition)
		if e != nil {
			return e
		}
		inputs := []GovernanceScenarioInput{}
		collect := func(task string, flow GovernanceDataflow) error {
			for _, node := range flow.Nodes {
				if node.Source == nil {
					continue
				}
				if e := governanceWorkspaceReference(tx, p, space, node.Source.Resource); e != nil {
					return e
				}
				basis, e := s.governanceInputBasisParameters(tx, p, node.Source.Resource, node.Source.Fields, true, node.Source.Parameters)
				if e != nil {
					return e
				}
				inputs = append(inputs, GovernanceScenarioInput{TaskID: task, NodeID: node.ID, Fields: basis.Fields})
			}
			return nil
		}
		if v.Scenario.Kind == "dataflow" {
			if definition.Dataflow == nil {
				return invalid
			}
			if e = collect("", *definition.Dataflow); e != nil {
				return e
			}
		} else {
			if definition.Workflow == nil {
				return invalid
			}
			for _, task := range definition.Workflow.Tasks {
				if governanceWorkflowTaskKind(task) != "dataflow" {
					continue
				}
				_, flow, e := governanceRead(tx, p, "dataflow", task.Dataflow.ID, task.Dataflow.Version)
				if e != nil {
					return e
				}
				def, e := decode[GovernanceDefinition](flow.Definition)
				if e != nil {
					return e
				}
				if def.Dataflow == nil {
					return invalid
				}
				if e = collect(task.ID, *def.Dataflow); e != nil {
					return e
				}
			}
		}
		v.Scenario.Inputs = inputs
	}
	return nil
}
func (s *Server) registerGovernanceCapabilities(api, write *gin.RouterGroup) {
	write.POST("/governance/metrics", s.require("governance", "governor"), s.mutate("metric_save", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		return s.saveMetric(tx, p, "", b)
	}))
	write.POST("/governance/metrics/:id/versions", s.require("governance", "governor"), s.mutatePath("metric_version", s.saveMetric))
	write.POST("/governance/scenarios/:id/instantiate", s.require("governance", "governor"), s.mutatePath("scenario_instantiate", s.instantiateScenario))
	api.GET("/governance/scenario-instances", s.require("governance", "governor"), func(c *gin.Context) {
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 15, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		p := principal(c)
		q := workspaceQuery(s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind='scenario_instance'"), "body->>'workspace_id'")
		if !admin(p) {
			q = q.Where("owner_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		var total int64
		rows := []Record{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("created_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		items := []json.RawMessage{}
		for _, row := range rows {
			items = append(items, governanceCapabilityRecord(row))
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	})
	write.POST("/governance/scenario-instances/:id/finalize", s.require("governance", "governor"), s.mutatePath("scenario_finalize", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[VersionAction](b)
		if e != nil {
			return 0, nil, id, e
		}
		row, e := get(tx, "scenario_instance", id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if !admin(p) && (row.OwnerID != p.UserID || row.DepartmentID != p.DepartmentID) {
			return 0, nil, id, denied
		}
		if row.State != "reviewing" || row.Version != input.ExpectedVersion {
			return 0, nil, id, conflict
		}
		body, e := decode[governanceScenarioInstance](row.Body)
		if e != nil {
			return 0, nil, id, e
		}
		if e = governanceWorkspaceMatch(tx.Statement.Context, body.WorkspaceID); e != nil {
			return 0, nil, id, e
		}
		if _, e = governanceWorkspaceWrite(tx, p, body.WorkspaceID); e != nil {
			return 0, nil, id, e
		}
		status, raw, newID, e := s.governanceSave(tx, p, "workflow", "", encode(governanceWrite{Definition: body.Definition, ChangeNote: "场景方案实例：已复核所有新输入及子数据流版本"}))
		if e != nil {
			return 0, nil, id, e
		}
		body.WorkflowID = newID
		row.Body = encode(body)
		row.State = "instantiated"
		row.Version++
		if e = save(tx, &row); e != nil {
			return 0, nil, id, e
		}
		return status, raw, newID, nil
	}))
	s.registerGovernanceMaster(api, write)
}
func (s *Server) saveMetric(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[governanceWrite](b)
	if e != nil {
		return 0, nil, id, e
	}
	if input.Definition.Metric == nil {
		return 0, nil, id, invalid
	}
	metric := input.Definition.Metric
	names := []string{}
	include := func(name string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range metric.Dimensions {
		include(name)
	}
	for _, condition := range metric.Filters {
		include(condition.Field)
	}
	for _, aggregate := range metric.Aggregates {
		include(aggregate.Source)
	}
	if metric.Period == "range" {
		include(metric.TimeField)
	}
	metric.Source.Fields = names
	graph, e := metricGraph(*input.Definition.Metric)
	if e != nil {
		return 0, nil, id, e
	}
	input.Definition.Dataflow = graph
	return s.governanceSave(tx, p, "dataflow", id, encode(input))
}
func (s *Server) instantiateScenario(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		Version  int    `json:"version"`
		Name     string `json:"name"`
		Bindings []struct {
			TaskID     string            `json:"task_id"`
			NodeID     string            `json:"node_id"`
			Resource   Resource          `json:"resource"`
			Parameters map[string]string `json:"parameters"`
		} `json:"bindings"`
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	if !valid(input.Name, 200) {
		return 0, nil, id, invalid
	}
	head, row, e := governanceRead(tx, p, "scenario", id, input.Version)
	if e != nil {
		return 0, nil, id, e
	}
	if row.State != "published" {
		return 0, nil, id, conflict
	}
	definition, e := decode[GovernanceDefinition](row.Definition)
	if e != nil || definition.Scenario == nil {
		return 0, nil, id, invalid
	}
	scenario := definition.Scenario
	if len(input.Bindings) != len(scenario.Inputs) {
		return 0, nil, id, invalid
	}
	bound := map[string]bool{}
	bindFlow := func(task string, flow *GovernanceDataflow) error {
		for i, node := range flow.Nodes {
			if node.Source == nil {
				continue
			}
			slot := task + "/" + node.ID
			index := slices.IndexFunc(input.Bindings, func(b struct {
				TaskID     string            `json:"task_id"`
				NodeID     string            `json:"node_id"`
				Resource   Resource          `json:"resource"`
				Parameters map[string]string `json:"parameters"`
			}) bool {
				return b.TaskID == task && b.NodeID == node.ID
			})
			if index < 0 || bound[slot] {
				return invalid
			}
			binding := input.Bindings[index]
			bound[slot] = true
			contract := slices.IndexFunc(scenario.Inputs, func(c GovernanceScenarioInput) bool { return c.TaskID == task && c.NodeID == node.ID })
			if contract < 0 {
				return conflict
			}
			if e := governanceWorkspaceReference(tx, p, workspaceID(head.WorkspaceID), binding.Resource); e != nil {
				return e
			}
			basis, e := s.governanceInputBasisParameters(tx, p, binding.Resource, node.Source.Fields, true, binding.Parameters)
			if e != nil {
				return e
			}
			if !governanceScenarioFieldsCompatible(basis.Fields, scenario.Inputs[contract].Fields) {
				return problem(400, "SCENARIO_INPUT_CONTRACT", "重新绑定的来源字段与方案输入契约不兼容")
			}
			replacement := *node.Source
			replacement.Resource = binding.Resource
			replacement.Parameters = binding.Parameters
			replacement.ParameterBindings = nil
			flow.Nodes[i].Source = &replacement
		}
		return nil
	}
	_, source, e := governanceRead(tx, p, scenario.Kind, scenario.Reference.ID, scenario.Reference.Version)
	if e != nil {
		return 0, nil, id, e
	}
	if source.State != "published" {
		return 0, nil, id, conflict
	}
	copyDef, e := decode[GovernanceDefinition](source.Definition)
	if e != nil {
		return 0, nil, id, e
	}
	copyDef.Name = input.Name
	copyDef.Description = "场景方案 " + definition.Name + fmt.Sprintf(" v%d；", row.Version) + scenario.Acceptance
	if copyDef.Workflow != nil {
		copyDef.Workflow.Parameters = nil
	}
	copyDef.OwnerID = p.UserID
	copyDef.StewardID = p.UserID
	if scenario.Kind == "dataflow" {
		if e = bindFlow("", copyDef.Dataflow); e != nil {
			return 0, nil, id, e
		}
		copyDef.Metric = nil
	} else {
		for i, task := range copyDef.Workflow.Tasks {
			if governanceWorkflowTaskKind(task) != "dataflow" {
				continue
			}
			_, sourceFlow, e := governanceRead(tx, p, "dataflow", task.Dataflow.ID, task.Dataflow.Version)
			if e != nil {
				return 0, nil, id, e
			}
			flow, e := decode[GovernanceDefinition](sourceFlow.Definition)
			if e != nil {
				return 0, nil, id, e
			}
			if e = bindFlow(task.ID, flow.Dataflow); e != nil {
				return 0, nil, id, e
			}
			flow.Metric = nil
			flow.OwnerID = p.UserID
			flow.StewardID = p.UserID
			flow.Name = input.Name + " · " + task.ID
			_, _, newID, e := s.governanceSave(tx, p, "dataflow", "", encode(governanceWrite{Definition: flow, ChangeNote: "场景方案实例输入重新授权", ExecutionMode: sourceFlow.ExecutionMode}))
			if e != nil {
				return 0, nil, id, e
			}
			// Child definitions remain drafts; the workflow must be published after its new children are reviewed.
			copyDef.Workflow.Tasks[i].Dataflow = GovernanceRef{ID: newID, Version: 1}
		}
		if len(bound) != len(scenario.Inputs) {
			return 0, nil, id, invalid
		}
		bundle := governanceScenarioInstance{WorkspaceID: workspaceID(head.WorkspaceID), Scenario: GovernanceRef{ID: id, Version: row.Version}, Definition: copyDef}
		record, e := newRecord("scenario_instance", head.DepartmentID, p.UserID, id, "reviewing", bundle)
		if e != nil {
			return 0, nil, id, e
		}
		e = tx.Create(&record).Error
		return 201, governanceCapabilityRecord(record), record.ID, e
	}
	if len(bound) != len(scenario.Inputs) {
		return 0, nil, id, invalid
	}
	return s.governanceSave(tx, p, scenario.Kind, "", encode(governanceWrite{Definition: copyDef, ChangeNote: fmt.Sprintf("由场景方案 %s v%d 实例化，重新绑定授权输入", id, row.Version), ExecutionMode: source.ExecutionMode}))
}

type governanceScenarioInstance struct {
	WorkspaceID string               `json:"workspace_id"`
	Scenario    GovernanceRef        `json:"scenario"`
	Definition  GovernanceDefinition `json:"definition"`
	WorkflowID  string               `json:"workflow_id"`
}

func governanceScenarioFieldsCompatible(actual, expected []Field) bool {
	if len(actual) != len(expected) || !governanceInputFieldsCompatible(actual, expected) {
		return false
	}
	for _, field := range actual {
		index := slices.IndexFunc(expected, func(other Field) bool { return other.Name == field.Name })
		if index < 0 {
			return false
		}
		contract := expected[index]
		if field.Sensitive != contract.Sensitive || field.Nullable && !contract.Nullable || contract.PrimaryKey && !field.PrimaryKey {
			return false
		}

	}
	return true
}
