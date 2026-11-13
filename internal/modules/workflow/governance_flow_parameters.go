package workflow

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

type governanceFlowRunInput struct {
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

func governanceFlowParameters(graph GovernanceDataflow) (map[string]json.RawMessage, error) {
	defaults := map[string]json.RawMessage{}
	put := func(name string, value json.RawMessage) error {
		if !governanceIdentifier.MatchString(name) || len(value) > 2048 || len(defaults) >= 20 && defaults[name] == nil {
			return invalid
		}
		if previous, ok := defaults[name]; ok && !bytes.Equal(previous, value) {
			return problem(400, "GOVERNANCE_PARAMETER_CONFLICT", "同名参数必须具有相同的默认值")
		}
		defaults[name] = value
		return nil
	}
	for _, node := range graph.Nodes {
		if node.Script != nil {
			for name, value := range node.Script.Parameters {
				var scalar interface{}
				decoder := json.NewDecoder(bytes.NewReader(value))
				decoder.UseNumber()
				if decoder.Decode(&scalar) != nil {
					return nil, invalid
				}
				switch scalar.(type) {
				case string, json.Number, bool:
				default:
					return nil, invalid
				}
				if err := put(name, value); err != nil {
					return nil, err
				}
			}
		}
		if node.Condition != nil && node.Condition.Parameter != "" {
			if node.Condition.Operator == "is_null" || node.Condition.Operator == "not_null" {
				return nil, invalid
			}
			if e := put(node.Condition.Parameter, node.Condition.Value); e != nil {
				return nil, e
			}
		}
		if node.Source != nil {
			if len(node.Source.Parameters) > 50 || len(node.Source.ParameterBindings) > 50 {
				return nil, invalid
			}
			if node.Source.Resource.Type != "api_resource" && (len(node.Source.ParameterBindings) > 0 || len(node.Source.Parameters) > 0) {
				return nil, invalid
			}
			for name, value := range node.Source.Parameters {
				if !valid(name, 128) || len(value) > 4000 {
					return nil, invalid
				}
			}
			for name, parameter := range node.Source.ParameterBindings {
				if !valid(name, 128) {
					return nil, invalid
				}
				if e := put(parameter, encode(node.Source.Parameters[name])); e != nil {
					return nil, e
				}
			}
		}
	}
	return defaults, nil
}

func governanceMaterializeFlow(graph GovernanceDataflow, values map[string]json.RawMessage, inputs map[string]governanceInput) (GovernanceDataflow, error) {
	defaults, e := governanceFlowParameters(graph)
	if e != nil {
		return graph, e
	}
	if len(values) > 20 {
		return graph, invalid
	}
	for name, raw := range values {
		if _, ok := defaults[name]; !ok || len(raw) > 2048 {
			return graph, problem(400, "GOVERNANCE_PARAMETER_INVALID", "参数未声明或值超过边界")
		}
		var scalar interface{}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&scalar) != nil {
			return graph, invalid
		}
		switch scalar.(type) {
		case string, json.Number, bool:
		default:
			return graph, invalid
		}
	}
	copyGraph, e := decode[GovernanceDataflow](encode(graph))
	if e != nil {
		return graph, e
	}
	for i := range copyGraph.Nodes {
		node := &copyGraph.Nodes[i]
		if node.Condition != nil && node.Condition.Parameter != "" {
			if value, ok := values[node.Condition.Parameter]; ok {
				node.Condition.Value = value
			}
		}
		if node.Source != nil {
			if basis, ok := inputs[node.ID]; ok {
				node.Source.Resource = basis.Resource
				node.Source.Fields = fieldNames(basis.Fields)
			}
			for name, parameter := range node.Source.ParameterBindings {
				if value, ok := values[parameter]; ok {
					if node.Source.Parameters == nil {
						node.Source.Parameters = map[string]string{}
					}
					node.Source.Parameters[name] = flowLiteralText(value)
				}
			}
		}
	}
	return copyGraph, nil
}

