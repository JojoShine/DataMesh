package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceImpactAlertsRetentionMigration(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "maintenance_", "maintenance-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	seq := 0
	post := func(path string, body interface{}, status int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint("maintenance-", seq))
		if w.Code != status {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		v := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	idOf := func(v map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if e := json.Unmarshal(v["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	source := "maintenance-source-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	if e := db.Create(&workflow.ImportRow{BatchID: source, RowNumber: 1, Data: json.RawMessage(`{"id":1}`)}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: source, DepartmentID: p.DepartmentID, OwnerID: uid, State: "ready", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: "受控输入", DataLayer: "raw", Adapter: "excel", Fields: fields, Config: jsonBody(t, workflow.TableConfig{BatchID: source, Fields: fields})})}).Error; e != nil {
		t.Fatal(e)
	}
	model := workflow.GovernanceDefinition{Name: "保留成果模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}}}}
	mid := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+mid+"/versions/1/publish", map[string]interface{}{}, 200)
	createFlow := func(resource string, kind string) string {
		t.Helper()
		graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: kind, ID: resource, Version: 1}, Fields: []string{"id"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Model: &workflow.GovernanceRef{ID: mid, Version: 1}}}}
		fid := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "生命周期验证流", OwnerID: uid, StewardID: uid, Dataflow: &graph}}, 201))
		post("dataflows/"+fid+"/versions/1/publish", map[string]interface{}{}, 200)
		return fid
	}
	fid := createFlow(source, "table_resource")
	worker, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stop := worker.StartGovernanceWorkers(context.Background())
	defer stop()
	await := func(id, state string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var r workflow.GovernanceRun
			if e := db.Where("id=?", id).Take(&r).Error; e != nil {
				t.Fatal(e)
			}
			if r.State == state {
				return
			}
			if r.State == "failed" && state != "failed" {
				t.Fatalf("run failed %s", r.ErrorCode)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("run timeout")
	}
	first := idOf(post("dataflows/"+fid+"/versions/1/run", map[string]interface{}{}, 201))
	await(first, "succeeded")
	second := idOf(post("dataflows/"+fid+"/versions/1/run", map[string]interface{}{}, 201))
	await(second, "succeeded")
	old := time.Now().Add(-72 * time.Hour)
	if e = db.Model(&workflow.GovernanceRun{}).Where("id=?", first).Update("finished_at", old).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&workflow.Record{}).Where("kind='dataset' AND id=?", first).Update("created_at", old).Error; e != nil {
		t.Fatal(e)
	}
	get := func(path string, status int) string {
		t.Helper()
		w := workflowRequest(router, "GET", "/api/v1/governance/"+path, nil, cookie, "", "")
		if w.Code != status {
			t.Fatalf("GET %s=%d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	impact := get("impact?kind=resource&id="+source, 200)
	if !strings.Contains(impact, mid) && !strings.Contains(impact, fid) {
		t.Fatal("impact lacks actual dataflow")
	}
	if !strings.Contains(impact, first) {
		t.Fatal("impact lacks real output")
	}
	get("impact?kind=run&id="+first, 200)
	post("retention-policies", map[string]interface{}{"flow_id": fid, "days": 1, "keep_last": 1, "enabled": false}, 201)
	stop()
	profile := idOf(post("profiles", map[string]interface{}{"resource": workflow.Resource{Type: "governed_dataset", ID: first, Version: 1}, "fields": []string{"id"}, "mode": "full"}, 201))
	protected := get("lifecycle/"+first+"/preview", 200)
	if !strings.Contains(protected, profile) || !strings.Contains(protected, "DOWNSTREAM_IN_USE") {
		t.Fatal("queued profile did not protect its source snapshot")
	}
	post("runs/"+profile+"/cancel", map[string]interface{}{}, 200)
	stop = worker.StartGovernanceWorkers(context.Background())
	defer stop()
	down := createFlow(first, "governed_dataset")
	plan := idOf(post("migrations", map[string]interface{}{"source_id": first, "target_id": second}, 201))
	get("lifecycle/"+first+"/preview", 200)
	post("lifecycle/"+first+"/purge", map[string]interface{}{"confirmation": first, "token": "old"}, 409)
	post("migrations/"+plan+"/complete", map[string]interface{}{}, 409)
	migrated := post("migrations/"+plan+"/draft", map[string]interface{}{"definition_id": down}, 200)
	if !strings.Contains(string(migrated["drafts"]), second) {
		t.Fatal("migration draft did not bind new output")
	}
	post("dataflows/"+down+"/versions/1/retire", map[string]interface{}{}, 200)
	post("migrations/"+plan+"/complete", map[string]interface{}{}, 200)
	var preview struct {
		Token    string
		Blockers []string
	}
	if e = json.Unmarshal([]byte(get("lifecycle/"+first+"/preview", 200)), &preview); e != nil {
		t.Fatal(e)
	}
	if len(preview.Blockers) > 0 {
		t.Fatalf("unexpected blockers %v", preview.Blockers)
	}
	receipt := post("lifecycle/"+first+"/purge", map[string]interface{}{"confirmation": first, "token": preview.Token}, 200)
	if string(receipt["deleted_rows"]) != "1" {
		t.Fatalf("purge row count %s", receipt["deleted_rows"])
	}
	get("runs/"+first+"/records", 410)
	get("runs/"+first+"/output", 200)
	get("lineage", 200)
	get("runs/"+second+"/records", 200)
	post("lifecycle/"+first+"/purge", map[string]interface{}{"confirmation": first, "token": preview.Token}, 409)
	automatic := idOf(post("dataflows/"+fid+"/versions/1/run", map[string]interface{}{}, 201))
	await(automatic, "succeeded")
	if e = db.Model(&workflow.GovernanceRun{}).Where("id=?", automatic).Updates(map[string]interface{}{"finished_at": old, "created_at": old}).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&workflow.Record{}).Where("kind='dataset' AND id=?", automatic).Update("created_at", old).Error; e != nil {
		t.Fatal(e)
	}
	post("retention-policies", map[string]interface{}{"flow_id": fid, "days": 1, "keep_last": 1, "enabled": true, "expected_version": 1, "confirmation": "wrong"}, 400)
	post("retention-policies", map[string]interface{}{"flow_id": fid, "days": 1, "keep_last": 1, "enabled": true, "expected_version": 1, "confirmation": fid}, 200)
	var tries atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Idempotency-Key") == "" {
			t.Error("webhook missing delivery identity")
		}
		tries.Add(1)
		if tries.Load() == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	t.Setenv("API_ALLOWED_TARGETS", string(jsonBody(t, []workflow.APITarget{{Origin: receiver.URL, Addresses: []string{"127.0.0.1"}}})))
	post("alert-rules", map[string]interface{}{"name": "拒绝未批准目标", "enabled": true, "failure": true, "quality_rate": 1, "webhook": "https://unapproved.invalid/hook"}, 403)
	rule := idOf(post("alert-rules", map[string]interface{}{"name": "失败通知", "enabled": true, "failure": true, "quality_rate": 1, "webhook": receiver.URL + "/hook"}, 201))
	if e = db.Model(&workflow.ImportRow{}).Where("batch_id=?", source).Update("data", json.RawMessage(`{"id":"bad"}`)).Error; e != nil {
		t.Fatal(e)
	}
	failed := idOf(post("dataflows/"+fid+"/versions/1/run", map[string]interface{}{}, 201))
	await(failed, "failed")
	sweep := func(want string) {
		t.Helper()
		done := worker.StartGovernanceMaintenanceWorker(context.Background())
		defer done()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var rows []workflow.GovernanceAlert
			if e = db.Where("rule_id=? AND run_id=?", rule, failed).Find(&rows).Error; e != nil {
				t.Fatal(e)
			}
			if len(rows) == 1 && rows[0].State == want && (want != "pending" || rows[0].Attempts > 0 && rows[0].ErrorCode != "") {
				if want == "delivered" {
					var purged int64
					if er := db.Model(&workflow.GovernanceSnapshotPurge{}).Where("run_id=?", automatic).Count(&purged).Error; er != nil {
						t.Fatal(er)
					}
					if purged == 0 {
						time.Sleep(50 * time.Millisecond)
						continue
					}
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("alert delivery timeout")
	}
	sweep("pending")
	if e = db.Model(&workflow.GovernanceAlert{}).Where("rule_id=?", rule).Update("next_at", time.Now().Add(-time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	sweep("delivered")
	sweep("delivered")
	if tries.Load() != 2 {
		t.Fatalf("notifications not deduplicated %d", tries.Load())
	}
	get("alerts", 200)
	get("runs/"+automatic+"/records", 410)
	get("runs/"+second+"/records", 200)
	_, otherName := businessUser(t, service, "maintenance_other_", "other-dept", []string{"governor"})
	other, otherP := login(t, router, otherName)
	w := workflowRequest(router, "GET", "/api/v1/governance/lifecycle/"+second+"/preview", nil, other, "", "")
	if w.Code != 403 {
		t.Fatalf("cross department lifecycle %d", w.Code)
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/alert-rules/"+rule, jsonBody(t, map[string]interface{}{"name": "steal", "enabled": false, "quality_rate": 1, "expected_version": 1}), other, otherP.CSRFToken, "foreign-rule")
	if w.Code != 403 {
		t.Fatalf("cross owner rule %d", w.Code)
	}
}
