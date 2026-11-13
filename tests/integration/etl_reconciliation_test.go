package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestDeliveryReconciliationRetention(t *testing.T) { testETLReconciliation(t, "retention") }
func TestETLReconciliationBeforeCommit(t *testing.T)   { testETLReconciliation(t, "trigger") }
func TestETLReconciliationDeferred(t *testing.T)       { testETLReconciliation(t, "deferred") }
func TestETLReconciliationAfterCommit(t *testing.T)    { testETLReconciliation(t, "normal") }
func TestETLUUIDFinalKeys(t *testing.T)                { testETLReconciliation(t, "uuid_keys") }
func TestETLExpectedFinalKeys(t *testing.T)            { testETLReconciliation(t, "keys") }
func testETLReconciliation(t *testing.T, mode string) {
	keyMode := mode == "keys" || mode == "uuid_keys"
	u, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(u.Port())
	database := strings.TrimPrefix(u.Path, "/")
	password, _ := u.User.Password()
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "recon_owner_", "reconciliation", []string{"admin"})
	_, otherName := businessUser(t, service, "recon_other_", "reconciliation", []string{"operator"})
	cookie, p := login(t, router, name)
	other, _ := login(t, router, otherName)
	schema := "recon_" + strings.ToLower(strings.ReplaceAll(uid, "-", ""))
	statements := []string{"CREATE SCHEMA " + schema, "CREATE TABLE " + schema + ".source(id bigint PRIMARY KEY,name text,amount numeric(20,6),stamp timestamp(3))", "CREATE TABLE " + schema + ".target(id bigint PRIMARY KEY,name text,amount numeric(20,6),stamp timestamp(3),unmapped integer DEFAULT 7)", "INSERT INTO " + schema + ".source SELECT 9007199254740993+i,' name-'||i||' ',i::numeric/1000000,timestamp '2026-10-21 12:00:00.123' FROM generate_series(1,501) i", "INSERT INTO " + schema + ".target(id,name) VALUES(1,'historical')"}
	if keyMode {
		statements = []string{"CREATE SCHEMA " + schema, "CREATE TABLE " + schema + ".source(id varchar(5) PRIMARY KEY,name text)", "CREATE TABLE " + schema + ".target(id char(5) PRIMARY KEY,name text)", "INSERT INTO " + schema + ".source VALUES('a','first'),('a ','last')"}
	}
	if mode == "uuid_keys" {
		statements = []string{"CREATE SCHEMA " + schema, "CREATE TABLE " + schema + ".source(id varchar(36) PRIMARY KEY,name text)", "CREATE TABLE " + schema + ".target(id uuid PRIMARY KEY,name text)", "INSERT INTO " + schema + ".source VALUES('AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA','first'),('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','last')"}
	}
	if mode == "trigger" {
		statements = append(statements, "CREATE FUNCTION "+schema+".rewrite_value() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=9007199254741494 THEN NEW.name='trigger-changed';END IF;RETURN NEW;END $$", "CREATE TRIGGER rewrite_value BEFORE INSERT OR UPDATE ON "+schema+".target FOR EACH ROW EXECUTE FUNCTION "+schema+".rewrite_value()")
	}
	if mode == "deferred" {
		statements = append(statements, "CREATE FUNCTION "+schema+".deferred_value() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=9007199254741494 THEN UPDATE "+schema+".target SET name='deferred-changed' WHERE id=NEW.id;END IF;RETURN NULL;END $$", "CREATE CONSTRAINT TRIGGER deferred_value AFTER INSERT ON "+schema+".target DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION "+schema+".deferred_value()")
	}
	for _, statement := range statements {
		if e = db.Exec(statement).Error; e != nil {
			t.Fatal(e)
		}
	}
	post := func(path string, value interface{}, status int) json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, value), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if e = json.Unmarshal(post("/api/v1/sources", workflow.SourceInput{Name: "reconciliation target", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: uid, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}, 201), &source); e != nil {
		t.Fatal(e)
	}
	mapping := []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}
	if !keyMode {
		mapping = append(mapping, workflow.ETLMapping{Source: "amount", Target: "amount", Rule: "copy"}, workflow.ETLMapping{Source: "stamp", Target: "stamp", Rule: "copy"})
	}
	definition := workflow.ETLDefinition{Name: "reconciled exchange", Source: workflow.TableConfig{SourceID: source.ID, Schema: schema, Table: "source"}, Target: workflow.TableConfig{SourceID: source.ID, Schema: schema, Table: "target"}, Mapping: mapping}
	var task, run struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(post("/api/v1/etl-tasks", definition, 201), &task)
	_ = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, 201), &run)
	var record workflow.Record
	var data workflow.ETLRun
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if e = db.Where("kind='etl_run' AND id=?", run.ID).Take(&record).Error; e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(record.Body, &data); e != nil {
			t.Fatal(e)
		}
		if record.State == "succeeded" || record.State == "failed" || record.State == "unknown" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mode == "trigger" || mode == "deferred" {
		var count int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&count).Error; e != nil {
			t.Fatal(e)
		}
		if record.State != "failed" || data.Written != 0 || count != 1 || data.ErrorCode != "RECONCILIATION_MISMATCH" {
			t.Fatalf("trigger mutation committed: state=%s written=%d rows=%d code=%s", record.State, data.Written, count, data.ErrorCode)
		}
		return
	}
	wanted := 501
	if keyMode {
		wanted = 2
	}
	if record.State != "succeeded" || data.Written != wanted {
		t.Fatalf("exchange %s written=%d code=%s", record.State, data.Written, data.ErrorCode)
	}
	if mode == "normal" {
		var metrics struct {
			Timings *struct {
				Total  int64 `json:"total_ms"`
				Before int64 `json:"before_check_ms"`
			} `json:"timings"`
		}
		if e = json.Unmarshal(record.Body, &metrics); e != nil {
			t.Fatal(e)
		}
		if metrics.Timings == nil || metrics.Timings.Total <= 0 {
			t.Fatal("execution stage timings were not recorded")
		}
	}
	path := "/api/v1/etl-runs/" + run.ID + "/reconciliation"
	type report struct {
		ID       string `json:"id"`
		Version  int    `json:"version"`
		State    string `json:"state"`
		KeyCount int64  `json:"key_count"`
		Checked  int64  `json:"checked_rows"`
		Mismatch int64  `json:"mismatch_count"`
	}
	read := func(version int, want string) report {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var out struct {
			Latest *report `json:"latest"`
		}
		for time.Now().Before(deadline) {
			w := workflowRequest(router, "GET", path, nil, cookie, "", "")
			if w.Code != 200 {
				t.Fatalf("report %d %s", w.Code, w.Body.String())
			}
			if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
				t.Fatal(e)
			}
			if out.Latest != nil && out.Latest.Version >= version && out.Latest.State == want {
				return *out.Latest
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("report did not reach %s: %+v", want, out.Latest)
		return report{}
	}
	initial := read(1, "passed")
	if keyMode {
		if initial.KeyCount != 1 || initial.Checked != 1 {
			t.Fatalf("equivalent database keys counted separately: %+v", initial)
		}
		var value string
		if e = db.Raw("SELECT name FROM " + schema + ".target").Scan(&value).Error; e != nil || value != "last" {
			t.Fatal("upsert final value differs")
		}
		return
	}
	if initial.KeyCount != 501 || initial.Checked != 501 || initial.Mismatch != 0 {
		t.Fatalf("scope counted historical/unmapped fields: %+v", initial)
	}
	if w := workflowRequest(router, "GET", path, nil, other, "", ""); w.Code != 403 {
		t.Fatalf("other user accessed report: %d", w.Code)
	}
	if mode == "retention" {
		if e = db.Exec("UPDATE etl_delivery_inputs SET expires_at=now()-interval '1 day' WHERE run_id=?", run.ID).Error; e != nil {
			t.Fatal(e)
		}
		if e = db.Model(&workflow.Record{}).Where("kind='etl_run' AND id=?", run.ID).Update("state", "unknown").Error; e != nil {
			t.Fatal(e)
		}
		server, err := workflow.New(db)
		if err != nil {
			t.Fatal(err)
		}
		stop := server.StartResourceRetentionWorker(context.Background())
		time.Sleep(200 * time.Millisecond)
		stop()
		var state string
		var wrapped int
		if e = db.Raw("SELECT state,octet_length(wrapped_key) FROM etl_delivery_inputs WHERE run_id=?", run.ID).Row().Scan(&state, &wrapped); e != nil || state != "ready" || wrapped == 0 {
			t.Fatal("unknown delivery lost protected evidence")
		}
		if e = db.Model(&workflow.Record{}).Where("kind='etl_run' AND id=?", run.ID).Update("state", "succeeded").Error; e != nil {
			t.Fatal(e)
		}
		stop = server.StartResourceRetentionWorker(context.Background())
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if e = db.Raw("SELECT state,octet_length(wrapped_key) FROM etl_delivery_inputs WHERE run_id=?", run.ID).Row().Scan(&state, &wrapped); e != nil {
				t.Fatal(e)
			}
			if state == "expired" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		stop()
		if state != "expired" || wrapped != 0 {
			t.Fatal("expired delivery key was retained")
		}
		var keys, reports int64
		if e = db.Table("etl_delivery_keys").Where("run_id=?", run.ID).Count(&keys).Error; e != nil || keys != 0 {
			t.Fatal("expired encrypted key index retained")
		}
		if e = db.Model(&workflow.ETLReconciliation{}).Where("run_id=? AND state='passed'", run.ID).Count(&reports).Error; e != nil || reports != 1 {
			t.Fatal("report metadata was removed")
		}
		post(path, struct{}{}, 409)
		w := workflowRequest(router, "GET", path+"?download=1", nil, cookie, "", "")
		if w.Code != 200 || w.Header().Get("Content-Disposition") == "" {
			t.Fatalf("safe report export %d", w.Code)
		}
		for _, secret := range []string{"encrypted_key", "wrapped_key", "expected_digest", "9007199254740994", password} {
			if secret != "" && strings.Contains(w.Body.String(), secret) {
				t.Fatal("export contains confidential index content")
			}
		}
		return
	}
	if e = db.Exec("DROP TABLE " + schema + ".source;UPDATE " + schema + ".target SET name='external-change' WHERE id=9007199254740994").Error; e != nil {
		t.Fatal(e)
	}
	post(path, struct{}{}, 202)
	later := read(2, "mismatched")
	if later.Mismatch != 1 {
		t.Fatalf("external change not found: %+v", later)
	}
	if e = db.Where("kind='etl_run' AND id=?", run.ID).Take(&record).Error; e != nil || record.State != "succeeded" {
		t.Fatal("reconciliation changed execution result")
	}
	var value string
	if e = db.Raw("SELECT name FROM " + schema + ".target WHERE id=9007199254740994").Scan(&value).Error; e != nil || value != "external-change" {
		t.Fatal("reconciliation rewrote the target")
	}
}
