package integration

import (
	"context"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGovernanceWorkflowAndSchedule(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "workflow_owner_", "workflow-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	sourceID := "workflow-source-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	if e := db.Create(&workflow.ImportRow{BatchID: sourceID, RowNumber: 1, Data: json.RawMessage(`{"id":1}`)}).Error; e != nil {
		t.Fatal(e)
	}
	managed := workflow.ManagedResource{Name: "工作流来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: sourceID, Fields: fields})}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: sourceID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	seq := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint("workflow-", seq))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		value := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	idOf := func(value map[string]json.RawMessage) string {
		var id string
		if e := json.Unmarshal(value["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	model := workflow.GovernanceDefinition{Name: "工作流模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}}}}
	mid := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+mid+"/versions/1/publish", map[string]interface{}{}, 200)
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: sourceID, Version: 1}, Fields: []string{"id"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Model: &workflow.GovernanceRef{ID: mid, Version: 1}}}}
	fid := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "工作流子任务", OwnerID: uid, StewardID: uid, Dataflow: &graph}}, 201))
	post("dataflows/"+fid+"/versions/1/publish", map[string]interface{}{}, 200)
	ref := workflow.GovernanceRef{ID: fid, Version: 1}
	wf := workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{{ID: "left", Dataflow: ref}, {ID: "right", Dataflow: ref}, {ID: "after", Dataflow: ref, DependsOn: []string{"left", "right"}, Condition: "all_success"}, {ID: "fallback", Dataflow: ref, DependsOn: []string{"left"}, Condition: "all_failure"}}}
	wid := idOf(post("workflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "并行后汇合", OwnerID: uid, StewardID: uid, Workflow: &wf}}, 201))
	post("workflows/"+wid+"/versions/1/publish", map[string]interface{}{}, 200)
	post("dataflows/"+fid+"/versions/1/retire", map[string]interface{}{}, 409)
	runID := idOf(post("workflows/"+wid+"/versions/1/run", map[string]interface{}{}, 201))
	cohort := []string{runID}
	for i := 1; i < 10; i++ {
		cohort = append(cohort, idOf(post("workflows/"+wid+"/versions/1/run", map[string]interface{}{}, 201)))
	}
	server, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stopWorkers := server.StartGovernanceWorkers(context.Background())
	defer stopWorkers()
	stopOrch := server.StartGovernanceOrchestrator(context.Background())
	defer stopOrch()
	await := func(id string) workflow.GovernanceRun {
		t.Helper()
		// Thirty child runs now execute against the external Doris engine.
		for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var run workflow.GovernanceRun
			if e := db.Where("id=?", id).Take(&run).Error; e != nil {
				t.Fatal(e)
			}
			if run.State == "succeeded" {
				return run
			}
			if run.State == "failed" || run.State == "cancelled" || time.Now().After(deadline) {
				t.Fatalf("workflow %s state=%s code=%s", id, run.State, run.ErrorCode)
			}
		}
	}
	for _, id := range cohort {
		await(id)
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"/tasks", nil, cookie, "", "")
	var tasks struct {
		Items []workflow.GovernanceWorkflowTaskRun `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tasks) != nil || len(tasks.Items) != 4 {
		t.Fatalf("tasks=%s", w.Body.String())
	}
	for _, task := range tasks.Items {
		if task.TaskID == "fallback" {
			if task.State != "skipped" || task.ChildRunID != nil {
				t.Fatal("failure condition dispatched after success")
			}
		} else if task.State != "succeeded" || task.ChildRunID == nil {
			t.Fatal("dependency graph not executed")
		}
	}
	var childCount int64
	if e := db.Model(&workflow.GovernanceWorkflowTaskRun{}).Where("parent_run_id=? AND child_run_id IS NOT NULL", runID).Count(&childCount).Error; e != nil || childCount != 3 {
		t.Fatal("parallel dependency children duplicated")
	}
	stopOrch()
	scheduleBody := map[string]interface{}{"workflow": workflow.GovernanceRef{ID: wid, Version: 1}, "timezone": "Asia/Shanghai", "interval_seconds": 60, "concurrency_policy": "forbid", "missed_policy": "latest", "next_run_at": time.Now().Add(-125 * time.Second)}
	scheduleID := idOf(post("schedules", scheduleBody, 201))
	post("workflows/"+wid+"/versions/1/retire", map[string]interface{}{}, 409)
	restartA := server.StartGovernanceOrchestrator(context.Background())
	defer restartA()
	restartB := server.StartGovernanceOrchestrator(context.Background())
	defer restartB()
	var triggered string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var schedule workflow.GovernanceSchedule
		if e := db.Where("id=?", scheduleID).Take(&schedule).Error; e != nil {
			t.Fatal(e)
		}
		if schedule.LastRunID != nil {
			triggered = *schedule.LastRunID
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("persistent schedule did not trigger")
		}
	}
	// External engine work can span another interval; freeze future ticks before
	// asserting that the first scheduled instant was dispatched only once.
	post("schedules/"+scheduleID+"/pause", map[string]interface{}{"base_version": 1}, 200)
	await(triggered)
	var triggers int64
	if e := db.Table("governance_schedule_triggers").Where("schedule_id=?", scheduleID).Count(&triggers).Error; e != nil || triggers != 1 {
		t.Fatalf("restart or multiple orchestrators duplicated schedule=%d %v", triggers, e)
	}
	post("schedules/"+scheduleID+"/resume", map[string]interface{}{"base_version": 1}, 409)
	queued := idOf(post("workflows/"+wid+"/versions/1/run", map[string]interface{}{}, 201))
	post("runs/"+queued+"/cancel", map[string]interface{}{}, 200)
	restartA()
	restartB()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var run workflow.GovernanceRun
		if e := db.Where("id=?", queued).Take(&run).Error; e != nil {
			t.Fatal(e)
		}
		if run.State == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel not durable")
		}
	}
	retried := idOf(post("runs/"+queued+"/retry", map[string]interface{}{}, 201))
	if retried == queued {
		t.Fatal("workflow retry overwrote history")
	}
	var recovered workflow.GovernanceRun
	if e := db.Where("id=?", retried).Take(&recovered).Error; e != nil || recovered.RetryOf == nil || *recovered.RetryOf != queued {
		t.Fatal("workflow recovery reference missing")
	}
}
