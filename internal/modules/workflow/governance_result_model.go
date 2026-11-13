package workflow

import (
	"encoding/json"
	"fmt"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Preserve declared types and sensitivity, but do not infer business keys or
// required values from observations of a particular result set.
func governanceResultModel(fields []governanceFlowField) GovernanceModel {
	model := GovernanceModel{Layer: "logical", Fields: []GovernanceField{}, Relations: []GovernanceRelation{}}
	for _, entry := range fields {
		f := entry.Field
		field := GovernanceField{Name: f.Name, LogicalType: f.LogicalType, Nullable: true, Sensitive: f.Sensitive}
		if f.Precision != nil {
			field.Precision = *f.Precision
		}
		if f.Scale != nil {
			field.Scale = *f.Scale
		}
		model.Fields = append(model.Fields, field)
	}
	return model
}

// The run and draft use one transaction so a failed, cancelled or preview run
// cannot leave a model that appears to have been generated from a real result.
func governanceCreateResultModel(tx *gorm.DB, run GovernanceRun, def governanceFlowExecution, fields []governanceFlowField, labels map[string]string) (*GovernanceRef, error) {
	definition, note, err := governanceResultModelDefinition(run, def, fields, labels)
	if err != nil {
		return nil, err
	}
	for _, userID := range []string{definition.OwnerID, definition.StewardID} {
		if err = governanceMember(tx, userID, run.DepartmentID); err != nil {
			return nil, err
		}
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	head := GovernanceDefinitionRecord{ID: id, Kind: "model", DepartmentID: run.DepartmentID, WorkspaceID: &run.WorkspaceID, LatestVersion: 1}
	if err = tx.Create(&head).Error; err != nil {
		return nil, err
	}
	version := GovernanceVersion{DefinitionID: id, Version: 1, State: "draft", Definition: encode(definition), ChangeNote: note, CreatedBy: run.ActorID}
	if err = tx.Create(&version).Error; err != nil {
		return nil, err
	}
	return &GovernanceRef{ID: id, Version: 1}, nil
}

func governanceResultModelDefinition(run GovernanceRun, def governanceFlowExecution, fields []governanceFlowField, labels map[string]string) (GovernanceDefinition, string, error) {
	if run.Kind != "dataflow" || def.Preview != nil || len(def.Pinned) == 0 {
		return GovernanceDefinition{}, "", problem(409, "GOVERNANCE_RESULT_REQUIRED", "请先完成一次完整的数据流运行，再根据结果生成模型")
	}
	if len(fields) > 200 {
		return GovernanceDefinition{}, "", problem(422, "GOVERNANCE_MODEL_FIELD_LIMIT", "结果字段超过模型支持的 200 个，请先在数据流中保留需要的字段，再重新运行")
	}
	model := governanceResultModel(fields)
	for _, node := range def.Pinned[0].Definition.Dataflow.Nodes {
		if node.Kind == "output" {
			model.DataLayer = outputDataLayer(node.DataLayer)
		}
	}
	for i := range model.Fields {
		model.Fields[i].Description = labels[model.Fields[i].Name]
	}
	name := []rune(def.Pinned[0].Definition.Name)
	if len(name) > 50 {
		name = name[:50]
	}
	definition := GovernanceDefinition{Name: string(name) + "结果模型", OwnerID: def.Pinned[0].Definition.OwnerID, StewardID: def.Pinned[0].Definition.StewardID, Model: &model,
		Description: fmt.Sprintf("根据数据流 %s v%d 的完整运行 %s 生成。请确认一行代表什么、业务范围、唯一标识及字段含义。", def.Flow.ID, def.Flow.Version, run.ID)}
	if err := validateGovernanceDefinition("model", definition); err != nil {
		return GovernanceDefinition{}, "", err
	}
	return definition, "根据完整运行 " + run.ID + " 的输出结构生成；主键及必填要求待业务确认", nil
}

func (s *Server) governanceResultModelSource(tx *gorm.DB, p identity.Principal, id string, lock bool) (GovernanceRun, governanceFlowExecution, governanceFlowReport, []governanceFlowField, error) {
	run, err := governanceRunRead(tx, p, id, lock)
	def := governanceFlowExecution{}
	report := governanceFlowReport{}
	if err != nil {
		return run, def, report, nil, err
	}
	if lightweightRunMarker(run) {
		return run, def, report, nil, problem(409, "LIGHTWEIGHT_MODEL_UNAVAILABLE", "轻量治理不创建结果模型")
	}
	if _, err = governanceWorkspaceRead(tx, p, run.WorkspaceID, false); err != nil {
		return run, def, report, nil, err
	}
	if run.Kind != "dataflow" || run.State != "succeeded" {
		return run, def, report, nil, problem(409, "GOVERNANCE_RESULT_REQUIRED", "请先完成一次完整的数据流运行，再根据结果生成模型")
	}
	def, err = decode[governanceFlowExecution](run.Definition)
	if err != nil {
		return run, def, report, nil, err
	}
	actor, err := s.etlPrincipal(tx.Statement.Context, run.ActorID)
	if err != nil {
		return run, def, report, nil, denied
	}
	if err = s.governanceFlowAuthorize(tx.Statement.Context, actor, def); err != nil {
		return run, def, report, nil, err
	}
	var purged int64
	if err = tx.Table("governance_snapshot_purges").Where("run_id=?", run.ID).Count(&purged).Error; err != nil {
		return run, def, report, nil, err
	}
	if purged > 0 {
		return run, def, report, nil, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "结果已清理，请重新运行后生成模型")
	}
	_, plan, _, err := governanceExecutionPlan(def)
	if err != nil {
		return run, def, report, nil, err
	}
	report, err = decode[governanceFlowReport](run.Result)
	return run, def, report, plan.Outputs[GovernanceFlowEdge{Node: plan.OutputNode}], err
}

func (s *Server) governanceResultModelProposal(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	run, def, report, fields, err := s.governanceResultModelSource(tx, p, c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	definition, note, err := governanceResultModelDefinition(run, def, fields, report.FieldLabels)
	if err != nil {
		s.fail(c, err)
		return
	}
	if report.GeneratedModel != nil {
		_, version, readErr := governanceRead(tx, p, "model", report.GeneratedModel.ID, report.GeneratedModel.Version)
		if readErr != nil {
			s.fail(c, readErr)
			return
		}
		definition, err = decode[GovernanceDefinition](version.Definition)
		if err != nil {
			s.fail(c, err)
			return
		}
		note = version.ChangeNote
	}
	response := gin.H{"definition": definition, "run_id": run.ID, "workspace_id": run.WorkspaceID, "change_note": note}
	if report.GeneratedModel != nil {
		response["generated_model"] = report.GeneratedModel
	}
	c.JSON(200, response)
}

// Historical successful runs can explicitly materialize the draft; GET remains
// read-only and repeated requests reuse the reference protected by the run lock.
func (s *Server) governanceSaveResultModel(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, err := bind[struct{}](b); err != nil {
		return 0, nil, id, err
	}
	run, def, report, fields, err := s.governanceResultModelSource(tx, p, id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if !report.Unmodeled {
		return 0, nil, id, problem(409, "GOVERNANCE_INFERRED_RESULT_REQUIRED", "此运行已有输出模型，请直接查看该模型")
	}
	if _, err = governanceWorkspaceWrite(tx, p, run.WorkspaceID); err != nil {
		return 0, nil, id, err
	}
	status := 200
	if report.GeneratedModel == nil {
		report.GeneratedModel, err = governanceCreateResultModel(tx, run, def, fields, report.FieldLabels)
		if err != nil {
			return 0, nil, id, err
		}
		if err = tx.Model(&GovernanceRun{}).Where("id=? AND state='succeeded'", run.ID).Update("result", encode(report)).Error; err != nil {
			return 0, nil, id, err
		}
		status = 201
	}
	head, version, err := governanceRead(tx, p, "model", report.GeneratedModel.ID, report.GeneratedModel.Version)
	if err != nil {
		return 0, nil, id, err
	}
	return status, governanceVersionResponse(head, version), id, nil
}

// Clone the latest definition so the source run can never overwrite newer work.
func governanceModelBindingDefinition(latest GovernanceDefinition, outputID string, ref GovernanceRef, model GovernanceModel) (GovernanceDefinition, error) {
	draft, err := decode[GovernanceDefinition](encode(latest))
	if err != nil {
		return draft, err
	}
	if draft.Dataflow == nil || model.Layer == "conceptual" {
		return draft, invalid
	}
	for i := range draft.Dataflow.Nodes {
		node := &draft.Dataflow.Nodes[i]
		if node.ID != outputID || node.Kind != "output" {
			continue
		}
		node.Model = &ref
		node.InferModel = false
		if model.DataLayer != "" {
			node.DataLayer = ""
		}
		return draft, nil
	}
	return draft, problem(409, "GOVERNANCE_OUTPUT_CHANGED", "来源输出节点已移除或改变，请在当前数据流中明确选择输出模型")
}

// This endpoint only prepares a reviewable candidate. Saving remains an explicit
// version mutation with the existing optimistic base_version check.
func (s *Server) governanceResultModelBinding(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	run, source, report, _, err := s.governanceResultModelSource(tx, p, c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	modelID := c.Query("model_id")
	var version int
	rawVersion := c.Query("version")
	if _, err = fmt.Sscanf(rawVersion, "%d", &version); err != nil || version < 1 || fmt.Sprint(version) != rawVersion {
		s.fail(c, invalid)
		return
	}
	origin := report.GeneratedModel
	if origin == nil && !report.Unmodeled && report.Model.ID != "" {
		origin = &report.Model
	}
	if origin == nil || origin.ID != modelID {
		s.fail(c, problem(409, "GOVERNANCE_MODEL_SOURCE", "请选择此运行生成或使用的模型版本"))
		return
	}
	modelHead, modelVersion, err := governanceRead(tx, p, "model", modelID, version)
	if err != nil {
		s.fail(c, err)
		return
	}
	if modelVersion.State != "published" || workspaceID(modelHead.WorkspaceID) != run.WorkspaceID {
		s.fail(c, conflict)
		return
	}
	modelDef, err := decode[GovernanceDefinition](modelVersion.Definition)
	if err != nil {
		s.fail(c, err)
		return
	}
	if modelDef.Model == nil {
		s.fail(c, invalid)
		return
	}
	head, latest, err := governanceRead(tx, p, "dataflow", source.Flow.ID, 0)
	if err != nil {
		s.fail(c, err)
		return
	}
	if workspaceID(head.WorkspaceID) != run.WorkspaceID {
		s.fail(c, denied)
		return
	}
	if _, err = governanceWorkspaceWrite(tx, p, run.WorkspaceID); err != nil {
		s.fail(c, err)
		return
	}
	definition, err := decode[GovernanceDefinition](latest.Definition)
	if err != nil {
		s.fail(c, err)
		return
	}
	outputID := report.OutputNode
	if outputID == "" && len(source.Pinned) > 0 && source.Pinned[0].Definition.Dataflow != nil {
		for _, node := range source.Pinned[0].Definition.Dataflow.Nodes {
			if node.Kind == "output" {
				outputID = node.ID
			}
		}
	}
	draft, err := governanceModelBindingDefinition(definition, outputID, GovernanceRef{ID: modelID, Version: version}, *modelDef.Model)
	if err != nil {
		s.fail(c, err)
		return
	}
	if err = governanceValidateReferences(tx, p, head.DepartmentID, run.WorkspaceID, draft); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"base": json.RawMessage(governanceVersionResponse(head, latest)), "definition": draft, "source_flow": source.Flow, "output_node": outputID, "changed": latest.Version != source.Flow.Version})
}
