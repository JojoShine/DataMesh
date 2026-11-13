package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceWorkflowParametersArtifactsApprovalRecovery(t *testing.T) {
	db, service, router := fixture(t, false)
	dept := "workflow-extended"
	uid, name := businessUser(t, service, "wfext_", dept, []string{"governor", "provider"})
	publisherID, publisherName := businessUser(t, service, "wfext_publish_", dept, []string{"provider"})
	cookie, p := login(t, router, name)
	publisher, publisherP := login(t, router, publisherName)
	seedAssignment(t, db, uid, dept, "govern_resource")
	seedAssignment(t, db, publisherID, dept, "manage_resource")
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "integer"}}
	seed := func(id string, rows []string) string {
		t.Helper()
		for i, row := range rows {
			if e := db.Create(&workflow.ImportRow{BatchID: id, RowNumber: i + 1, Data: json.RawMessage(row)}).Error; e != nil {
				t.Fatal(e)
			}
		}
		if e := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: dept, OwnerID: uid, State: "ready", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: id, DataLayer: "raw", Adapter: "excel", Fields: fields, Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields})})}).Error; e != nil {
			t.Fatal(e)
		}
		grantID := "grant-" + id
		if e := db.Create(&workflow.Record{Kind: "processing_authorization", ID: grantID, DepartmentID: dept, OwnerID: publisherID, State: "active", Version: 1, Body: jsonBody(t, workflow.ProcessingAuthorization{UserID: uid, Resource: workflow.Resource{Type: "table_resource", ID: id, Version: 1}, Fields: []string{"id", "amount"}, Purpose: "数据治理", ExpiresAt: time.Now().Add(2 * time.Hour), AllowDerived: true, AllowSharing: true, SharingBasis: "隔离验证的部门共享依据"})}).Error; e != nil {
			t.Fatal(e)
		}
		return grantID
	}
	sourceID := "wfext-source-" + uid
	grantID := seed(sourceID, []string{`{"id":1,"amount":10}`, `{"id":2,"amount":20}`})
	seq := 0
	post := func(path string, value interface{}, status int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, value), cookie, p.CSRFToken, fmt.Sprint("wfext-", seq))
		if w.Code != status {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	idOf := func(value map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if e := json.Unmarshal(value["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	model := workflow.GovernanceDefinition{Name: "编排输出模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "integer"}}, Contract: workflow.GovernanceContract{FreshnessSeconds: 3600}}}
	mid := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+mid+"/versions/1/publish", map[string]interface{}{}, 200)
	flowGraph := func(source string, parameter bool) workflow.GovernanceDataflow {
		nodes := []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: source, Version: 1}, Fields: []string{"id", "amount"}}}}
		last := "source"
		if parameter {
			nodes = append(nodes, workflow.GovernanceFlowNode{ID: "filter", Kind: "filter", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Condition: &workflow.GovernanceFlowCondition{Field: "amount", Operator: "gte", Value: json.RawMessage(`"0"`), Parameter: "minimum"}})
			last = "filter"
		}
		return workflow.GovernanceDataflow{Nodes: append(nodes, workflow.GovernanceFlowNode{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: last}}, Model: &workflow.GovernanceRef{ID: mid, Version: 1}})}
	}
	createFlow := func(name string, graph workflow.GovernanceDataflow) string {
		t.Helper()
		id := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: name, OwnerID: uid, StewardID: uid, Dataflow: &graph}}, 201))
		post("dataflows/"+id+"/versions/1/publish", map[string]interface{}{}, 200)
		return id
	}
	first := createFlow("参数筛选", flowGraph(sourceID, true))
	second := createFlow("前置成果计算", flowGraph(sourceID, false))
	worker, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stopData := worker.StartGovernanceWorkers(context.Background())
	defer stopData()
	stopWorkflow := worker.StartGovernanceOrchestrator(context.Background())
	defer stopWorkflow()
	await := func(id, want string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(25 * time.Second); ; time.Sleep(25 * time.Millisecond) {
			var run workflow.GovernanceRun
			if e := db.Where("id=?", id).Take(&run).Error; e != nil {
				t.Fatal(e)
			}
			if run.State == want {
				return run
			}
			if run.State == "failed" || run.State == "succeeded" || time.Now().After(deadline) {
				t.Fatalf("%s state=%s code=%s", id, run.State, run.ErrorCode)
			}
		}
	}
	post("dataflows/"+first+"/versions/1/run", map[string]interface{}{"parameters": map[string]string{"undeclared": "ignored"}}, 400)
	direct := idOf(post("dataflows/"+first+"/versions/1/run", map[string]interface{}{"parameters": map[string]string{"minimum": "15"}}, 201))
	directRun := await(direct, "succeeded")
	var directResult map[string]interface{}
	if e = json.Unmarshal(directRun.Result, &directResult); e != nil || directResult["output_rows"] != float64(1) {
		t.Fatal("declared runtime filter was not applied")
	}
	graph := workflow.GovernanceWorkflow{Parameters: map[string]json.RawMessage{"minimum": json.RawMessage(`"15"`)}, Tasks: []workflow.GovernanceWorkflowTask{{ID: "first", Dataflow: workflow.GovernanceRef{ID: first, Version: 1}}, {ID: "second", Dataflow: workflow.GovernanceRef{ID: second, Version: 1}, DependsOn: []string{"first"}, Inputs: map[string]string{"source": "first"}}, {ID: "check", Kind: "quality_check", InputTask: "second", DependsOn: []string{"second"}}, {ID: "publish", Kind: "publish_approval", InputTask: "second", DependsOn: []string{"second", "check"}}}}
	createWorkflow := func(name string, value workflow.GovernanceWorkflow) string {
		t.Helper()
		id := idOf(post("workflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: name, OwnerID: uid, StewardID: uid, Workflow: &value}}, 201))
		post("workflows/"+id+"/versions/1/publish", map[string]interface{}{}, 200)
		return id
	}
	wid := createWorkflow("成果检查与审批", graph)
	parent := idOf(post("workflows/"+wid+"/versions/1/run", map[string]interface{}{"parameters": map[string]string{"minimum": "11"}}, 201))
	for deadline := time.Now().Add(25 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		var row workflow.GovernanceWorkflowTaskRun
		if e := db.Where("parent_run_id=? AND task_id='publish'", parent).Take(&row).Error; e != nil {
			t.Fatal(e)
		}
		if row.State == "waiting_approval" {
			break
		}
		if row.State == "failed" || row.State == "skipped" || time.Now().After(deadline) {
			var tasks []workflow.GovernanceWorkflowTaskRun
			if er := db.Where("parent_run_id=?", parent).Find(&tasks).Error; er != nil {
				t.Fatal(er)
			}
			for _, task := range tasks {
				t.Logf("task %s state=%s code=%s", task.TaskID, task.State, task.ErrorCode)
			}
			t.Fatalf("approval task state=%s code=%s", row.State, row.ErrorCode)
		}
	}
	var outputTask workflow.GovernanceWorkflowTaskRun
	if e = db.Where("parent_run_id=? AND task_id='second'", parent).Take(&outputTask).Error; e != nil || outputTask.ChildRunID == nil {
		t.Fatal("artifact task missing")
	}
	datasetID := *outputTask.ChildRunID
	var forgedGrants int64
	if e = db.Model(&workflow.Record{}).Where("kind='processing_authorization' AND body->'resource'->>'resource_id'=?", datasetID).Count(&forgedGrants).Error; e != nil || forgedGrants != 0 {
		t.Fatal("workflow forged an output processing grant")
	}
	var dataset workflow.Record
	if e = db.Where("kind='dataset' AND id=?", datasetID).Take(&dataset).Error; e != nil || dataset.State != "draft" {
		t.Fatal("waiting approval published automatically")
	}
	post("publication-approvals/"+parent+"/publish/approve", map[string]string{"dataset_id": datasetID, "note": "自行核验"}, 403)
	w := workflowRequest(router, "GET", "/api/v1/governance/publication-approvals", nil, publisher, "", "")
	if w.Code != 200 {
		t.Fatalf("provider queue %d %s", w.Code, w.Body.String())
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/publication-approvals/"+parent+"/publish/approve", jsonBody(t, map[string]string{"dataset_id": datasetID, "note": "核对质量与共享依据后发布"}), publisher, publisherP.CSRFToken, "wfext-provider-approve")
	if w.Code != 200 {
		t.Fatalf("provider approval %d %s", w.Code, w.Body.String())
	}
	await(parent, "succeeded")
	if e = db.Where("kind='dataset' AND id=?", datasetID).Take(&dataset).Error; e != nil || dataset.State != "published" {
		t.Fatal("approved result was not published")
	}
	w = workflowRequest(router, "GET", "/api/v1/governance/runs/"+datasetID+"/records", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("artifact result %d %s", w.Code, w.Body.String())
	}
	var result struct{ Total int }
	if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil || result.Total != 1 {
		t.Fatal("artifact reread prototype instead of prior output")
	}
	post("schedules", map[string]interface{}{"workflow": workflow.GovernanceRef{ID: wid, Version: 1}, "cron": "0 9 * * 1-5", "timezone": "Asia/Shanghai", "interval_seconds": 0, "next_run_at": time.Now().Add(24 * time.Hour), "missed_policy": "latest", "concurrency_policy": "forbid", "parameters": map[string]string{"minimum": "18"}}, 201)
	w = workflowRequest(router, "GET", "/api/v1/governance/schedules/preview?cron="+url.QueryEscape("0 9 * * 1-5")+"&timezone=Asia%2FShanghai&after="+url.QueryEscape(time.Now().UTC().Format(time.RFC3339)), nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("calendar preview %d %s", w.Code, w.Body.String())
	}
	badSource := "wfext-bad-" + uid
	seed(badSource, []string{`{"id":1,"amount":"bad"}`})
	badFlow := createFlow("实际失败输入", flowGraph(badSource, false))
	recoveryGraph := workflow.GovernanceWorkflow{Parameters: graph.Parameters, Tasks: []workflow.GovernanceWorkflowTask{{ID: "complete", Dataflow: workflow.GovernanceRef{ID: first, Version: 1}}, {ID: "broken", Dataflow: workflow.GovernanceRef{ID: badFlow, Version: 1}}, {ID: "after", Dataflow: workflow.GovernanceRef{ID: second, Version: 1}, Inputs: map[string]string{"source": "complete"}, DependsOn: []string{"complete", "broken"}}}}
	recoveryID := createWorkflow("保留成功任务恢复", recoveryGraph)
	old := idOf(post("workflows/"+recoveryID+"/versions/1/run", map[string]interface{}{}, 201))
	await(old, "failed")
	var complete, broken workflow.GovernanceWorkflowTaskRun
	if e = db.Where("parent_run_id=? AND task_id='complete'", old).Take(&complete).Error; e != nil || complete.ChildRunID == nil {
		t.Fatal("successful checkpoint missing")
	}
	if e = db.Where("parent_run_id=? AND task_id='broken'", old).Take(&broken).Error; e != nil || broken.ChildRunID == nil {
		t.Fatal("failure child missing")
	}
	if e = db.Model(&workflow.GovernanceRun{}).Where("id=?", *broken.ChildRunID).Update("error_code", "GOVERNANCE_COMMIT_UNKNOWN").Error; e != nil {
		t.Fatal(e)
	}
	post("runs/"+old+"/resume", map[string]interface{}{}, 409)
	if e = db.Model(&workflow.GovernanceRun{}).Where("id=?", *broken.ChildRunID).Update("error_code", "GOVERNANCE_OUTPUT_TYPE").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&workflow.ImportRow{}).Where("batch_id=?", badSource).Update("data", json.RawMessage(`{"id":1,"amount":1}`)).Error; e != nil {
		t.Fatal(e)
	}
	resumed := idOf(post("runs/"+old+"/resume", map[string]interface{}{}, 201))
	await(resumed, "succeeded")
	var reused workflow.GovernanceWorkflowTaskRun
	if e = db.Where("parent_run_id=? AND task_id='complete'", resumed).Take(&reused).Error; e != nil || reused.ChildRunID == nil || *reused.ChildRunID != *complete.ChildRunID || reused.ReusedFromRun == nil || *reused.ReusedFromRun != old {
		t.Fatal("successful checkpoint was rerun instead of reused")
	}
	var newBroken workflow.GovernanceWorkflowTaskRun
	if e = db.Where("parent_run_id=? AND task_id='broken'", resumed).Take(&newBroken).Error; e != nil || newBroken.ChildRunID == nil || *newBroken.ChildRunID == *broken.ChildRunID {
		t.Fatal("failed child was not retried")
	}
	if e = db.Model(&workflow.Record{}).Where("kind='processing_authorization' AND id=?", grantID).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "GET", "/api/v1/governance/runs/"+datasetID+"/records", nil, cookie, "", "")
	if w.Code != 403 {
		t.Fatalf("ancestor grant revocation did not stop artifact read: %d", w.Code)
	}
}
