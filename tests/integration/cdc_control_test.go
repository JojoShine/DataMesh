package integration

import (
	"context"
	"database/sql"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCDCControlRecovery(t *testing.T) {
	var mu sync.Mutex
	configs := map[string]map[string]string{}
	states := map[string]string{}
	writes := 0
	restarts := 0
	var gate, entered chan struct{}
	holdConfig := false
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/config/validate") {
			_, _ = w.Write([]byte(`{"error_count":0}`))
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "connectors" {
			w.WriteHeader(404)
			return
		}
		name, action := parts[1], parts[2]
		if action == "config" && r.Method == "PUT" {
			var cfg map[string]string
			if e := json.NewDecoder(r.Body).Decode(&cfg); e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			configs[name] = cfg
			states[name] = "RUNNING"
			writes++
			// The external create succeeded, but its HTTP response was lost.
			w.WriteHeader(503)
			return
		}
		cfg, exists := configs[name]
		if !exists {
			w.WriteHeader(404)
			return
		}
		if action == "config" {
			if holdConfig {
				holdConfig = false
				close(entered)
				<-gate
			}
			_ = json.NewEncoder(w).Encode(cfg)
			return
		}
		if action == "status" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"connector": map[string]string{"state": states[name]}, "tasks": []map[string]string{{"state": states[name]}}})
			return
		}
		if r.Method == "POST" && action == "restart" {
			states[name] = "RUNNING"
			restarts++
			w.WriteHeader(202)
			return
		}
		if r.Method == "PUT" && (action == "pause" || action == "resume") {
			if action == "pause" {
				states[name] = "PAUSED"
			} else {
				states[name] = "RUNNING"
			}
			w.WriteHeader(202)
			return
		}
		w.WriteHeader(404)
	}))
	defer capture.Close()
	t.Setenv("CAPTURE_ORIGIN", capture.URL)
	sourceURL, e := url.Parse(os.Getenv("CDC_TEST_SOURCE_DSN"))
	if e != nil || !strings.HasPrefix(sourceURL.Path, "/datamesh_test_") {
		t.Fatal("isolated CDC source required")
	}
	sourceDB, e := sql.Open("pgx", sourceURL.String())
	if e != nil {
		t.Fatal(e)
	}
	defer sourceDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, e = sourceDB.ExecContext(ctx, `CREATE TABLE cdc_control_probe(id bigint PRIMARY KEY,name text); CREATE PUBLICATION cdc_control_pub FOR TABLE cdc_control_probe`); e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(sourceURL.Port())
	database := strings.TrimPrefix(sourceURL.Path, "/")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": sourceURL.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t)
	// Two independent workers must reconcile the same durable control without replaying create.
	second, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stopSecond := second.StartCDCWorkers(context.Background())
	defer stopSecond()
	_, name := businessUser(t, service, "cdc_control_", "cdc-control", []string{"admin"})
	cookie, p := login(t, router, name)
	password, _ := sourceURL.User.Password()
	source := workflow.SourceInput{Name: "CDC worker source", Kind: "postgres", Host: sourceURL.Hostname(), Port: port, Database: database, OwnerID: p.UserID, Credentials: workflow.Credential{Username: sourceURL.User.Username(), Password: password}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, source), cookie, p.CSRFToken, "cdc-control-source")
	if w.Code != 201 {
		t.Fatalf("source %d", w.Code)
	}
	var src struct {
		ID string `json:"source_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &src)
	w = workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, map[string]interface{}{"source_id": src.ID, "source_version": 1, "schema": "public", "table": "cdc_control_probe", "publication": "cdc_control_pub"}), cookie, p.CSRFToken, "cdc-control-task")
	if w.Code != 201 {
		t.Fatalf("task %d %s", w.Code, w.Body.String())
	}
	var task struct {
		ID      string              `json:"task_id"`
		Capture workflow.CDCBinding `json:"capture_binding"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &task)
	waitState := func(want string) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			var record workflow.Record
			if e := db.Where("kind='task' AND id=?", task.ID).Take(&record).Error; e != nil {
				t.Fatal(e)
			}
			if record.State == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		var ctl workflow.CDCControl
		_ = db.Where("task_id=?", task.ID).Take(&ctl).Error
		var record workflow.Record
		_ = db.Where("kind='task' AND id=?", task.ID).Take(&record).Error
		mu.Lock()
		n := writes
		mu.Unlock()
		t.Fatalf("CDC did not reach %s: state=%s error=%s generation=%d/%d lease=%t writes=%d", want, record.State, ctl.LastErrorCode, ctl.ObservedGeneration, ctl.Generation, ctl.LeaseOwner != "", n)
	}
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/start", []byte(`{}`), cookie, p.CSRFToken, "cdc-control-start")
	if w.Code != 202 {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	waitState("streaming")
	mu.Lock()
	n := writes
	mu.Unlock()
	if n != 1 {
		t.Fatalf("create replayed %d times after lost response", n)
	}
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/pause", []byte(`{}`), cookie, p.CSRFToken, "cdc-control-pause")
	if w.Code != 202 {
		t.Fatalf("pause %d", w.Code)
	}
	waitState("paused")
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/resume", []byte(`{}`), cookie, p.CSRFToken, "cdc-control-resume")
	if w.Code != 202 {
		t.Fatalf("resume %d", w.Code)
	}
	waitState("streaming")
	mu.Lock()
	n = writes
	mu.Unlock()
	if n != 1 {
		t.Fatal("pause/resume replaced configuration or reset position")
	}
	mu.Lock()
	states[task.Capture.ConnectorName] = "FAILED"
	mu.Unlock()
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/reconcile", []byte(`{}`), cookie, p.CSRFToken, "cdc-control-restart")
	if w.Code != 202 {
		t.Fatalf("reconcile failed: %d", w.Code)
	}
	waitState("streaming")
	mu.Lock()
	restarted := restarts
	mu.Unlock()
	if restarted != 1 {
		t.Error("failed capture was not restarted using the failed-task control")
	}
	// Publish new archive metadata while an external control read is in flight.
	mu.Lock()
	gate = make(chan struct{})
	entered = make(chan struct{})
	holdConfig = true
	mu.Unlock()
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/reconcile", []byte(`{}`), cookie, p.CSRFToken, "cdc-archive-race")
	if w.Code != 202 {
		t.Fatal("archive-race control not accepted")
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("control was never blocked")
	}
	archiveID := "verified-control-archive-" + task.ID
	archive := workflow.ArchiveData{ManifestVersion: 2, Topic: task.Capture.Topic, Start: 0, End: 5, Count: 5, Boundary: workflow.CDCSnapshotBoundary{State: "complete", ObjectVersion: 1, Partitions: []workflow.CDCPartitionRange{{Topic: task.Capture.Topic, Partition: 0, Start: 0, End: 5}}}}
	if e = db.Create(&workflow.Record{Kind: "archive", ID: archiveID, ParentID: task.Capture.ObjectID, OwnerID: p.UserID, State: "verified", Version: 1, Body: jsonBody(t, archive)}).Error; e != nil {
		close(gate)
		t.Fatal(e)
	}
	if e = db.Exec("UPDATE business_records SET body=jsonb_set(jsonb_set(body,'{archive_id}',to_jsonb(?::text)),'{run_id}',to_jsonb('concurrent-metadata'::text)) WHERE kind='task' AND id=?", archiveID, task.ID).Error; e != nil {
		close(gate)
		t.Fatal(e)
	}
	close(gate)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var control workflow.CDCControl
		if e = db.Where("task_id=?", task.ID).Take(&control).Error; e != nil {
			t.Fatal(e)
		}
		if control.ObservedGeneration == control.Generation && control.LeaseOwner == "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var latest workflow.Record
	var latestData workflow.TaskData
	if e = db.Where("kind='task' AND id=?", task.ID).Take(&latest).Error; e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(latest.Body, &latestData); e != nil {
		t.Fatal(e)
	}
	if latestData.ArchiveID != archiveID || latestData.RunID == nil || *latestData.RunID != "concurrent-metadata" {
		t.Error("control erased concurrently published archive or other task metadata")
	}
	// Task responses expose the verified archive even when capture/snapshot stages differ.
	if e = db.Exec("UPDATE business_records SET body=jsonb_set(body,'{archive_id}',to_jsonb(?::text)) WHERE kind='task' AND id=?", archiveID, task.ID).Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "GET", "/api/v1/tasks/"+task.ID, nil, cookie, "", "")
	var view struct {
		Archive struct {
			Status   string `json:"status"`
			Position struct {
				ManifestID string `json:"manifest_id"`
			} `json:"position"`
		} `json:"archive"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || view.Archive.Status != "complete" || view.Archive.Position.ManifestID != archiveID {
		t.Errorf("verified archive hidden: HTTP %d status=%s", w.Code, view.Archive.Status)
	}
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/archive", []byte(`{}`), cookie, p.CSRFToken, "cdc-failed-archive")
	if w.Code != 202 {
		t.Fatalf("archive request HTTP %d", w.Code)
	}
	archiveDeadline := time.Now().Add(15 * time.Second)
	var archiveOperation workflow.Operation
	for time.Now().Before(archiveDeadline) {
		archiveOperation = workflow.Operation{}
		if e = db.Where("actor_id=? AND idempotency_key=?", p.UserID, "cdc-failed-archive").Take(&archiveOperation).Error; e != nil {
			t.Fatal(e)
		}
		if archiveOperation.Status == "failed" || archiveOperation.Status == "succeeded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if archiveOperation.Status != "failed" {
		t.Fatalf("incomplete snapshot archive result=%s", archiveOperation.Status)
	}
	w = workflowRequest(router, "GET", "/api/v1/tasks/"+task.ID, nil, cookie, "", "")
	if e = json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || view.Archive.Status != "complete" || view.Archive.Position.ManifestID != archiveID {
		t.Error("failed subsequent archive hid the verified range")
	}
	// This task already owns its slot: consuming the last slot must not pause it.
	slots := []string{}
	defer func() {
		for _, slot := range slots {
			if _, err := sourceDB.Exec("SELECT pg_drop_replication_slot($1)", slot); err != nil {
				t.Error("isolated slot cleanup failed")
			}
		}
	}()
	for i := 0; i < 20; i++ {
		var available bool
		if e = sourceDB.QueryRow("SELECT current_setting('max_replication_slots')::int>(SELECT count(*) FROM pg_replication_slots)").Scan(&available); e != nil {
			t.Fatal(e)
		}
		if !available {
			break
		}
		slot := task.Capture.Slot
		if i > 0 {
			slot += "_" + strconv.Itoa(i)
		}
		if _, e = sourceDB.Exec("SELECT pg_create_logical_replication_slot($1,'pgoutput')", slot); e != nil {
			t.Fatal(e)
		}
		slots = append(slots, slot)
	}
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/pause", []byte(`{}`), cookie, p.CSRFToken, "cdc-full-pause")
	if w.Code != 202 {
		t.Fatal("full capacity pause failed")
	}
	waitState("paused")
	w = workflowRequest(router, "POST", "/api/v1/tasks/"+task.ID+"/resume", []byte(`{}`), cookie, p.CSRFToken, "cdc-full-resume")
	if w.Code != 202 {
		t.Fatal("full capacity resume failed")
	}
	waitState("streaming")
	w = workflowRequest(router, "POST", "/api/v1/cdc-tasks/validate", jsonBody(t, map[string]interface{}{"source_id": src.ID, "source_version": 1, "schema": "public", "table": "cdc_control_probe", "publication": "cdc_control_pub"}), cookie, p.CSRFToken, "cdc-full-new-validation")
	var validation struct {
		Checks []struct {
			Check  string `json:"check"`
			Passed bool   `json:"passed"`
		} `json:"checks"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &validation); e != nil {
		t.Fatal(e)
	}
	blocked := false
	for _, check := range validation.Checks {
		if check.Check == "replication_capacity" && !check.Passed {
			blocked = true
		}
	}
	if !blocked {
		t.Error("unrelated new task accepted exhausted slot capacity")
	}
	mu.Lock()
	configs[task.Capture.ConnectorName]["snapshot.mode"] = "never"
	mu.Unlock()
	// Removing trust must pause a previously verified owned connector even if its config drifted.

	t.Setenv("SOURCE_ALLOWED_TARGETS", "[]")
	waitState("paused")
	mu.Lock()
	state := states[task.Capture.ConnectorName]
	mu.Unlock()
	if state != "PAUSED" {
		t.Fatal("untrusted capture still running")
	}
}