func governanceExecutionPlan(def governanceFlowExecution) (GovernanceDataflow, governanceFlowPlan, map[GovernanceRef]GovernanceModel, error) {
	var graph GovernanceDataflow
	var plan governanceFlowPlan
	models := map[GovernanceRef]GovernanceModel{}
	if len(def.Pinned) == 0 || def.Pinned[0].Definition.Dataflow == nil {
		return graph, plan, models, invalid
	}
	graph, e := governanceMaterializeFlow(*def.Pinned[0].Definition.Dataflow, def.Parameters, def.Inputs)
	if e != nil {
		return graph, plan, models, e
	}
	templates := map[GovernanceRef]GovernanceQualityTemplate{}
	standards := map[GovernanceRef]GovernanceStandard{}
	schemas := map[string][]Field{}
	for name, input := range def.Inputs {
		schemas[name] = input.Fields
	}
	for _, pinned := range def.Pinned {
		if pinned.Definition.QualityTemplate != nil {
			templates[pinned.Ref] = *pinned.Definition.QualityTemplate
		}
		if pinned.Definition.Model != nil {
			models[pinned.Ref] = *pinned.Definition.Model
		}
		if pinned.Definition.Standard != nil {
			standards[pinned.Ref] = *pinned.Definition.Standard
		}
	}
	graph, e = governanceTemplateGraph(graph, templates)
	if e != nil {
		return graph, plan, models, e
	}
	target := ""
	if def.Preview != nil {
		target = def.Preview.NodeID
	}
	maxSources := 4
	if def.Lightweight != nil {
		if e = validateLightweightGraph(graph); e != nil {
			return graph, plan, models, e
		}
		maxSources = 8
	}
	plan, e = compileGovernanceFlowSources(graph, schemas, models, standards, target, maxSources)
	// Expand private confirmed-entity sources back to the original field contracts.
	for edge, fields := range plan.Outputs {
		for i := range fields {
			origins := []GovernanceFieldOrigin{}
			for _, origin := range fields[i].Origins {
				input := def.Inputs[origin.Node]
				if input.MasterSnapshotID != "" || input.WorkflowTrialArtifact {
					origins = append(origins, input.Origins[origin.Field]...)
				} else {
					origins = append(origins, origin)
				}
			}
			fields[i].Origins = origins
		}
		plan.Outputs[edge] = fields
	}
	plan.BlankTextMissing = def.Lightweight != nil
	if e == nil {
		e = governanceTemplatePlan(graph, templates, plan)
	}
	return graph, plan, models, e
}

func governanceInputFieldsCompatible(actual, expected []Field) bool {
	for _, field := range actual {
		at := slices.IndexFunc(expected, func(other Field) bool { return other.Name == field.Name })
		if at < 0 {
			return false
		}
		other := expected[at]
		if field.LogicalType != other.LogicalType || !reflect.DeepEqual(field.Precision, other.Precision) || !reflect.DeepEqual(field.Scale, other.Scale) {
			return false
		}
	}
	return true
}

func (s *Server) governanceWorkflowArtifactBasis(tx *gorm.DB, p identity.Principal, parent, child string, names []string) (governanceInput, error) {
	var out governanceInput
	if !valid(parent, 64) || !valid(child, 128) {
		return out, invalid
	}
	var count int64
	if e := tx.Table("governance_workflow_tasks t").Joins("JOIN governance_runs r ON r.id=t.parent_run_id").Where("t.parent_run_id=? AND t.child_run_id=? AND t.state='succeeded' AND r.actor_id=? AND r.department_id=?", parent, child, p.UserID, p.DepartmentID).Count(&count).Error; e != nil {
		return out, e
	}
	if count != 1 {
		return out, denied
	}
	var parentRun GovernanceRun
	if e := tx.Where("id=?", parent).Take(&parentRun).Error; e != nil {
		return out, e
	}
	parentDef, e := decode[governanceWorkflowExecution](parentRun.Definition)
	if e != nil {
		return out, e
	}
	if parentDef.Trial != nil {
		return s.workflowTrialArtifactBasis(tx, p, parent, child, names)
	}
	record, e := get(tx, "dataset", child, false)
	if e != nil {
		return out, e
	}
	if record.OwnerID != p.UserID {
		return out, denied
	}
	if slices.Contains([]string{"withdrawn", "disabled", "archived", "retired"}, record.State) {
		return out, conflict
	}
	data, _, childRun, _, e := s.governanceOutputReady(tx.Statement.Context, tx, record)
	if e != nil {
		return out, e
	}
	if childRun.DepartmentID != p.DepartmentID || childRun.ActorID != p.UserID {
		return out, denied
	}
	if !validFields(names, data.Definition.Fields) {
		return out, invalid
	}
	fields := []Field{}
	for _, name := range names {
		for _, field := range data.Definition.Fields {
			if field.Name == name {
				fields = append(fields, field)
			}
		}
	}
	out = governanceInput{Resource: Resource{Type: "governed_dataset", ID: record.ID, Version: record.Version}, Fields: fields, DepartmentID: p.DepartmentID, Administrator: admin(p), ResourceFingerprint: fingerprint(record.Body), ProcessingFingerprint: "workflow:" + parent + ":" + child, WorkflowParent: parent}
	return out, nil
}
