package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGovernanceModelBinding(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "model_binding_", "binding-dept", []string{"admin"})
	cookie, actor := login(t, router, name)
	sourceID := "binding-source-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	if err := db.Create(&workflow.ImportRow{BatchID: sourceID, RowNumber: 1, Data: json.RawMessage(`{"id":1}`)}).Error; err != nil {
		t.Fatal(err)
	}
	managed := workflow.ManagedResource{Name: "建模来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: sourceID, Fields: fields})}
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: sourceID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
		t.Fatal(err)
	}
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("binding-%s-%d", uid, serial))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	idOf := func(value map[string]json.RawMessage) string {
		var id string
		if err := json.Unmarshal(value["id"], &id); err != nil || id == "" {
			t.Fatal("missing ID")
		}
		return id
	}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: sourceID, Version: 1}, Fields: []string{"id"}}}, {ID: "output", Kind: "output", InferModel: true, DataLayer: "dws", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}}}}
	definition := workflow.GovernanceDefinition{Name: "建模处理", OwnerID: uid, StewardID: uid, Dataflow: &graph}
	flow := idOf(post("dataflows", map[string]interface{}{"definition": definition}, 201))
	post("dataflows/"+flow+"/versions/1/publish", map[string]interface{}{}, 200)
	runID := idOf(post("dataflows/"+flow+"/versions/1/run", map[string]interface{}{}, 201))
	var run workflow.GovernanceRun
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if err := db.Where("id=?", runID).Take(&run).Error; err != nil {
			t.Fatal(err)
		}
		if run.State == "succeeded" {
			break
		}
		if run.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("run %s %s", run.State, run.ErrorCode)
		}
	}
	var report struct {
		GeneratedModel workflow.GovernanceRef `json:"generated_model"`
	}
	if err := json.Unmarshal(run.Result, &report); err != nil || report.GeneratedModel.ID == "" {
		t.Fatal("generated model missing")
	}
	modelID := report.GeneratedModel.ID
	get := func(model string, version int, want int) []byte {
		t.Helper()
		w := workflowRequest(router, "GET", fmt.Sprintf("/api/v1/governance/runs/%s/model-binding?model_id=%s&version=%d", runID, model, version), nil, cookie, "", "")
		if w.Code != want {
			t.Fatalf("binding %d: %s", w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	get(modelID, 1, 409)
	get("unrelated-model", 1, 409)
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	// A newer draft may be the base; its content must remain intact until an explicit save.
	definition.Description = "后续维护的说明"
	post("dataflows/"+flow+"/versions", map[string]interface{}{"definition": definition, "base_version": 1}, 201)
	var candidate struct {
		Base struct {
			Version       int
			LatestVersion int `json:"latest_version"`
		}
		Definition workflow.GovernanceDefinition
		Changed    bool
	}
	if err := json.Unmarshal(get(modelID, 1, 200), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.Base.Version != 2 || candidate.Base.LatestVersion != 2 || !candidate.Changed || candidate.Definition.Description != definition.Description {
		t.Fatal("did not preserve latest revision")
	}
	output := candidate.Definition.Dataflow.Nodes[1]
	if output.Model == nil || *output.Model != report.GeneratedModel || output.InferModel || output.DataLayer != "" {
		t.Fatal("binding did not inherit model purpose")
	}
	var latest workflow.GovernanceVersion
	if err := db.Where("definition_id=? AND version=2", flow).Take(&latest).Error; err != nil {
		t.Fatal(err)
	}
	var persisted workflow.GovernanceDefinition
	if err := json.Unmarshal(latest.Definition, &persisted); err != nil || !persisted.Dataflow.Nodes[1].InferModel {
		t.Fatal("GET rewrote the latest revision")
	}
	var head workflow.GovernanceDefinitionRecord
	if err := db.Where("id=?", flow).Take(&head).Error; err != nil || head.LatestVersion != 2 {
		t.Fatal("binding saved an extra version")
	}
	var count int64
	if err := db.Model(&workflow.GovernanceRun{}).Where("definition->'flow'->>'id'=?", flow).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("binding triggered a run")
	}
	if w := workflowRequest(router, "GET", fmt.Sprintf("/api/v1/governance/runs/%s/model-binding?model_id=%s&version=1", runID, modelID), nil, nil, "", ""); w.Code != 401 {
		t.Fatalf("anonymous access: %d", w.Code)
	}
	otherSpace := idOf(post("workspaces", map[string]interface{}{"name": "其他空间"}, 201))
	if err := db.Model(&workflow.GovernanceDefinitionRecord{}).Where("id=?", modelID).Update("workspace_id", otherSpace).Error; err != nil {
		t.Fatal(err)
	}
	get(modelID, 1, 409)
	if err := db.Model(&workflow.GovernanceDefinitionRecord{}).Where("id=?", modelID).Update("workspace_id", run.WorkspaceID).Error; err != nil {
		t.Fatal(err)
	}
	// A published revision of the same generated model is valid; incompatible types are rejected.
	var modelVersion workflow.GovernanceVersion
	if err := db.Where("definition_id=? AND version=1", modelID).Take(&modelVersion).Error; err != nil {
		t.Fatal(err)
	}
	var modelDefinition workflow.GovernanceDefinition
	if err := json.Unmarshal(modelVersion.Definition, &modelDefinition); err != nil {
		t.Fatal(err)
	}
	modelDefinition.Description = "已确认业务含义"
	post("models/"+modelID+"/versions", map[string]interface{}{"definition": modelDefinition, "base_version": 1}, 201)
	get(modelID, 2, 409)
	post("models/"+modelID+"/versions/2/publish", map[string]interface{}{}, 200)
	get(modelID, 2, 200)
	modelDefinition.Model.Fields[0].LogicalType = "string"
	post("models/"+modelID+"/versions", map[string]interface{}{"definition": modelDefinition, "base_version": 2}, 201)
	post("models/"+modelID+"/versions/3/publish", map[string]interface{}{"migration_note": "确认类型变更，待调整来源"}, 200)
	get(modelID, 3, 400)
}
