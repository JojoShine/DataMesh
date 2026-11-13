package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGovernanceQualityIssueLifecycle(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "issue_owner_", "issue-dept", []string{"admin"})
	otherID, otherName := businessUser(t, service, "issue_other_", "other-issue-dept", []string{"governor"})
	cookie, actor := login(t, router, name)
	other, _ := login(t, router, otherName)
	inputID := "issue-input-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "integer"}}
	for i, row := range []string{`{"id":1,"amount":4}`, `{"id":2,"amount":-1}`} {
		if e := db.Create(&workflow.ImportRow{BatchID: inputID, RowNumber: i + 1, Data: json.RawMessage(row)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	resource := workflow.ManagedResource{Name: "质量来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: inputID, Fields: fields})}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: inputID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, resource)}).Error; e != nil {
		t.Fatal(e)
	}
	seq := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("issues-", seq))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		r := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &r); e != nil {
			t.Fatal(e)
		}
		return r
	}
	idOf := func(r map[string]json.RawMessage) string {
		var id string
		if e := json.Unmarshal(r["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	model := workflow.GovernanceDefinition{Name: "质量模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "integer"}}, Contract: workflow.GovernanceContract{MaxFailureRate: 0.5}}}
	modelID := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: inputID, Version: 1}, Fields: []string{"id", "amount"}}}, {ID: "scope", Kind: "filter", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Condition: &workflow.GovernanceFlowCondition{Field: "amount", Operator: "gte", Value: json.RawMessage(`0`), Parameter: "minimum"}}, {ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "scope"}}, Checks: []workflow.Check{{Field: "amount", Kind: "range", Blocking: true, Parameters: map[string]json.RawMessage{"min": json.RawMessage("0")}}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, Model: &workflow.GovernanceRef{ID: modelID, Version: 1}}}}
	flowID := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "质量数据流", OwnerID: uid, StewardID: uid, Dataflow: &graph}}, 201))
	post("dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
	await := func(id string) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+id, nil, cookie, "", "")
			var run workflow.GovernanceRun
			if e := json.Unmarshal(w.Body.Bytes(), &run); e != nil || w.Code != 200 {
				t.Fatalf("read run=%s", w.Body.String())
			}
			if run.State == "succeeded" {
				return
			}
			if run.State == "failed" || time.Now().After(deadline) {
				t.Fatalf("run=%s", w.Body.String())
			}
		}
	}
	runID := idOf(post("dataflows/"+flowID+"/versions/1/run", map[string]interface{}{"parameters": map[string]int{"minimum": -10}}, 201))
	await(runID)
	issueBody := map[string]interface{}{"run_id": runID, "assignee_id": otherID, "due_at": time.Now().Add(24 * time.Hour)}
	post("quality-issues", issueBody, 400)
	issueBody["assignee_id"] = uid
	issue := post("quality-issues", issueBody, 201)
	issueID := idOf(issue)
	preview := workflowRequest(router, "GET", "/api/v1/governance/lifecycle/"+runID+"/preview", nil, cookie, "", "")
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "QUALITY_ISSUE_OPEN") {
		t.Fatalf("open issue did not protect evidence: %d %s", preview.Code, preview.Body.String())
	}
	post("quality-issues", issueBody, 409)
	if w := workflowRequest(router, "GET", "/api/v1/governance/quality-issues/"+issueID, nil, other, "", ""); w.Code != 403 {
		t.Fatalf("cross-department issue=%d", w.Code)
	}
	post("quality-issues/"+issueID+"/close", map[string]interface{}{"base_version": 1}, 409)
	post("quality-issues/"+issueID+"/notes", map[string]interface{}{"base_version": 1, "note": "核对来源后复测"}, 200)
	post("quality-issues/"+issueID+"/notes", map[string]interface{}{"base_version": 1, "note": "过期覆盖"}, 409)
	retest := post("quality-issues/"+issueID+"/retest", map[string]interface{}{"base_version": 2}, 201)
	var next string
	if e := json.Unmarshal(retest["retest_run_id"], &next); e != nil {
		t.Fatal(e)
	}
	await(next)
	var replay workflow.GovernanceRun
	if e := db.Where("id=?", next).Take(&replay).Error; e != nil {
		t.Fatal(e)
	}
	var scope struct {
		Parameters map[string]int `json:"parameters"`
	}
	if e := json.Unmarshal(replay.Definition, &scope); e != nil || scope.Parameters["minimum"] != -10 {
		t.Fatal("retest lost original runtime parameters")
	}
	post("quality-issues/"+issueID+"/close", map[string]interface{}{"base_version": 3}, 409)
	if e := db.Model(&workflow.ImportRow{}).Where("batch_id=? AND row_number=2", inputID).Update("data", json.RawMessage(`{"id":2,"amount":2}`)).Error; e != nil {
		t.Fatal(e)
	}
	retest = post("quality-issues/"+issueID+"/retest", map[string]interface{}{"base_version": 3}, 201)
	if e := json.Unmarshal(retest["retest_run_id"], &next); e != nil {
		t.Fatal(e)
	}
	await(next)
	post("quality-issues/"+issueID+"/close", map[string]interface{}{"base_version": 4}, 200)
	w := workflowRequest(router, "GET", "/api/v1/governance/quality-issues/"+issueID, nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "核对来源后复测") || !strings.Contains(w.Body.String(), `"state":"closed"`) {
		t.Fatalf("issue history=%d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"quality-issues?page_size=1", "operations", "lineage?resource_id=" + inputID} {
		w := workflowRequest(router, "GET", "/api/v1/governance/"+path, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"amount":-1`) {
			t.Fatal("record values leaked")
		}
	}
	w = workflowRequest(router, "GET", "/api/v1/governance/lineage?resource_id="+inputID, nil, other, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), runID) {
		t.Fatalf("cross-department lineage=%s", w.Body.String())
	}
	var audits int64
	if e := db.Table("audit_records").Where("actor_id=? AND action LIKE 'governance_quality_%'", uid).Count(&audits).Error; e != nil || audits < 5 {
		t.Fatalf("missing audit=%d %v", audits, e)
	}
	if e := db.Model(&workflow.Record{}).Where("id=?", inputID).Update("state", "disabled").Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "GET", "/api/v1/governance/lineage?resource_id="+inputID, nil, cookie, "", "")
	if w.Code != 409 && w.Code != 403 {
		t.Fatalf("revoked source lineage=%d %s", w.Code, w.Body.String())
	}
}
