package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestETLLifecycleAPI(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "etl_ops_", "ops-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	otherID, otherName := businessUser(t, service, "etl_view_", "ops-dept", []string{"operator"})
	other, op := login(t, router, otherName)
	task := workflow.Record{Kind: "etl_task", ID: "lifecycle-task", OwnerID: uid, DepartmentID: "ops-dept", State: "ready", Version: 1, Body: jsonBody(t, workflow.ETLDefinition{Name: "Lifecycle"})}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	call := func(path string, body interface{}, status int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if w := workflowRequest(router, "POST", "/api/v1/etl-tasks/"+task.ID+"/disable", jsonBody(t, map[string]interface{}{"expected_version": 1}), other, op.CSRFToken, "deny-disable"); w.Code != 403 {
		t.Fatalf("other disabled task: %d", w.Code)
	}
	call("/api/v1/etl-tasks/"+task.ID+"/disable", map[string]interface{}{"expected_version": 1}, 200)
	call("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, 409)
	call("/api/v1/etl-tasks/"+task.ID+"/archive", map[string]interface{}{"expected_version": 1}, 409)
	call("/api/v1/etl-tasks/"+task.ID+"/archive", map[string]interface{}{"expected_version": 2}, 200)
	for _, state := range []string{"queued", "committing", "unknown"} {
		r := workflow.Record{Kind: "etl_run", ID: "control-" + state, ParentID: task.ID, OwnerID: uid, DepartmentID: "ops-dept", State: state, Version: 1, Body: jsonBody(t, workflow.ETLRun{Definition: workflow.ETLDefinition{Name: "control"}})}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		status := 200
		if state != "queued" {
			status = 409
		}
		call("/api/v1/etl-runs/"+r.ID+"/cancel", map[string]interface{}{"reason": "停止测试"}, status)
	}
	call("/api/v1/etl-runs/control-unknown/retry", map[string]interface{}{"reason": "重试"}, 409)
	if err := db.Create(&workflow.ETLControl{RunID: "control-unknown", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	call("/api/v1/etl-runs/control-unknown/resolve", map[string]interface{}{"outcome": "failed", "reason": "核对", "evidence": "事务记录"}, 409)
	if err := db.Model(&workflow.ETLControl{}).Where("run_id=?", "control-unknown").Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	call("/api/v1/etl-runs/control-unknown/resolve", map[string]interface{}{"outcome": "failed", "reason": "核对目标事务已回滚", "evidence": "隔离测试事务记录"}, 200)
	if err := db.Create(&workflow.ETLControl{RunID: "control-committing", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	call("/api/v1/etl-runs/control-committing/reconcile", map[string]interface{}{"reason": "检查执行器"}, 409)
	if err := db.Model(&workflow.ETLControl{}).Where("run_id=?", "control-committing").Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	stalled := call("/api/v1/etl-runs/control-committing/reconcile", map[string]interface{}{"reason": "执行器已退出，核实持久化失败"}, 200)
	if stalled["state"] != "unknown" {
		t.Fatalf("inactive execution not recoverable: %v", stalled)
	}
	call("/api/v1/etl-runs/control-committing/retry", map[string]interface{}{"reason": "禁止直接重试"}, 409)
	var alertCount int64
	if err := db.Model(&workflow.ETLAlert{}).Where("run_id=? AND code='EXECUTOR_INACTIVE'", "control-committing").Count(&alertCount).Error; err != nil || alertCount != 1 {
		t.Fatalf("missing reconciliation alert: %d %v", alertCount, err)
	}
	var cancelled workflow.Record
	if err := db.Where("kind='etl_run' AND id='control-queued'").Take(&cancelled).Error; err != nil {
		t.Fatal(err)
	}
	if cancelled.State != "cancelled" {
		t.Fatalf("queued cancellation: %s", cancelled.State)
	}
	for _, path := range []string{"/api/v1/etl-runs/control-queued/events", "/api/v1/etl-runs/control-unknown/events"} {
		w := workflowRequest(router, "GET", path, nil, cookie, "", "")
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("events %d", w.Code)
		}
		w = workflowRequest(router, "GET", path, nil, other, "", "")
		if w.Code != 403 {
			t.Fatalf("events leaked to %s: %d", otherID, w.Code)
		}
	}
}
