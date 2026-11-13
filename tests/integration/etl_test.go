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

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
)

func TestETLRealExchange(t *testing.T) {
	t.Setenv("ETL_MAX_ROWS", "10000")
	parsed, err := url.Parse(os.Getenv("TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	t.Setenv("CREDENTIAL_KEY", strings.Repeat("01", 32))
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": parsed.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "etl_admin_", "etl-dept", []string{"admin"})
	otherID, otherName := businessUser(t, service, "etl_other_", "etl-dept", []string{"operator"})
	cookie, principal := login(t, router, ownerName)
	other, otherPrincipal := login(t, router, otherName)
	schema := "etl_" + strings.ToLower(strings.ReplaceAll(ownerID, "-", ""))
	if err = db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE " + schema + ".source (id integer PRIMARY KEY, name text)", "CREATE TABLE " + schema + ".target (id integer PRIMARY KEY, name text CHECK(name <> 'FAIL'))", "INSERT INTO " + schema + ".source VALUES (1,'  Alice  '),(2,' Bob ')"} {
		if err = db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	post := func(path string, value interface{}, key string, status int) json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, value), cookie, principal.CSRFToken, key)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	password, _ := parsed.User.Password()
	var source struct {
		ID string `json:"source_id"`
	}
	if err = json.Unmarshal(post("/api/v1/sources", workflow.SourceInput{Name: "ETL isolated source", Kind: "postgres", Host: parsed.Hostname(), Port: port, Database: database, OwnerID: ownerID, Credentials: workflow.Credential{Username: parsed.User.Username(), Password: password}}, "etl-source", 201), &source); err != nil {
		t.Fatal(err)
	}
	def := workflow.ETLDefinition{Name: "客户转换", Source: workflow.TableConfig{SourceID: source.ID, Schema: schema, Table: "source"}, Target: workflow.TableConfig{SourceID: source.ID, Schema: schema, Table: "target"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}}
	var task struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(post("/api/v1/etl-tasks", def, "etl-create", 201), &task); err != nil {
		t.Fatal(err)
	}
	if w := workflowRequest(router, "POST", "/api/v1/etl-tasks/"+task.ID+"/run", []byte("{}"), other, otherPrincipal.CSRFToken, "other-run"); w.Code != 403 {
		t.Fatalf("other operator ran task: %d", w.Code)
	}
	type runView struct {
		ID    string          `json:"id"`
		State string          `json:"state"`
		Data  workflow.ETLRun `json:"data"`
	}
	execute := func(key string) runView {
		t.Helper()
		path := "/api/v1/etl-tasks/" + task.ID + "/run"
		var accepted runView
		if err = json.Unmarshal(post(path, struct{}{}, key, 201), &accepted); err != nil {
			t.Fatal(err)
		}
		var replay runView
		if err = json.Unmarshal(post(path, struct{}{}, key, 201), &replay); err != nil || accepted.ID != replay.ID {
			t.Fatal("idempotent request created duplicate run")
		}
		for i := 0; i < 100; i++ {
			w := workflowRequest(router, "GET", "/api/v1/etl-tasks/"+task.ID+"/runs", nil, cookie, "", "")
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			var page struct {
				Items []runView `json:"items"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Items {
				if r.ID == accepted.ID && (r.State == "succeeded" || r.State == "failed" || r.State == "unknown") {
					return r
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("run did not finish")
		return runView{}
	}
	run := execute("etl-first")
	if run.State != "succeeded" || run.Data.Written != 2 {
		t.Fatalf("run failed %+v", run)
	}
	var names []string
	if err = db.Raw("SELECT name FROM " + schema + ".target ORDER BY id").Scan(&names).Error; err != nil || strings.Join(names, ",") != "Alice,Bob" {
		t.Fatal("transformation mismatch", names)
	}
	// Fail the second loading event, after writes began, to verify metadata errors roll back data.
	for _, q := range []string{
		"CREATE SEQUENCE " + schema + ".event_attempt",
		"CREATE FUNCTION " + schema + ".fail_progress() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.code='RUN_LOADING' AND NEW.task_id='" + strings.ReplaceAll(task.ID, "'", "''") + "' AND nextval('" + schema + ".event_attempt')=2 THEN RAISE EXCEPTION 'injected progress failure'; END IF; RETURN NEW; END $$",
		"CREATE TRIGGER fail_progress BEFORE INSERT ON etl_events FOR EACH ROW EXECUTE FUNCTION " + schema + ".fail_progress()",
		"UPDATE " + schema + ".source SET name='ShouldRollback' WHERE id=1",
	} {
		if err = db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	metadataFailure := execute("ops-progress-failure")
	if metadataFailure.State != "failed" || metadataFailure.Data.Written != 0 {
		t.Fatalf("metadata failure stranded execution: %+v", metadataFailure)
	}
	var unchanged string
	if err = db.Raw("SELECT name FROM " + schema + ".target WHERE id=1").Scan(&unchanged).Error; err != nil || unchanged != "Alice" {
		t.Fatal("metadata failure committed target writes", unchanged, err)
	}
	if err = db.Exec("DROP TRIGGER fail_progress ON etl_events").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE " + schema + ".source SET name='  Alice  ' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}

	// Task configuration changes create drafts; historical runs retain their definition.
	var copied struct {
		ID      string                 `json:"id"`
		State   string                 `json:"state"`
		Version int                    `json:"version"`
		Data    workflow.ETLDefinition `json:"data"`
	}
	if err = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/copy", map[string]interface{}{"expected_version": 1}, "ops-copy", 201), &copied); err != nil || copied.State != "draft" || !strings.Contains(copied.Data.Name, "副本") {
		t.Fatal("copy not a named draft", err, copied)
	}
	revision := map[string]interface{}{"name": "新版本", "source": def.Source, "target": def.Target, "mapping": def.Mapping, "expected_version": 1}
	post("/api/v1/etl-tasks/"+copied.ID+"/revisions", revision, "ops-revise", 200)
	post("/api/v1/etl-tasks/"+copied.ID+"/run", struct{}{}, "ops-draft-block", 409)
	post("/api/v1/etl-tasks/"+copied.ID+"/enable", map[string]interface{}{"expected_version": 2}, "ops-enable", 200)
	// Cancellation while a target statement is blocked must roll back the entire transaction.
	if err = db.Exec("CREATE FUNCTION " + schema + ".slow_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(3); RETURN NEW; END $$").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("CREATE TRIGGER slow_write BEFORE INSERT OR UPDATE ON " + schema + ".target FOR EACH ROW EXECUTE FUNCTION " + schema + ".slow_write()").Error; err != nil {
		t.Fatal(err)
	}
	var cancelling runView
	if err = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, "ops-cancel-start", 201), &cancelling); err != nil {
		t.Fatal(err)
	}
	loading := false
	for i := 0; i < 100; i++ {
		var current workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", cancelling.ID).Take(&current).Error; e != nil {
			t.Fatal(e)
		}
		if current.State == "loading" {
			loading = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !loading {
		t.Fatal("run never reached target transaction")
	}
	if err = db.Model(&workflow.ETLControl{}).Where("run_id=?", cancelling.ID).Update("stage_started_at", time.Now().Add(-2*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	var noProgress int64
	for i := 0; i < 40; i++ {
		if err = db.Model(&workflow.ETLAlert{}).Where("run_id=? AND code='NO_PROGRESS'", cancelling.ID).Count(&noProgress).Error; err != nil {
			t.Fatal(err)
		}
		if noProgress == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if noProgress != 1 {
		t.Fatal("blocked target did not produce no-progress alert")
	}
	// A second task targeting the same table must remain queued, even with a fresh worker instance.
	var pending runView
	if e := json.Unmarshal(post("/api/v1/etl-tasks/"+copied.ID+"/run", struct{}{}, "same-target-queued", 201), &pending); e != nil {
		t.Fatal(e)
	}
	second, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stopSecond := second.StartETLWorkers(context.Background())
	time.Sleep(300 * time.Millisecond)
	var queued workflow.Record
	if e = db.Where("kind='etl_run' AND id=?", pending.ID).Take(&queued).Error; e != nil || queued.State != "queued" {
		t.Fatal("same target executed concurrently", e, queued.State)
	}
	post("/api/v1/etl-runs/"+pending.ID+"/cancel", map[string]interface{}{"reason": "取消等待目标的任务"}, "same-target-cancel", 200)
	stopSecond()
	post("/api/v1/etl-runs/"+cancelling.ID+"/cancel", map[string]interface{}{"reason": "取消阻塞写入"}, "ops-cancel", 200)
	cancelled := false
	for i := 0; i < 120; i++ {
		var current workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", cancelling.ID).Take(&current).Error; e != nil {
			t.Fatal(e)
		}
		if current.State == "cancelled" {
			cancelled = true
			break
		}
		if current.State == "succeeded" {
			t.Fatal("cancelled run committed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !cancelled {
		var r workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", cancelling.ID).Take(&r).Error; e != nil {
			t.Fatal(e)
		}
		var data workflow.ETLRun
		_ = json.Unmarshal(r.Body, &data)
		t.Fatalf("cancellation did not terminate: state=%s code=%s message=%s", r.State, data.ErrorCode, data.Message)
	}
	if err = db.Exec("DROP TRIGGER slow_write ON " + schema + ".target").Error; err != nil {
		t.Fatal(err)
	}
	var cancellationEvents int64
	if err = db.Table("etl_events").Where("run_id=? AND code='RUN_CANCELLED'", cancelling.ID).Count(&cancellationEvents).Error; err != nil || cancellationEvents != 1 {
		t.Fatal("cancellation event missing", err)
	}
	// Force a live worker to lose its lease before Commit; it must not publish its target writes.
	if err = db.Exec("CREATE TRIGGER slow_write BEFORE INSERT OR UPDATE ON " + schema + ".target FOR EACH ROW EXECUTE FUNCTION " + schema + ".slow_write()").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE " + schema + ".source SET name='LeaseMustRollback' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	var lost runView
	if err = json.Unmarshal(post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, "lease-loss-start", 201), &lost); err != nil {
		t.Fatal(err)
	}
	reached := false
	for i := 0; i < 100; i++ {
		var r workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", lost.ID).Take(&r).Error; e != nil {
			t.Fatal(e)
		}
		if r.State == "loading" {
			reached = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !reached {
		t.Fatal("lease loss never reached target phase")
	}
	if err = db.Model(&workflow.ETLControl{}).Where("run_id=?", lost.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	unknown := false
	for i := 0; i < 100; i++ {
		var r workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", lost.ID).Take(&r).Error; e != nil {
			t.Fatal(e)
		}
		if r.State == "unknown" {
			unknown = true
			break
		}
		if r.State == "succeeded" {
			t.Fatal("expired worker committed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !unknown {
		t.Fatal("lost lease not isolated")
	}
	var reserved int64
	if err = db.Table("etl_target_locks").Where("run_id=?", lost.ID).Count(&reserved).Error; err != nil || reserved != 1 {
		t.Fatal("unknown target not reserved", reserved, err)
	}
	resolved := false
	for i := 0; i < 60; i++ {
		w := workflowRequest(router, "POST", "/api/v1/etl-runs/"+lost.ID+"/resolve", jsonBody(t, map[string]interface{}{"outcome": "failed", "reason": "确认旧执行器未提交", "evidence": "目标事务锁已释放，原有记录未变化"}), cookie, principal.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code == 200 {
			resolved = true
			break
		}
		if w.Code != 409 {
			t.Fatalf("lease resolution %d %s", w.Code, w.Body.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !resolved {
		t.Fatal("expired transaction never became safe to reconcile")
	}
	var preserved string
	if err = db.Raw("SELECT name FROM " + schema + ".target WHERE id=1").Scan(&preserved).Error; err != nil || preserved != "Alice" {
		t.Fatal("lost lease changed target", preserved, err)
	}
	if err = db.Exec("DROP TRIGGER slow_write ON " + schema + ".target").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE " + schema + ".source SET name='  Alice  ' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	// Both data layers use the resource delivery path, not a client-supplied physical table.
	seedAssignment(t, db, otherID, "etl-dept", "operate_resource")
	originalTaskID := task.ID
	wSchema := workflowRequest(router, "GET", "/api/v1/sources/"+source.ID+"/table-schema?schema="+schema+"&table=source", nil, cookie, "", "")
	if wSchema.Code != 200 {
		t.Fatal(wSchema.Body.String())
	}
	var cfg workflow.TableConfig
	if err = json.Unmarshal(wSchema.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.SourceID = source.ID
	cfg.Schema = schema
	cfg.Table = "source"
	for _, layer := range []string{"raw_object", "governed_dataset"} {
		ref := workflow.Resource{Type: "table_resource", ID: "etl-resource-" + layer + originalTaskID, Version: 1}
		managed := workflow.ManagedResource{Name: "ETL resource", DataLayer: layer, Adapter: "database", Fields: cfg.Fields, Config: jsonBody(t, cfg)}
		if err = db.Create(&workflow.Record{Kind: ref.Type, ID: ref.ID, OwnerID: ownerID, DepartmentID: "etl-dept", State: "ready", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
			t.Fatal(err)
		}
		snapshot := seedSharingFixture(t, db, ownerID, "etl-dept", ref, cfg.Fields)
		if err = db.Exec("UPDATE business_records SET body=jsonb_set(body,'{definition,data_layer}',to_jsonb(?::text)) WHERE kind='directory_revision' AND id=?", layer, snapshot.DirectoryRevisionID).Error; err != nil {
			t.Fatal(err)
		}
		resourceDef := def
		resourceDef.SourceResource = &ref
		resourceDef.Source = workflow.TableConfig{}
		if err = json.Unmarshal(post("/api/v1/etl-tasks", resourceDef, "resource-create-"+layer, 201), &task); err != nil {
			t.Fatal(err)
		}
		if run := execute("resource-run-" + layer); run.State != "succeeded" || run.Data.Written != 2 {
			t.Fatalf("resource exchange failed: %+v", run)
		}
		same := resourceDef
		same.Target = cfg
		post("/api/v1/etl-tasks", same, "resource-self-"+layer, 400)
		// Even operators with target access cannot consume an ungranted resource.

		denied := workflowRequest(router, "POST", "/api/v1/etl-tasks", jsonBody(t, resourceDef), other, otherPrincipal.CSRFToken, "resource-denied-"+layer)
		if denied.Code != 403 {
			t.Fatalf("ungranted resource accepted: %d %s", denied.Code, denied.Body.String())
		}
		if err = db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "suspended").Error; err != nil {
			t.Fatal(err)
		}
		post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, "resource-suspended-"+layer, 409)
	}
	task.ID = originalTaskID
	if err = db.Exec("UPDATE " + schema + ".source SET name='  Updated  ' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if execute("etl-second").State != "succeeded" {
		t.Fatal("upsert failed")
	}
	var count int64
	db.Table(schema + ".target").Count(&count)
	if count != 2 {
		t.Fatal("rerun duplicated rows")
	}
	if err = db.Exec("INSERT INTO " + schema + ".source VALUES (3,'FAIL')").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE " + schema + ".source SET name='MustRollback' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	run = execute("etl-rollback")
	if run.State != "failed" || run.Data.Written != 0 {
		t.Fatal("failed transaction reported success")
	}

	// A constraint failure in the second SQL batch must also roll back the first batch.
	if err = db.Exec("UPDATE " + schema + ".source SET name='OK' WHERE id=3").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("INSERT INTO " + schema + ".source SELECT i, CASE WHEN i=601 THEN 'FAIL' ELSE 'BatchValue' END FROM generate_series(4,601) i").Error; err != nil {
		t.Fatal(err)
	}
	crossBatch := execute("cross-batch-rollback")
	if crossBatch.State != "failed" || crossBatch.Data.Written != 0 || len(crossBatch.Data.Batches) != 1 {
		t.Fatalf("cross batch failure not atomic: %+v", crossBatch)
	}
	var targetCount int64
	if err = db.Table(schema + ".target").Count(&targetCount).Error; err != nil || targetCount != 2 {
		t.Fatal("first batch escaped rollback", targetCount, err)
	}
	if err = db.Exec("DELETE FROM " + schema + ".source WHERE id>=4").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE " + schema + ".source SET name='FAIL' WHERE id=3").Error; err != nil {
		t.Fatal(err)
	}
	var retried runView
	retryPath := "/api/v1/etl-runs/" + run.ID + "/retry"
	if err = json.Unmarshal(post(retryPath, map[string]interface{}{"reason": "验证安全重试"}, "ops-retry", 201), &retried); err != nil || retried.Data.RetryOf != run.ID || retried.Data.Attempt != 2 {
		t.Fatal("retry ancestry missing", err, retried)
	}
	var replayRetry runView
	if err = json.Unmarshal(post(retryPath, map[string]interface{}{"reason": "验证安全重试"}, "ops-retry", 201), &replayRetry); err != nil || replayRetry.ID != retried.ID {
		t.Fatal("retry not idempotent")
	}
	retryFinished := false
	for i := 0; i < 100; i++ {
		var current workflow.Record
		if e := db.Where("kind='etl_run' AND id=?", retried.ID).Take(&current).Error; e != nil {
			t.Fatal(e)
		}
		if current.State == "failed" {
			retryFinished = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !retryFinished {
		t.Fatal("retry never finished")
	}
	var alert workflow.ETLAlert
	if err = db.Where("run_id=?", retried.ID).Take(&alert).Error; err != nil {
		t.Fatal("failure alert missing", err)
	}
	post("/api/v1/etl-alerts/"+alert.ID+"/acknowledge", map[string]interface{}{"reason": "正在核对目标约束"}, "ops-alert-ack", 200)
	post("/api/v1/etl-alerts/"+alert.ID+"/resolve", map[string]interface{}{"reason": "隔离验证已完成"}, "ops-alert-close", 200)
	wEvents := workflowRequest(router, "GET", "/api/v1/etl-runs/"+retried.ID+"/events?download=1", nil, cookie, "", "")
	if wEvents.Code != 200 || !strings.Contains(wEvents.Body.String(), "RUN_FAILED") || strings.Contains(wEvents.Body.String(), password) {
		t.Fatalf("unsafe or missing event export: %d", wEvents.Code)
	}
	for _, note := range []string{"验证安全重试", "正在核对目标约束", "隔离验证已完成"} {
		if !strings.Contains(wEvents.Body.String(), note) {
			t.Fatalf("handling history missing: %s", note)
		}
	}
	var name string
	if err = db.Raw("SELECT name FROM " + schema + ".target WHERE id=1").Scan(&name).Error; err != nil || name != "Updated" {
		t.Fatal("partial transaction escaped rollback", name)
	}
	// Ordinary operators need processing permission and an explicit derived-data grant.

	if err = db.Create(&identity.UserRole{UserID: otherID, Role: "governor"}).Error; err != nil {
		t.Fatal(err)
	}
	objectID := "etl-object-" + task.ID
	object := workflow.Record{Kind: "object", ID: objectID, ParentID: source.ID, OwnerID: ownerID, DepartmentID: "etl-dept", State: "available", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: objectID, SourceID: source.ID, QualifiedName: schema + ".source", SchemaVersion: 1})}
	if err = db.Create(&object).Error; err != nil {
		t.Fatal(err)
	}
	createOther := func(key string, want int) {
		t.Helper()
		w := workflowRequest(router, "POST", "/api/v1/etl-tasks", jsonBody(t, def), other, otherPrincipal.CSRFToken, key)
		if w.Code != want {
			t.Fatalf("processing check %s got %d: %s", key, w.Code, w.Body.String())
		}
	}
	createOther("missing-grant", 403)
	grant := workflow.Record{Kind: "processing_authorization", ID: "etl-grant-" + task.ID, OwnerID: ownerID, DepartmentID: "etl-dept", State: "active", Version: 1}
	grantData := workflow.ProcessingAuthorization{Resource: workflow.Resource{Type: "raw_object", ID: objectID, Version: 1}, UserID: otherID, Fields: []string{"id", "name"}, Purpose: "数据治理", ExpiresAt: time.Now().Add(time.Hour)}
	grant.Body = jsonBody(t, grantData)
	if err = db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	createOther("no-derived", 403)
	grantData.AllowDerived = true
	grant.Body = jsonBody(t, grantData)
	if err = db.Save(&grant).Error; err != nil {
		t.Fatal(err)
	}
	createOther("derived-approved", 201)
	var external workflow.Record
	if err = db.Where("kind='source' AND id=?", source.ID).Take(&external).Error; err != nil {
		t.Fatal(err)
	}
	external.ID = "etl-external-" + task.ID
	external.DepartmentID = "external-dept"
	if err = db.Create(&external).Error; err != nil {
		t.Fatal(err)
	}
	externalScope := workflow.ResourceAssignment{UserID: otherID, DepartmentID: "external-dept", ResourceID: external.ID, Capability: "operate_resource", Reason: "隔离跨部门权限测试", ExpiresAt: time.Now().Add(time.Hour)}
	if err = db.Create(&workflow.Record{Kind: "assignment", ID: "external-scope-" + task.ID, DepartmentID: "external-dept", OwnerID: otherID, State: "active", Version: 1, Body: jsonBody(t, externalScope)}).Error; err != nil {
		t.Fatal(err)
	}
	def.Target.SourceID = external.ID
	createOther("no-cross-department-export", 403)
	def.Target.SourceID = source.ID

	if err = db.Exec("INSERT INTO " + schema + ".source SELECT i, 'bounded' FROM generate_series(4,10001) i").Error; err != nil {
		t.Fatal(err)
	}
	run = execute("etl-limit")
	if run.State != "failed" || run.Data.Written != 0 {
		t.Fatal("oversized input was written")
	}

	def.Target = def.Source
	post("/api/v1/etl-tasks", def, "etl-same-table", 400)
	// A stalled/unknown run must block another submission rather than duplicate writes.
	record := workflow.Record{Kind: "etl_run", ID: "stalled-" + task.ID, ParentID: task.ID, OwnerID: ownerID, DepartmentID: "etl-dept", State: "unknown", Version: 1, Body: jsonBody(t, workflow.ETLRun{Definition: def})}
	if err = db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	post("/api/v1/etl-tasks/"+task.ID+"/run", struct{}{}, "etl-blocked", 409)
	t.Log(fmt.Sprintf("verified two committed runs and transactional rollback for %s", task.ID))
}
