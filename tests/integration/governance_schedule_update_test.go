package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGovernanceScheduleUpdatePreservesHistoryAndValidatesTarget(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "schedule_update_", "schedule-update", []string{"admin"})
	cookie, actor := login(t, router, name)
	sourceID := "schedule-update-source-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	managed := workflow.ManagedResource{Name: "调度来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: sourceID, Fields: fields})}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: sourceID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	seq := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("schedule-update-%d", seq))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	idOf := func(row map[string]json.RawMessage) string {
		var id string
		if e := json.Unmarshal(row["id"], &id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: sourceID, Version: 1}, Fields: []string{"id"}}},
		{ID: "filter", Kind: "filter", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Condition: &workflow.GovernanceFlowCondition{Field: "id", Operator: "gte", Parameter: "minimum", Value: json.RawMessage("1")}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "filter"}}, InferModel: true, DataLayer: "dws"},
	}}
	def := workflow.GovernanceDefinition{Name: "调度版本调整", OwnerID: uid, StewardID: uid, Dataflow: &graph}
	fid := idOf(post("dataflows", map[string]interface{}{"definition": def}, 201))
	post("dataflows/"+fid+"/versions/1/publish", map[string]interface{}{}, 200)
	post("dataflows/"+fid+"/versions", map[string]interface{}{"definition": def, "base_version": 1}, 201)
	body := map[string]interface{}{"dataflow": workflow.GovernanceRef{ID: fid, Version: 1}, "parameters": map[string]int{"minimum": 2}, "timezone": "Asia/Shanghai", "interval_seconds": 3600, "concurrency_policy": "forbid", "missed_policy": "skip", "next_run_at": time.Now().Add(24 * time.Hour)}
	sid := idOf(post("schedules", body, 201))
	path := "schedules/" + sid + "/update"
	post("schedules/"+sid+"/pause", map[string]int{"base_version": 1}, 200)
	if e := db.Exec("INSERT INTO governance_schedule_triggers(schedule_id,scheduled_at,code) VALUES(?,?,?)", sid, time.Now().Add(-time.Hour), "SCHEDULE_MISSED_SKIPPED").Error; e != nil {
		t.Fatal(e)
	}
	body["base_version"] = 2
	body["dataflow"] = workflow.GovernanceRef{ID: fid, Version: 2}
	post(path, body, 409) // Draft target cannot replace a published version.
	post("dataflows/"+fid+"/versions/2/publish", map[string]interface{}{}, 200)
	body["parameters"] = map[string]int{"unknown": 2}
	post(path, body, 400)
	body["parameters"] = map[string]int{"minimum": 3}
	body["interval_seconds"] = 7200
	updated := post(path, body, 200)
	if idOf(updated) != sid || string(updated["state"]) != `"paused"` || string(updated["version"]) != "3" || !strings.Contains(string(updated["parameters"]), `"minimum":3`) {
		t.Fatalf("update changed identity/state or lost values: %v", updated)
	}
	post(path, body, 409) // Stale editor must not overwrite an accepted update.
	// Concurrent editors sharing a base version may accept only one update.
	body["base_version"] = 3
	statuses := make(chan int, 2)
	var editors sync.WaitGroup
	for editor := 0; editor < 2; editor++ {
		editors.Add(1)
		go func(editor int) {
			defer editors.Done()
			w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("schedule-editor-%d", editor))
			statuses <- w.Code
		}(editor)
	}
	editors.Wait()
	close(statuses)
	accepted, stale := 0, 0
	for status := range statuses {
		if status == 200 {
			accepted++
		}
		if status == 409 {
			stale++
		}
	}
	if accepted != 1 || stale != 1 {
		t.Fatalf("concurrent updates accepted=%d stale=%d", accepted, stale)
	}
	body["base_version"] = 4
	delete(body, "dataflow")
	body["workflow"] = workflow.GovernanceRef{ID: fid, Version: 2}
	post(path, body, 400)
	delete(body, "workflow")
	body["dataflow"] = workflow.GovernanceRef{ID: "different", Version: 2}
	post(path, body, 400)
	body["dataflow"] = workflow.GovernanceRef{ID: fid, Version: 2}
	// A plan adjustment must wait for workspace serialization before locking its row.
	barrier := db.Begin()
	if barrier.Error != nil {
		t.Fatal(barrier.Error)
	}
	defer barrier.Rollback()
	if e := barrier.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		t.Fatal(e)
	}
	waitingPID := make(chan int, 1)
	callback := "test:schedule_advisory_order"
	if e := db.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.SQL.String() != "SELECT pg_advisory_xact_lock(619483219)" {
			return
		}
		var pid int
		if e := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT pg_backend_pid()").Scan(&pid); e != nil {
			tx.AddError(e)
			return
		}
		select {
		case waitingPID <- pid:
		default:
		}
	}); e != nil {
		t.Fatal(e)
	}
	defer db.Callback().Raw().Remove(callback)
	payload := jsonBody(t, body)
	updateResult := make(chan int, 1)
	go func() {
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, payload, cookie, actor.CSRFToken, "schedule-lock-order")
		updateResult <- w.Code
	}()
	var pid int
	select {
	case pid = <-waitingPID:
	case <-time.After(10 * time.Second):
		t.Fatal("update did not reach advisory lock")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var waiting bool
		if e := db.Raw("SELECT COALESCE(wait_event='advisory',false) FROM pg_stat_activity WHERE pid=?", pid).Scan(&waiting).Error; e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("update did not wait on workspace serialization")
		}
	}
	if e := barrier.Exec("SELECT id FROM governance_schedules WHERE id=? FOR UPDATE NOWAIT", sid).Error; e != nil {
		t.Fatal("adjustment locked plan before workspace serialization")
	}
	if e := barrier.Rollback().Error; e != nil {
		t.Fatal(e)
	}
	select {
	case status := <-updateResult:
		if status != 200 {
			t.Fatalf("serialized adjustment status=%d", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("adjustment did not finish")
	}
	body["base_version"] = 5
	if e := db.Model(&workflow.Record{}).Where("id=?", sourceID).Update("state", "disabled").Error; e != nil {
		t.Fatal(e)
	}
	post(path, body, 409)
	history := workflowRequest(router, "GET", "/api/v1/governance/schedules/"+sid+"/triggers", nil, cookie, "", "")
	if history.Code != 200 || !strings.Contains(history.Body.String(), "SCHEDULE_MISSED_SKIPPED") {
		t.Fatalf("history lost: %s", history.Body.String())
	}
	var count int64
	if e := db.Model(&workflow.GovernanceSchedule{}).Where("workflow_id=?", fid).Count(&count).Error; e != nil || count != 1 {
		t.Fatalf("duplicated schedule count=%d %v", count, e)
	}
	preview := workflowRequest(router, "GET", "/api/v1/governance/schedules/preview?timezone=UTC&interval_seconds=3600&after="+url.QueryEscape("2030-01-01T00:00:00Z"), nil, cookie, "", "")
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "2030-01-01T01:00:00Z") {
		t.Fatalf("preview: %s", preview.Body.String())
	}
}
