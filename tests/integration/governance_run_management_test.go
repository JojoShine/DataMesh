package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGovernanceDataflowRunManagement(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "flow_manage_", "flow-management", []string{"admin"})
	cookie, p := login(t, router, name)
	sourceID := "run-source-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	input := workflow.ManagedResource{Name: "运行控制来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: sourceID, Fields: fields})}
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: sourceID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, input)}).Error; err != nil {
		t.Fatal(err)
	}
	modelID := "manage-model-" + uid
	model := workflow.GovernanceDefinition{Name: "运行控制模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}}}}
	if err := db.Create(&workflow.GovernanceDefinitionRecord{ID: modelID, Kind: "model", DepartmentID: p.DepartmentID, LatestVersion: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&workflow.GovernanceVersion{DefinitionID: modelID, Version: 1, State: "published", Definition: jsonBody(t, model), CreatedBy: uid}).Error; err != nil {
		t.Fatal(err)
	}
	modelRef := workflow.GovernanceRef{ID: modelID, Version: 1}
	flowID := "manage-flow-" + uid
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: sourceID, Version: 1}, Fields: []string{"id"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Model: &modelRef}}}
	definition := workflow.GovernanceDefinition{Name: "固定版本重试", OwnerID: uid, StewardID: uid, Dataflow: &graph}
	if err := db.Create(&workflow.GovernanceDefinitionRecord{ID: flowID, Kind: "dataflow", DepartmentID: p.DepartmentID, LatestVersion: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&workflow.GovernanceVersion{DefinitionID: flowID, Version: 1, State: "published", Definition: jsonBody(t, definition), CreatedBy: uid}).Error; err != nil {
		t.Fatal(err)
	}
	post := func(path, key string, want int) workflow.GovernanceRun {
		t.Helper()
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, []byte(`{}`), cookie, p.CSRFToken, key)
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		var run workflow.GovernanceRun
		if want < 300 {
			if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}
	original := post("dataflows/"+flowID+"/versions/1/run", "manage-create", 201)
	post("runs/"+original.ID+"/cancel", "manage-cancel", 200)
	retried := post("runs/"+original.ID+"/retry", "manage-retry", 201)
	if retried.ID == original.ID || retried.RetryOf == nil || *retried.RetryOf != original.ID || retried.State != "queued" {
		t.Fatalf("not a new linked attempt: %+v", retried)
	}
	replay := post("runs/"+original.ID+"/retry", "manage-retry", 201)
	if replay.ID != retried.ID {
		t.Fatal("idempotent retry duplicated run")
	}
	var execution struct {
		Flow workflow.GovernanceRef `json:"flow"`
	}
	if err := json.Unmarshal(retried.Definition, &execution); err != nil || execution.Flow.ID != flowID || execution.Flow.Version != 1 {
		t.Fatal("retry did not pin original version")
	}
	post("runs/"+retried.ID+"/retry", "retry-active", 409)
	for _, state := range []string{"failed", "interrupted", "cancelled"} {
		id := "other-" + state + "-" + uid
		if err := db.Create(&workflow.GovernanceRun{ID: id, Kind: "profile", DepartmentID: p.DepartmentID, ActorID: uid, State: state, Definition: jsonBody(t, map[string]interface{}{"input": map[string]interface{}{"resource": map[string]string{"resource_id": sourceID}}}), Result: json.RawMessage(`null`), LeaseExpiresAt: time.Unix(0, 0)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []struct {
		filter string
		count  int
	}{{"kind=dataflow&definition_id=" + flowID, 2}, {"kind=profile&resource_id=" + sourceID, 3}, {"kind=dataflow&definition_id=absent", 0}} {
		w := workflowRequest(router, "GET", "/api/v1/governance/runs?"+query.filter, nil, cookie, "", "")
		var result struct {
			Total int `json:"total"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total != query.count {
			t.Fatalf("filter %s: %d %s", query.filter, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"kind=sql", "state=unknown", "definition_id=" + strings.Repeat("x", 129)} {
		w := workflowRequest(router, "GET", "/api/v1/governance/runs?"+query, nil, cookie, "", "")
		if w.Code != 400 {
			t.Fatalf("invalid filter %s: %d", query, w.Code)
		}
	}
	post("runs/"+retried.ID+"/cancel", "retry-cancel", 200)
	if err := db.Model(&workflow.Record{}).Where("kind='table_resource' AND id=?", sourceID).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	post("runs/"+retried.ID+"/retry", fmt.Sprint("changed-source-", uid), 409)
}
