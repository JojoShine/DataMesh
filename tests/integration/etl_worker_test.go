package integration

import (
	"context"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestETLWorkerRecovery(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "lease_owner_", "lease-dept", []string{"admin"})
	row := workflow.Record{Kind: "etl_run", ID: "lease-live", ParentID: "lease-task", OwnerID: uid, DepartmentID: "lease-dept", State: "loading", Version: 1, Body: jsonBody(t, workflow.ETLRun{})}
	if e := db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	ctl := workflow.ETLControl{RunID: row.ID, Active: true, TargetKey: "recovery-target", LeaseOwner: "other-live-worker", LeaseExpiresAt: time.Now().Add(time.Minute)}
	if e := db.Create(&ctl).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("INSERT INTO etl_target_locks(target_key,run_id) VALUES (?,?)", "recovery-target", row.ID).Error; e != nil {
		t.Fatal(e)
	}
	server, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	var current workflow.Record
	if e = db.Where("kind='etl_run' AND id=?", row.ID).Take(&current).Error; e != nil || current.State != "loading" {
		t.Fatal("new instance reset active lease", e, current.State)
	}
	if e = db.Model(&workflow.ETLControl{}).Where("run_id=?", row.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	stop := server.StartETLWorkers(context.Background())
	defer stop()
	recovered := false
	for i := 0; i < 50; i++ {
		var r workflow.Record
		if e = db.Where("kind='etl_run' AND id=?", row.ID).Take(&r).Error; e != nil {
			t.Fatal(e)
		}
		if r.State == "unknown" {
			var count int64
			if e := db.Table("etl_target_locks").Where("run_id=?", r.ID).Count(&count).Error; e != nil || count != 1 {
				t.Fatal("unknown released target reservation", e, count)
			}
			recovered = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("expired lease did not become unknown")
	}
	stop()
	// Enqueue with no worker, recreate the service, then drain the durable queue.
	u, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(u.Port())
	password, _ := u.User.Password()
	database := strings.TrimPrefix(u.Path, "/")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	for _, q := range []string{"CREATE TABLE public.queue_source(id integer PRIMARY KEY)", "CREATE TABLE public.queue_target(id integer PRIMARY KEY)", "INSERT INTO public.queue_source VALUES(1),(2)"} {
		if e = db.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	cookie, p := login(t, router, name)
	post := func(path string, v interface{}) json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, v), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != 201 {
			t.Fatalf("durable queue %s %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var source struct {
		ID string `json:"source_id"`
	}
	e = json.Unmarshal(post("/api/v1/sources", workflow.SourceInput{Name: "Queue source", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: uid, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}), &source)
	if e != nil {
		t.Fatal(e)
	}
	var task struct {
		ID string `json:"id"`
	}
	e = json.Unmarshal(post("/api/v1/etl-tasks", workflow.ETLDefinition{Name: "Queued across restart", Source: workflow.TableConfig{SourceID: source.ID, Schema: "public", Table: "queue_source"}, Target: workflow.TableConfig{SourceID: source.ID, Schema: "public", Table: "queue_target"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}}}), &task)
	if e != nil {
		t.Fatal(e)
	}
	var queued struct {
		ID string `json:"id"`
	}
	if e = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}), &queued); e != nil {
		t.Fatal(e)
	}
	time.Sleep(300 * time.Millisecond)
	var waiting workflow.Record
	if e = db.Where("kind='etl_run' AND id=?", queued.ID).Take(&waiting).Error; e != nil || waiting.State != "queued" {
		t.Fatal("HTTP request still dispatched work", e, waiting.State)
	}
	replacement, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	drain := replacement.StartETLWorkers(context.Background())
	defer drain()
	for i := 0; i < 100; i++ {
		var r workflow.Record
		if e = db.Where("kind='etl_run' AND id=?", queued.ID).Take(&r).Error; e != nil {
			t.Fatal(e)
		}
		if r.State == "succeeded" {
			var count int64
			if e = db.Table("queue_target").Count(&count).Error; e != nil || count != 2 {
				t.Fatal("durable output mismatch", e, count)
			}
			return
		}
		if r.State == "unknown" || r.State == "failed" {
			t.Fatal("queued work did not survive restart", r.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("replacement worker did not drain queue")
}
