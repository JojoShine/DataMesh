package integration

import (
	"context"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGovernanceProfileControl(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "profile_control_", "profile-control", []string{"admin"})
	cookie, actor := login(t, router, name)
	governorID, governorName := businessUser(t, service, "profile_revoked_", "profile-control", []string{"governor"})
	governor, gov := login(t, router, governorName)
	seedAssignment(t, db, governorID, "profile-control", "govern_resource")
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	ids := []string{"small-" + uid, "large-" + uid}
	for i, id := range ids {
		rows := 7
		if i == 1 {
			rows = 50000
		}
		if err := db.Exec("INSERT INTO resource_import_rows(batch_id,row_number,data) SELECT ?,n,jsonb_build_object('id',n) FROM generate_series(1,?) n", id, rows).Error; err != nil {
			t.Fatal(err)
		}
		managed := workflow.ManagedResource{Name: id, DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields, Keys: []string{"id"}})}
		if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	serial := 0
	create := func(id string) string {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: id, Version: 1}, "fields": []string{"id"}, "mode": "full"}), cookie, actor.CSRFToken, fmt.Sprint("control-", serial))
		if w.Code != 201 {
			t.Fatalf("enqueue=%d %s", w.Code, w.Body.String())
		}
		var run workflow.GovernanceRun
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	cancelled := create(ids[0])
	expired := create(ids[0])
	live := create(ids[0])
	normal := create(ids[0])
	large := create(ids[1])
	if w := workflowRequest(router, "POST", "/api/v1/governance/runs/"+cancelled+"/cancel", []byte(`{}`), cookie, actor.CSRFToken, "cancel-queued-profile"); w.Code != 200 {
		t.Fatalf("cancel=%d %s", w.Code, w.Body.String())
	}
	if err := db.Model(&workflow.GovernanceRun{}).Where("id=?", expired).Updates(map[string]interface{}{"state": "running", "lease_owner": "dead-worker", "lease_expires_at": time.Now().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&workflow.GovernanceRun{}).Where("id=?", live).Updates(map[string]interface{}{"state": "running", "lease_owner": "live-worker", "lease_expires_at": time.Now().Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	grantID := "profile-processing-" + uid
	grant := workflow.ProcessingAuthorization{UserID: governorID, Resource: workflow.Resource{Type: "table_resource", ID: ids[0], Version: 1}, Fields: []string{"id"}, Purpose: "数据治理", ExpiresAt: time.Now().Add(time.Hour), ApprovalRef: "profile-test"}
	if err := db.Create(&workflow.Record{Kind: "processing_authorization", ID: grantID, DepartmentID: actor.DepartmentID, OwnerID: uid, ParentID: governorID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; err != nil {
		t.Fatal(err)
	}
	w := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, map[string]interface{}{"resource": grant.Resource, "fields": []string{"id"}, "mode": "full"}), governor, gov.CSRFToken, "revoked-profile")
	if w.Code != 201 {
		t.Fatalf("governor enqueue=%d %s", w.Code, w.Body.String())
	}
	var revoked workflow.GovernanceRun
	if err := json.Unmarshal(w.Body.Bytes(), &revoked); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&workflow.Record{}).Where("kind='processing_authorization' AND id=?", grantID).Update("state", "revoked").Error; err != nil {
		t.Fatal(err)
	}
	replacement, err := workflow.New(db)
	if err != nil {
		t.Fatal(err)
	}
	stop := replacement.StartGovernanceWorkers(context.Background())
	defer stop()
	wait := func(id string, want string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			var run workflow.GovernanceRun
			if err := db.Where("id=?", id).Take(&run).Error; err != nil {
				t.Fatal(err)
			}
			if run.State == want {
				return run
			}
			if run.State == "succeeded" || run.State == "failed" || run.State == "cancelled" || run.State == "interrupted" {
				t.Fatalf("run %s=%s code=%s want=%s", id, run.State, run.ErrorCode, want)
			}
		}
		t.Fatalf("run %s did not reach %s", id, want)
		return workflow.GovernanceRun{}
	}
	wait(cancelled, "cancelled")
	wait(expired, "interrupted")
	wait(large, "running")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		var extracting workflow.GovernanceRun
		if err := db.Where("id=?", large).Take(&extracting).Error; err != nil {
			t.Fatal(err)
		}
		if extracting.Processed > 0 && extracting.Processed < 50000 {
			break
		}
		if extracting.State != "running" || time.Now().After(deadline) {
			t.Fatalf("no progress during extraction: state=%s processed=%d", extracting.State, extracting.Processed)
		}
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/runs/"+large+"/cancel", []byte(`{}`), cookie, actor.CSRFToken, "cancel-running-profile")
	if w.Code != 200 {
		t.Fatalf("cancel running=%d %s", w.Code, w.Body.String())
	}
	stopped := wait(large, "cancelled")
	if string(stopped.Result) != "null" {
		t.Fatal("cancelled profile exposed partial report")
	}
	finished := wait(normal, "succeeded")
	var report workflow.ProfileReport
	if err := json.Unmarshal(finished.Result, &report); err != nil || report.Rows != 7 {
		t.Fatalf("temporary stage contaminated: %s", finished.Result)
	}
	blocked := wait(revoked.ID, "failed")
	if blocked.ErrorCode != "PROCESSING_AUTHORIZATION_REQUIRED" || string(blocked.Result) != "null" {
		t.Fatalf("revoked processing continued: %+v", blocked)
	}
	var liveRun workflow.GovernanceRun
	if err := db.Where("id=?", live).Take(&liveRun).Error; err != nil || liveRun.LeaseOwner != "live-worker" || liveRun.State != "running" {
		t.Fatal("new worker stole live lease")
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/runs/"+expired+"/retry", []byte(`{}`), cookie, actor.CSRFToken, "retry-profile-observation")
	if w.Code != 201 {
		t.Fatalf("retry=%d %s", w.Code, w.Body.String())
	}
	var retry workflow.GovernanceRun
	if err := json.Unmarshal(w.Body.Bytes(), &retry); err != nil || retry.ID == expired || retry.RetryOf == nil || *retry.RetryOf != expired {
		t.Fatal("retry reused original read snapshot")
	}
	wait(retry.ID, "succeeded")
	stop()
	var tempTables int64
	if err := db.Raw("SELECT count(*) FROM pg_tables WHERE schemaname LIKE 'pg_temp_%' AND tablename='governance_input_stage'").Scan(&tempTables).Error; err != nil || tempTables != 0 {
		t.Fatal("temporary data survived rollback")
	}
}
