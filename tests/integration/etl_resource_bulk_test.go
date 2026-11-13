package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"gorm.io/gorm"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestDataSecurityLegacyETLAdminForcedMask(t *testing.T) {
	testETLResourceBulk(t, "security_admin_legacy_mask")
}
func TestDataSecurityRowFilterPolicyCaptureRace(t *testing.T) {
	testETLResourceBulk(t, "security_row_filter_race")
}
func TestDataSecurityRowFilteredDeliveryAndETL(t *testing.T) {
	testETLResourceBulk(t, "security_row_filter")
}
func TestDataSecurityETLProof(t *testing.T) { testETLResourceBulk(t, "security_proof") }
func TestDataSecurityETLUnavailableKey(t *testing.T) {
	testETLResourceBulk(t, "security_key_unavailable")
}
func TestETLResourcePolicyChange(t *testing.T)      { testETLResourceBulk(t, "policy_change") }
func TestETLLegacyMaskedKey(t *testing.T)           { testETLResourceBulk(t, "legacy_masked_key") }
func TestETLResourceCapabilities(t *testing.T)      { testETLResourceBulk(t, "capabilities") }
func TestDeliveryReceiptAuthorization(t *testing.T) { testETLResourceBulk(t, "receipts") }
func TestGovernedCheckRecovery(t *testing.T)        { testETLResourceBulk(t, "governed_recovery") }
func TestGovernedBulkBinding(t *testing.T)          { testETLResourceBulk(t, "governed_snapshot") }
func TestGovernedBulkWrongBinding(t *testing.T)     { testETLResourceBulk(t, "governed_bad_binding") }
func TestGovernedBulkQuality(t *testing.T)          { testETLResourceBulk(t, "governed_bad_late") }
func TestETLResourceRawCorrupt(t *testing.T)        { testETLResourceBulk(t, "raw_corrupt") }
func TestETLResourceRawKeyMismatch(t *testing.T)    { testETLResourceBulk(t, "raw_key_mismatch") }
func TestETLResourceRawSnapshot(t *testing.T)       { testETLResourceBulk(t, "raw_snapshot") }
func TestETLPostgresStaleTargetStats(t *testing.T)  { testETLResourceBulk(t, "plan_stale_stats") }
func TestETLPostgresGrowingTargetPlan(t *testing.T) { testETLResourceBulk(t, "plan_growth") }
func TestETLResourceBulkTable(t *testing.T)         { testETLResourceBulk(t, "normal") }
func TestETLResourceBulkRevocation(t *testing.T)    { testETLResourceBulk(t, "revocation") }
func TestETLResourceRoleChange(t *testing.T)        { testETLResourceBulk(t, "role_change") }
func TestETLResourceBoundaries(t *testing.T) {
	for _, mode := range []string{"row_limit", "byte_limit", "wide_row", "timeout", "source_interrupt"} {
		t.Run(mode, func(t *testing.T) { testETLResourceBulk(t, mode) })
	}
}
func testETLResourceBulk(t *testing.T, mode string, benchmark ...*resourceBenchmark) {
	if strings.HasPrefix(mode, "security_") {
		t.Setenv("SECURITY_WATERMARK_KEY_ID", "etl-test")
		t.Setenv("SECURITY_WATERMARK_KEYS", `{"etl-test":"`+strings.Repeat("ab", 32)+`"}`)
	}
	rows := 10001
	if strings.HasPrefix(mode, "security_") {
		rows = 51
	}
	if mode == "row_limit" {
		rows = 2000001
	}
	if mode == "source_interrupt" {
		rows = 1000000
	}
	if mode == "wide_row" {
		rows = 1
	}
	if mode == "plan_stale_stats" {
		rows = 20000
	}
	if mode == "plan_growth" {
		rows = 500000
	}
	if mode == "legacy_masked_key" {
		rows = 2
	}
	if len(benchmark) > 0 {
		rows = benchmark[0].Rows
	}
	rawMode := strings.HasPrefix(mode, "raw_") || strings.HasPrefix(mode, "governed_")
	t.Setenv("ETL_MAX_ROWS", "20000")
	if len(benchmark) > 0 || mode == "plan_growth" || mode == "row_limit" || mode == "source_interrupt" {
		t.Setenv("ETL_MAX_ROWS", "2000000")
	}
	if mode == "byte_limit" {
		t.Setenv("ETL_MAX_BYTES", "20000")
	}
	if mode == "timeout" {
		t.Setenv("ETL_TIMEOUT_SECONDS", "5")
	}
	u, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(u.Port())
	database := strings.TrimPrefix(u.Path, "/")
	password, _ := u.User.Password()
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t, mode != "governed_recovery" && mode != "security_row_filter_race")
	ownerID, ownerName := businessUser(t, service, "resource_provider_", "provider", []string{"admin"})
	roles := []string{"operator"}
	if mode == "role_change" || mode == "security_admin_legacy_mask" {
		roles = append(roles, "admin")
	}
	userID, userName := businessUser(t, service, "resource_consumer_", "consumer", roles)
	seedAssignment(t, db, userID, "consumer", "operate_resource")
	owner, op := login(t, router, ownerName)
	consumer, cp := login(t, router, userName)
	schema := "bulk_" + strings.ToLower(strings.ReplaceAll(userID, "-", ""))
	if e = db.Exec("CREATE SCHEMA " + schema + "; CREATE TABLE " + schema + ".source(id bigint PRIMARY KEY,name text,secret text); CREATE TABLE " + schema + ".target(id bigint PRIMARY KEY,name text); INSERT INTO " + schema + ".source SELECT i,'sensitive-'||i,'not-granted' FROM generate_series(1," + strconv.Itoa(rows) + ") i").Error; e != nil {
		t.Fatal(e)
	}
	if mode == "wide_row" {
		if e = db.Exec("UPDATE " + schema + ".source SET name=repeat('x',4194305)").Error; e != nil {
			t.Fatal(e)
		}
	}
	if mode == "plan_growth" {
		// Bound actual scan work rather than asserting a wall-clock threshold or a particular setting.
		sql := "CREATE FUNCTION " + schema + ".check_scan_budget() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE scanned bigint; BEGIN SELECT seq_tup_read INTO scanned FROM pg_stat_xact_user_tables WHERE relid='" + schema + ".target'::regclass; IF scanned>150000000 THEN RAISE EXCEPTION 'ETL_PLAN_SCAN_BUDGET_EXCEEDED'; END IF;RETURN NULL; END $$;CREATE CONSTRAINT TRIGGER check_scan_budget AFTER INSERT ON " + schema + ".target DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.id=500000) EXECUTE FUNCTION " + schema + ".check_scan_budget()"
		if e = db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	if mode == "plan_stale_stats" {
		sql := "CREATE FUNCTION " + schema + ".check_lookup_budget() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE scanned bigint; BEGIN SELECT seq_tup_read+idx_tup_fetch INTO scanned FROM pg_stat_xact_user_tables WHERE relid='" + schema + ".target'::regclass; IF scanned>120000 THEN RAISE EXCEPTION 'ETL_LOOKUP_SCAN_BUDGET_EXCEEDED';END IF;RETURN NULL;END $$;CREATE CONSTRAINT TRIGGER check_lookup_budget AFTER INSERT ON " + schema + ".target DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.id=20000) EXECUTE FUNCTION " + schema + ".check_lookup_budget()"
		if e = db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	sourceInput := workflow.SourceInput{Name: "bulk resource source", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: ownerID, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, sourceInput), owner, op.CSRFToken, "bulk-provider-source")
	if w.Code != 201 {
		t.Fatalf("provider %d", w.Code)
	}
	var src struct {
		ID string `json:"source_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &src)
	sourceInput.Name = "bulk recipient target"
	sourceInput.OwnerID = userID
	w = workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, sourceInput), consumer, cp.CSRFToken, "bulk-consumer-target")
	if w.Code != 201 {
		t.Fatalf("target %d %s", w.Code, w.Body.String())
	}
	var dst struct {
		ID string `json:"source_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &dst)
	w = workflowRequest(router, "GET", "/api/v1/sources/"+src.ID+"/table-schema?schema="+schema+"&table=source", nil, owner, "", "")
	if w.Code != 200 {
		t.Fatalf("schema %d", w.Code)
	}
	var cfg workflow.TableConfig
	if e = json.Unmarshal(w.Body.Bytes(), &cfg); e != nil {
		t.Fatal(e)
	}
	cfg.SourceID = src.ID
	fields := append([]workflow.Field{}, cfg.Fields...)
	for i := range fields {
		if fields[i].Name == "name" {
			fields[i].Sensitive = mode != "policy_change" && mode != "wide_row" && mode != "security_admin_legacy_mask"
		}
	}
	ref := workflow.Resource{Type: "table_resource", ID: "bulk-resource-" + userID, Version: 1}
	managed := workflow.ManagedResource{Name: "authorized bulk source", DataLayer: "raw_object", Adapter: "database", Fields: fields, Config: jsonBody(t, cfg)}
	if mode == "legacy_masked_key" {
		fields[0].LogicalType = "string"
		fields[0].Sensitive = true
		managed.Fields = fields
		managed.Adapter = "excel"
		managed.Config = jsonBody(t, workflow.TableConfig{BatchID: ref.ID, Fields: fields, Keys: []string{"id"}})
		if e = db.Exec("ALTER TABLE " + schema + ".target ALTER COLUMN id TYPE text").Error; e != nil {
			t.Fatal(e)
		}
		for i, id := range []string{"private-key-a", "private-key-b"} {
			if e = db.Create(&workflow.ImportRow{BatchID: ref.ID, RowNumber: i + 1, Data: jsonBody(t, map[string]interface{}{"id": id, "name": "sensitive-name", "secret": "not-granted"})}).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	if mode == "security_admin_legacy_mask" {
		managed.Adapter = "excel"
		managed.Config = jsonBody(t, workflow.TableConfig{BatchID: ref.ID, Fields: fields, Keys: []string{"id"}})
		for i := 1; i <= rows; i++ {
			if e = db.Create(&workflow.ImportRow{BatchID: ref.ID, RowNumber: i, Data: jsonBody(t, map[string]interface{}{"id": i, "name": "private-admin-value", "secret": "not-granted"})}).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = db.Create(&workflow.Record{Kind: "table_resource", ID: ref.ID, OwnerID: ownerID, DepartmentID: "provider", State: "ready", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	if rawMode {
		ref = workflow.Resource{Type: "raw_object", ID: "bulk-raw-" + userID, Version: 1}
		obj := workflow.Object{ObjectID: ref.ID, SourceID: src.ID, QualifiedName: schema + ".source", SchemaVersion: 1, Fields: fields}
		if e = db.Create(&workflow.Record{Kind: "object", ID: ref.ID, OwnerID: ownerID, DepartmentID: "provider", State: "ready", Version: 1, Body: jsonBody(t, obj)}).Error; e != nil {
			t.Fatal(e)
		}
		meta := seedBulkRawArchive(t, ref.ID, mode, rows)
		archiveID := "bulk-archive-" + userID
		taskDigest := sha256.Sum256([]byte("bulk-capture-" + userID))
		taskID := hex.EncodeToString(taskDigest[:16])
		if e = db.Create(&workflow.Record{Kind: "archive", ID: archiveID, OwnerID: ownerID, DepartmentID: "provider", ParentID: ref.ID, State: "verified", Version: 1, Body: jsonBody(t, meta)}).Error; e != nil {
			t.Fatal(e)
		}
		taskData := workflow.TaskData{TaskInput: workflow.TaskInput{SourceID: src.ID, ObjectID: ref.ID, SchemaVersion: 1}, Binding: "postgres", ArchiveID: archiveID, Capture: &workflow.CDCBinding{TaskID: taskID, ConnectorName: "datamesh-cdc-" + taskID, Topic: meta.Topic, TopicPrefix: "fixture"}}
		if e = db.Create(&workflow.Record{Kind: "task", ID: taskID, OwnerID: ownerID, DepartmentID: "provider", State: "draft", Version: 1, Body: jsonBody(t, taskData)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	if strings.HasPrefix(mode, "governed_") {
		if e = db.Exec("CREATE TABLE " + schema + ".result(id bigint PRIMARY KEY,name text);INSERT INTO " + schema + ".result SELECT id,name FROM " + schema + ".source").Error; e != nil {
			t.Fatal(e)
		}
		if e = db.Exec("DELETE FROM " + schema + ".result WHERE id=2;INSERT INTO " + schema + ".result VALUES(" + strconv.Itoa(rows+1) + ",'sensitive-" + strconv.Itoa(rows+1) + "')").Error; e != nil {
			t.Fatal(e)
		}
		if mode == "governed_bad_late" {
			if e = db.Exec("UPDATE " + schema + ".result SET name=NULL WHERE id=10001").Error; e != nil {
				t.Fatal(e)
			}
		}
		resultTable := "result"
		if mode == "governed_bad_binding" {
			if e = db.Exec("CREATE TABLE " + schema + ".result_wrong(LIKE " + schema + ".result INCLUDING ALL);INSERT INTO " + schema + ".result_wrong SELECT * FROM " + schema + ".result;UPDATE " + schema + ".result_wrong SET name='different-materialization' WHERE id=10001").Error; e != nil {
				t.Fatal(e)
			}
			resultTable = "result_wrong"
		}
		rawID := ref.ID
		ruleID := "bulk-rule-" + userID
		rule := workflow.RuleData{Definition: workflow.RuleInput{Name: "bulk-quality", ObjectID: rawID, SchemaVersion: 1, Checks: []workflow.Check{{Field: "name", Kind: "required", Blocking: true, Parameters: map[string]json.RawMessage{}}}}}
		if e = db.Create(&workflow.Record{Kind: "rule", ID: ruleID, OwnerID: ownerID, DepartmentID: "provider", ParentID: rawID, State: "published", Version: 1, Body: jsonBody(t, rule)}).Error; e != nil {
			t.Fatal(e)
		}
		definition := workflow.DatasetInput{Name: "bulk governed dataset", Description: "verified materialization", SourceObjectID: rawID, OwnerID: ownerID, Fields: fields[:2], UsageRestrictions: "authorized exchange"}
		datasetID := "bulk-dataset-" + userID
		dataset := workflow.DatasetData{Definition: definition, RuleID: ruleID, RuleVersion: 1, QualityStatus: "not_checked", CompletenessStatus: "pending", Quality: json.RawMessage("null"), LatestWatermark: json.RawMessage("null")}
		if e = db.Create(&workflow.Record{Kind: "dataset", ID: datasetID, OwnerID: ownerID, DepartmentID: "provider", ParentID: rawID, State: "draft", Version: 1, Body: jsonBody(t, dataset)}).Error; e != nil {
			t.Fatal(e)
		}
		input := map[string]interface{}{"source_id": src.ID, "source_version": 1, "schema": schema, "table": resultTable, "input_archive_id": "bulk-archive-" + userID, "contract_version": 1, "rule_version": 1, "mapping": []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}}
		w = workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/result-binding", jsonBody(t, input), consumer, cp.CSRFToken, "governed-binding-denied")
		if w.Code != 403 {
			t.Fatalf("consumer changed result binding: %d", w.Code)
		}
		input["rule_version"] = 2
		w = workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/result-binding", jsonBody(t, input), owner, op.CSRFToken, "governed-stale-rule")
		if w.Code != 409 {
			t.Fatalf("stale rule binding accepted: %d", w.Code)
		}
		input["rule_version"] = 1
		input["input_archive_id"] = "wrong-archive"
		w = workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/result-binding", jsonBody(t, input), owner, op.CSRFToken, "governed-stale-input")
		if w.Code != 409 {
			t.Fatalf("mismatched input accepted: %d", w.Code)
		}
		input["input_archive_id"] = "bulk-archive-" + userID
		checkStarted := time.Now()
		w = workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/result-binding", jsonBody(t, input), owner, op.CSRFToken, "governed-binding")
		if w.Code != 202 {
			t.Fatalf("result binding %d %s", w.Code, w.Body.String())
		}
		if mode == "governed_recovery" {
			var job workflow.GovernedCheck
			if e = db.Where("dataset_id=?", datasetID).Take(&job).Error; e != nil {
				t.Fatal(e)
			}
			if e = db.Exec("CREATE FUNCTION " + schema + ".block_check() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.snapshot_id='" + job.ResultSnapshotID + "' AND NEW.source_offset=501 THEN PERFORM pg_sleep(10);END IF;RETURN NEW;END $$;CREATE TRIGGER block_check BEFORE INSERT ON resource_snapshot_rows FOR EACH ROW EXECUTE FUNCTION " + schema + ".block_check()").Error; e != nil {
				t.Fatal(e)
			}
			server, err := workflow.New(db)
			if err != nil {
				t.Fatal(err)
			}
			stop := server.StartGovernedWorkers(context.Background())
			started := false
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if e = db.Where("id=?", job.ID).Take(&job).Error; e != nil {
					t.Fatal(e)
				}
				if job.Processed >= 500 {
					started = true
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			stop()
			if !started {
				t.Fatal("check did not enter result staging before shutdown")
			}
			if e = db.Where("id=?", job.ID).Take(&job).Error; e != nil {
				t.Fatal(e)
			}
			if job.State != "queued" {
				t.Fatalf("graceful shutdown discarded recoverable check: %s", job.State)
			}
			if e = db.Exec("DROP TRIGGER block_check ON resource_snapshot_rows").Error; e != nil {
				t.Fatal(e)
			}
			resumed, err := workflow.New(db)
			if err != nil {
				t.Fatal(err)
			}
			resumeStop := resumed.StartGovernedWorkers(context.Background())
			defer resumeStop()
			etlStop := resumed.StartETLWorkers(context.Background())
			defer etlStop()
		}
		checked := false
		checkTimeout := 45 * time.Second
		if len(benchmark) > 0 {
			checkTimeout = 20 * time.Minute
		}
		deadline := time.Now().Add(checkTimeout)
		for time.Now().Before(deadline) {
			var r workflow.Record
			if e = db.Where("kind='dataset' AND id=?", datasetID).Take(&r).Error; e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(r.Body, &dataset); e != nil {
				t.Fatal(e)
			}
			if dataset.QualityStatus == "passed" || dataset.QualityStatus == "blocked" {
				checked = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !checked {
			t.Fatal("durable governed result check did not finish")
		}
		if len(benchmark) > 0 {
			benchmark[0].GovernedValidationMS = time.Since(checkStarted).Milliseconds()
		}
		w = workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/publish", []byte(`{}`), owner, op.CSRFToken, "governed-publish")
		if strings.HasPrefix(mode, "governed_bad") {
			if dataset.QualityStatus != "blocked" || w.Code != 409 {
				t.Fatalf("late invalid governed row published: quality=%s status=%d", dataset.QualityStatus, w.Code)
			}
			return
		}
		if dataset.QualityStatus != "passed" || w.Code != 200 {
			t.Fatalf("verified result publication: quality=%s status=%d body=%s", dataset.QualityStatus, w.Code, w.Body.String())
		}
		ref = workflow.Resource{Type: "governed_dataset", ID: datasetID, Version: 1}
		fields = definition.Fields
	}
	snapshot := seedSharingFixture(t, db, ownerID, "provider", ref, fields)
	layer := "raw_object"
	if ref.Type == "governed_dataset" {
		layer = "governed_dataset"
	}
	if e = db.Exec("UPDATE business_records SET body=jsonb_set(body,'{definition,data_layer}',?::jsonb) WHERE kind='directory_revision' AND id=?", string(jsonBody(t, layer)), snapshot.DirectoryRevisionID).Error; e != nil {
		t.Fatal(e)
	}
	grantID := "bulk-grant-" + userID
	grant := workflow.GrantData{Snapshot: &snapshot, ApplicationID: "bulk-app-" + userID, GranteeUserID: userID, Scope: workflow.GrantRequest{Resource: ref, DepartmentID: "consumer", Purpose: "批量复制", Fields: []string{"id", "name"}, ExpiresAt: time.Now().Add(time.Hour)}, ApprovedBy: ownerID, ApprovedAt: time.Now()}
	if e = db.Create(&workflow.Record{Kind: "grant", ID: grantID, OwnerID: userID, DepartmentID: "consumer", ParentID: grant.ApplicationID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; e != nil {
		t.Fatal(e)
	}
	if mode == "capabilities" {
		w = workflowRequest(router, "GET", "/api/v1/etl-capabilities?resource_type="+ref.Type+"&resource_id="+ref.ID+"&contract_version=1", nil, consumer, "", "")
		if w.Code != 200 {
			t.Fatalf("resource capability %d %s", w.Code, w.Body.String())
		}
		var capability struct {
			Resource struct {
				MaxRows int    `json:"max_rows"`
				Adapter string `json:"adapter"`
			} `json:"platform_resource"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &capability); e != nil {
			t.Fatal(e)
		}
		if capability.Resource.MaxRows != 20000 || capability.Resource.Adapter != "database_postgres" {
			t.Fatalf("actual resource capacity missing: %+v", capability)
		}
	}
	if strings.HasPrefix(mode, "security_") {
		control := workflow.SecurityControl{ID: "etl-security-" + userID, Kind: "policy", OwnerID: ownerID, DepartmentID: "provider", Generation: 3, LatestVersion: 1, PublishedVersion: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		version := workflow.SecurityControlVersion{ControlID: control.ID, Version: 1, State: "published", CreatedBy: ownerID, ReviewerID: userID, Definition: jsonBody(t, workflow.SecurityDefinition{Name: "ETL签名", Resource: &ref, Actions: []string{"etl"}, RequireProof: true}), CreatedAt: time.Now()}
		if mode == "security_admin_legacy_mask" {
			version.Definition = jsonBody(t, workflow.SecurityDefinition{Name: "forced admin mask", Resource: &ref, Actions: []string{"etl"}, MaskFields: []string{"name"}})
		}
		if strings.HasPrefix(mode, "security_row_filter") {
			version.Definition = jsonBody(t, workflow.SecurityDefinition{Name: "行过滤", Resource: &ref, Actions: []string{"query", "etl"}, RowFilters: []workflow.SecurityRowFilter{{Field: "id", Operator: "gte", Value: json.RawMessage(`26`)}, {Field: "name", Operator: "ne", Value: json.RawMessage(`"sensitive-27"`)}}})
		}
		if e = db.Create(&control).Error; e != nil {
			t.Fatal(e)
		}
		if e = db.Create(&version).Error; e != nil {
			t.Fatal(e)
		}
		if mode == "security_key_unavailable" {
			t.Setenv("SECURITY_WATERMARK_KEYS", "{}")
		}
	}
	if mode == "security_row_filter" {
		preview := workflowRequest(router, "GET", "/api/v1/table-resources/"+ref.ID+"/preview", nil, owner, "", "")
		if preview.Code != 403 || !strings.Contains(preview.Body.String(), "SECURITY_CONTROLLED_PREVIEW_REQUIRED") {
			t.Fatalf("preview bypassed filter: %d %s", preview.Code, preview.Body)
		}
		unsupported := managed
		unsupported.Adapter = "excel"
		if e = db.Model(&workflow.Record{}).Where("kind='table_resource' AND id=?", ref.ID).Update("body", jsonBody(t, unsupported)).Error; e != nil {
			t.Fatal(e)
		}
		blocked := workflowRequest(router, "GET", "/delivery/v1/tables/"+ref.ID+"/records?contract_version=1", nil, consumer, "", "")
		if blocked.Code != 403 || !strings.Contains(blocked.Body.String(), "SECURITY_ROW_FILTER_UNSUPPORTED") {
			t.Fatalf("unsupported adapter bypassed filter: %d %s", blocked.Code, blocked.Body)
		}
		if e = db.Model(&workflow.Record{}).Where("kind='table_resource' AND id=?", ref.ID).Update("body", jsonBody(t, managed)).Error; e != nil {
			t.Fatal(e)
		}
		rows = 25
		path := "/delivery/v1/tables/" + ref.ID + "/records?contract_version=1&limit=2&fields=id,name"
		first := workflowRequest(router, "GET", path, nil, consumer, "", "")
		if first.Code != 200 {
			t.Fatalf("filtered page: %d %s", first.Code, first.Body)
		}
		var page struct {
			Records []map[string]string `json:"records"`
			Next    *string             `json:"next_cursor"`
		}
		if e = json.Unmarshal(first.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if len(page.Records) != 2 || page.Records[0]["id"] != "26" || page.Records[1]["id"] != "28" || page.Records[0]["name"] != "***" || page.Next == nil {
			t.Fatalf("filter must precede masking and pagination: %s", first.Body)
		}
		second := workflowRequest(router, "GET", path+"&cursor="+url.QueryEscape(*page.Next), nil, consumer, "", "")
		if second.Code != 200 {
			t.Fatalf("second filtered page: %d %s", second.Code, second.Body)
		}
		if e = json.Unmarshal(second.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if len(page.Records) != 2 || page.Records[0]["id"] != "29" || page.Records[1]["id"] != "30" {
			t.Fatalf("unstable filtered pagination: %s", second.Body)
		}
	}
	definition := workflow.ETLDefinition{Name: "authorized bulk", SourceResource: &ref, Target: workflow.TableConfig{SourceID: dst.ID, Schema: schema, Table: "target"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "copy"}}}
	w = workflowRequest(router, "POST", "/api/v1/etl-tasks", jsonBody(t, definition), consumer, cp.CSRFToken, "bulk-task")
	if w.Code != 201 {
		t.Fatalf("task %d %s", w.Code, w.Body.String())
	}
	var task struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &task)
	var filterPolicySwitched atomic.Bool
	if mode == "security_row_filter_race" {
		// Publish a stronger policy after the reader's first policy SELECT has
		// acquired its snapshot. A split decision/basis read would then accept
		// basis B while executing the stale predicates from A.
		callbackName := "test_security_row_filter_capture"
		if err := db.Callback().Row().After("gorm:row").Register(callbackName, func(tx *gorm.DB) {
			sql := tx.Statement.SQL.String()
			if !strings.Contains(sql, "security_control_versions") || !strings.Contains(sql, "h.id") || !strings.Contains(sql, "v.definition") {
				return
			}
			pcs := make([]uintptr, 32)
			count := runtime.Callers(2, pcs)
			frames := runtime.CallersFrames(pcs[:count])
			inReader := false
			for {
				frame, more := frames.Next()
				if strings.Contains(frame.Function, ".etlResourceReader") {
					inReader = true
					break
				}
				if !more {
					break
				}
			}
			if !inReader || !filterPolicySwitched.CompareAndSwap(false, true) {
				return
			}
			next := workflow.SecurityDefinition{Name: "stronger row filter", Resource: &ref, Actions: []string{"query", "etl"}, RowFilters: []workflow.SecurityRowFilter{{Field: "id", Operator: "gte", Value: json.RawMessage(`40`)}}}
			if err := db.Model(&workflow.SecurityControlVersion{}).Where("control_id=? AND version=1", "etl-security-"+userID).Update("definition", jsonBody(t, next)).Error; err != nil {
				tx.AddError(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Callback().Row().Remove(callbackName) })
		runner, err := workflow.New(db)
		if err != nil {
			t.Fatal(err)
		}
		stop := runner.StartETLWorkers(context.Background())
		t.Cleanup(stop)
	}
	var release func()
	if mode == "revocation" || mode == "role_change" || mode == "policy_change" || mode == "plan_stale_stats" || mode == "timeout" || mode == "source_interrupt" {
		if e = db.Exec("CREATE FUNCTION " + schema + ".block_second_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=501 THEN PERFORM pg_advisory_xact_lock(27191564); END IF; RETURN NEW; END $$; CREATE TRIGGER block_bulk BEFORE INSERT ON " + schema + ".target FOR EACH ROW EXECUTE FUNCTION " + schema + ".block_second_batch()").Error; e != nil {
			t.Fatal(e)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		block, err := sqlDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer block.Rollback()
		if _, err = block.Exec("SELECT pg_advisory_xact_lock(27191564)"); err != nil {
			t.Fatal(err)
		}
		release = func() { _ = block.Rollback() }
	}

	started := time.Now()
	var memory *benchmarkMemory
	if len(benchmark) > 0 {
		memory = startBenchmarkMemory()
		t.Cleanup(func() { memory.finish() })
	}
	w = workflowRequest(router, "POST", "/api/v1/etl-tasks/"+task.ID+"/run", []byte(`{}`), consumer, cp.CSRFToken, "bulk-run")
	if w.Code != 201 {
		t.Fatalf("run %d %s", w.Code, w.Body.String())
	}
	var run struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	if mode == "revocation" || mode == "role_change" || mode == "policy_change" || mode == "plan_stale_stats" || mode == "timeout" || mode == "source_interrupt" {
		blocked := false
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			var waiting int64
			err := db.Raw("SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid=27191564 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())").Scan(&waiting).Error
			if err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				blocked = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !blocked {
			t.Fatal("test never reached a pending second target batch")
		}
		if mode == "revocation" {
			if e = db.Model(&workflow.Record{}).Where("kind='grant' AND id=?", grantID).Update("state", "revoked").Error; e != nil {
				t.Fatal(e)
			}
		}
		if mode == "role_change" {
			if e = db.Exec("DELETE FROM user_roles WHERE user_id=? AND role='admin'", userID).Error; e != nil {
				t.Fatal(e)
			}
		}
		if mode == "policy_change" {
			var revision, mountRevision workflow.Record
			if e = db.Where("kind='directory_revision' AND id=?", snapshot.DirectoryRevisionID).Take(&revision).Error; e != nil {
				t.Fatal(e)
			}
			if e = db.Where("kind='mount_revision' AND id=?", snapshot.MountRevisionID).Take(&mountRevision).Error; e != nil {
				t.Fatal(e)
			}
			var dir workflow.DirectoryRevisionData
			var mount workflow.MountRevisionData
			if e = json.Unmarshal(revision.Body, &dir); e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(mountRevision.Body, &mount); e != nil {
				t.Fatal(e)
			}
			for i := range dir.Definition.Items {
				if dir.Definition.Items[i].Name == "name" {
					dir.Definition.Items[i].Sensitive = true
				}
			}
			revision.ID += "-stronger"
			revision.Version++
			revision.Body = jsonBody(t, dir)
			mountRevision.ID += "-stronger"
			mountRevision.Version++
			mount.DirectoryRevisionID = revision.ID
			mountRevision.Body = jsonBody(t, mount)
			if e = db.Create(&revision).Error; e != nil {
				t.Fatal(e)
			}
			if e = db.Create(&mountRevision).Error; e != nil {
				t.Fatal(e)
			}
			if e = db.Exec("UPDATE business_records SET body=jsonb_set(body,'{published_id}',to_jsonb(?::text)) WHERE kind='directory' AND id=?", revision.ID, snapshot.DirectoryID).Error; e != nil {
				t.Fatal(e)
			}
			if e = db.Exec("UPDATE business_records SET body=jsonb_set(body,'{active_id}',to_jsonb(?::text)) WHERE kind='mount' AND id=?", mountRevision.ID, snapshot.MountID).Error; e != nil {
				t.Fatal(e)
			}
			next := snapshot
			next.DirectoryRevisionID = revision.ID
			next.MountRevisionID = mountRevision.ID
			grant.Snapshot = &next
			grant.ApplicationID += "-stronger"
			if e = db.Model(&workflow.Record{}).Where("kind='grant' AND id=?", grantID).Update("state", "revoked").Error; e != nil {
				t.Fatal(e)
			}
			if e = db.Create(&workflow.Record{Kind: "grant", ID: grantID + "-stronger", OwnerID: userID, DepartmentID: "consumer", ParentID: grant.ApplicationID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; e != nil {
				t.Fatal(e)
			}
		}
		if mode == "plan_stale_stats" {
			if e = db.Exec("ANALYZE " + schema + ".target").Error; e != nil {
				t.Fatal(e)
			}
		}
		if mode == "source_interrupt" {
			var killed int64
			pattern := "%FROM \"" + schema + "\".\"source\" ORDER BY%"
			if e = db.Raw("SELECT count(*) FILTER (WHERE pg_terminate_backend(pid)) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND query LIKE ?", pattern).Scan(&killed).Error; e != nil || killed != 1 {
				t.Fatalf("did not interrupt exactly one test source connection: killed=%d err=%v", killed, e)
			}
		}
		if mode != "timeout" {
			release()
		}
	}

	var record workflow.Record
	var data workflow.ETLRun
	runTimeout := 40 * time.Second
	if mode == "plan_growth" {
		runTimeout = 5 * time.Minute
	}
	if len(benchmark) > 0 {
		runTimeout = 20 * time.Minute
	}
	deadline := time.Now().Add(runTimeout)
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
		time.Sleep(100 * time.Millisecond)
	}
	if mode == "security_row_filter_race" {
		var committed int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&committed).Error; e != nil {
			t.Fatal(e)
		}
		if !filterPolicySwitched.Load() || record.State != "failed" || data.ErrorCode != "ETL_AUTHORIZATION_CHANGED" || committed != 0 || data.Written != 0 {
			t.Fatalf("policy capture race leaked rows: switched=%v state=%s code=%s committed=%d written=%d", filterPolicySwitched.Load(), record.State, data.ErrorCode, committed, data.Written)
		}
		return
	}
	if mode == "security_key_unavailable" {
		var committed int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&committed).Error; e != nil {
			t.Fatal(e)
		}
		if record.State != "failed" || committed != 0 {
			t.Fatalf("missing signing key committed data: %s rows=%d", record.State, committed)
		}
		return
	}
	if mode == "row_limit" || mode == "byte_limit" || mode == "wide_row" || mode == "timeout" || mode == "source_interrupt" {
		expected := "ETL_LIMIT"
		if mode == "timeout" {
			expected = "RUN_TIMEOUT"
		}
		if mode == "source_interrupt" {
			expected = "RUN_FAILED"
		}
		var committed int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&committed).Error; e != nil {
			t.Fatal(e)
		}
		if record.State != "failed" || data.ErrorCode != expected || data.Written != 0 || committed != 0 {
			t.Fatalf("boundary did not roll back: state=%s code=%s written=%d committed=%d", record.State, data.ErrorCode, data.Written, committed)
		}
		return
	}
	if mode == "revocation" || mode == "role_change" || mode == "policy_change" {
		if mode == "policy_change" && data.ErrorCode != "ETL_AUTHORIZATION_CHANGED" && data.ErrorCode != "SHARING_VERSION_CHANGED" {
			t.Fatalf("policy change failed for unrelated reason: %s", data.ErrorCode)
		}
		if record.State != "failed" || data.Written != 0 {
			t.Fatalf("authority changed during delivery: state=%s written=%d", record.State, data.Written)
		}
		var rows int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&rows).Error; e != nil || rows != 0 {
			t.Fatal("authority change left committed rows")
		}
		return
	}

	if mode == "legacy_masked_key" {
		var count int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&count).Error; e != nil {
			t.Fatal(e)
		}
		if record.State != "failed" || data.ErrorCode != "ETL_MASKED_KEY" || count != 0 {
			t.Fatalf("legacy masked keys delivered: state=%s code=%s rows=%d", record.State, data.ErrorCode, count)
		}
		return
	}
	if mode == "raw_corrupt" || mode == "raw_key_mismatch" {
		var ready, rows int64
		if e = db.Table("resource_snapshots").Where("object_id=? AND state='ready'", ref.ID).Count(&ready).Error; e != nil {
			t.Fatal(e)
		}
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target").Scan(&rows).Error; e != nil {
			t.Fatal(e)
		}
		if record.State != "failed" || data.Written != 0 || rows != 0 || ready != 0 {
			t.Fatalf("invalid archive delivered: %s written=%d ready=%d rows=%d", record.State, data.Written, ready, rows)
		}
		return
	}
	if record.State != "succeeded" || data.Written != rows {
		t.Fatalf("authorized bulk did not deliver 10001 rows: %s written=%d code=%s message=%s", record.State, data.Written, data.ErrorCode, data.Message)
	}
	if mode == "security_row_filter" {
		var leaked int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target WHERE id<26 OR id=27").Scan(&leaked).Error; e != nil || leaked != 0 || data.Planned != 25 || data.Read != 25 {
			t.Fatalf("ETL count/read/filter mismatch: leaked=%d planned=%d read=%d err=%v", leaked, data.Planned, data.Read, e)
		}
	}
	if mode == "security_proof" {
		var proof workflow.SecurityDeliveryProof
		if e = db.Where("run_id=?", run.ID).Take(&proof).Error; e != nil {
			t.Fatal(e)
		}
		if proof.State != "completed" || proof.Action != "etl" || proof.SHA256 == "" || !strings.Contains(string(proof.Manifest), "target_table") {
			t.Fatal("ETL proof did not bind manifest")
		}
		w = workflowRequest(router, "POST", "/api/v1/security/delivery-proofs/"+proof.ID+"/verify", jsonBody(t, map[string]string{"sha256": proof.SHA256}), consumer, cp.CSRFToken, "verify-etl-proof")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"matched"`) {
			t.Fatalf("ETL proof invalid: %d %s", w.Code, w.Body.String())
		}
	}
	var count, mismatch int64
	if e = db.Raw("SELECT count(*),count(*) FILTER (WHERE name<>'***') FROM "+schema+".target").Row().Scan(&count, &mismatch); e != nil || count != int64(rows) || mismatch != 0 {
		t.Fatalf("masking count=%d mismatch=%d", count, mismatch)
	}
	if mode == "security_admin_legacy_mask" {
		return
	}
	if mode == "raw_snapshot" && len(benchmark) == 0 {
		w = workflowRequest(router, "GET", "/delivery/v1/raw/"+ref.ID+"/records?contract_version=1&limit=100&fields=name", nil, consumer, "", "")
		if w.Code != 200 {
			t.Fatalf("raw event page: %d %s", w.Code, w.Body.String())
		}
		var page struct {
			Events []struct {
				Key   map[string]json.RawMessage `json:"key"`
				After map[string]string          `json:"after"`
			} `json:"events"`
			Next *string `json:"next_cursor"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if len(page.Events) != 100 || page.Next == nil {
			t.Fatal("raw event pagination unavailable")
		}
		for _, event := range page.Events {
			if len(event.Key) != 0 || event.After["name"] != "***" {
				t.Fatal("raw event projection leaked unauthorized key or sensitive value")
			}
		}
		var snapshots int64
		if e = db.Table("resource_snapshots").Where("object_id=? AND state='ready' AND row_count=?", ref.ID, rows).Count(&snapshots).Error; e != nil || snapshots != 1 {
			t.Fatalf("ready snapshot count=%d err=%v", snapshots, e)
		}
		var forbidden int64
		if e = db.Raw("SELECT count(*) FROM " + schema + ".target WHERE id=2 OR id=" + strconv.Itoa(rows+1)).Scan(&forbidden).Error; e != nil || forbidden != 1 {
			t.Fatal("delete/insert events were not reconstructed")
		}
		var plaintext int64
		if e = db.Raw("SELECT count(*) FROM resource_snapshot_rows WHERE position(convert_to('sensitive-','UTF8') in encrypted_body)>0").Scan(&plaintext).Error; e != nil || plaintext != 0 {
			t.Fatal("snapshot body contains plaintext")
		}
	}
	if len(benchmark) > 0 {
		benchmark[0].collect(t, db, router, consumer, cp.CSRFToken, task.ID, run.ID, schema, mode, "empty-1", started, data, memory)
		if mode == "normal" {
			for attempt := 2; attempt <= 4; attempt++ {
				label := "empty-" + strconv.Itoa(attempt)
				if attempt < 4 {
					if e = db.Exec("TRUNCATE " + schema + ".target").Error; e != nil {
						t.Fatal(e)
					}
				} else {
					label = "existing-keys-update"
					if e = db.Exec("UPDATE " + schema + ".target SET name='stale' WHERE id%2=0").Error; e != nil {
						t.Fatal(e)
					}
				}
				started = time.Now()
				memory = startBenchmarkMemory()
				currentMemory := memory
				t.Cleanup(func() { currentMemory.finish() })
				w = workflowRequest(router, "POST", "/api/v1/etl-tasks/"+task.ID+"/run", []byte(`{}`), consumer, cp.CSRFToken, "million-run-"+strconv.Itoa(attempt))
				if w.Code != 201 {
					t.Fatalf("million rerun %d", w.Code)
				}
				if e = json.Unmarshal(w.Body.Bytes(), &run); e != nil {
					t.Fatal(e)
				}
				record = workflow.Record{}
				deadline = time.Now().Add(20 * time.Minute)
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
					time.Sleep(200 * time.Millisecond)
				}
				if record.State != "succeeded" || data.Written != rows {
					t.Fatalf("million rerun state=%s code=%s", record.State, data.ErrorCode)
				}
				benchmark[0].collect(t, db, router, consumer, cp.CSRFToken, task.ID, run.ID, schema, mode, label, started, data, memory)
			}
		}
	}
	if mode == "receipts" {
		reportPath := "/api/v1/etl-runs/" + run.ID + "/reconciliation"
		var report struct {
			Latest *struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"latest"`
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			w = workflowRequest(router, "GET", reportPath, nil, consumer, "", "")
			if w.Code != 200 {
				t.Fatalf("recipient report %d %s", w.Code, w.Body.String())
			}
			if e = json.Unmarshal(w.Body.Bytes(), &report); e != nil {
				t.Fatal(e)
			}
			if report.Latest != nil && report.Latest.State == "passed" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if report.Latest == nil || report.Latest.State != "passed" {
			t.Fatal("authorized recipient did not get a passed report")
		}
		receiptPath := "/api/v1/etl-runs/" + run.ID + "/receipts"
		input := map[string]interface{}{"decision": "accepted", "reason": "核验完成，确认接收", "expected_version": 1, "reconciliation_id": report.Latest.ID}
		if e = db.Model(&workflow.Record{}).Where("kind='etl_run' AND id=?", run.ID).Update("state", "unknown").Error; e != nil {
			t.Fatal(e)
		}
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-unknown")
		if w.Code != 409 {
			t.Fatalf("unknown transaction accepted: %d", w.Code)
		}
		if e = db.Model(&workflow.Record{}).Where("kind='etl_run' AND id=?", run.ID).Update("state", "succeeded").Error; e != nil {
			t.Fatal(e)
		}
		_, otherName := businessUser(t, service, "receipt_other_", "consumer", []string{"operator"})
		other, otherPrincipal := login(t, router, otherName)
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), other, otherPrincipal.CSRFToken, "receipt-other")
		if w.Code != 403 {
			t.Fatalf("unrelated operator accepted receipt: %d", w.Code)
		}
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-self")
		if w.Code != 201 {
			t.Fatalf("recipient receipt %d %s", w.Code, w.Body.String())
		}
		var firstReceipt workflow.DeliveryReceipt
		if e = json.Unmarshal(w.Body.Bytes(), &firstReceipt); e != nil {
			t.Fatal(e)
		}
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-self")
		var replayReceipt workflow.DeliveryReceipt
		if e = json.Unmarshal(w.Body.Bytes(), &replayReceipt); e != nil {
			t.Fatal(e)
		}
		if w.Code != 201 || replayReceipt.ID != firstReceipt.ID || replayReceipt.Version != firstReceipt.Version || replayReceipt.Reason != firstReceipt.Reason {
			t.Fatal("idempotent receipt duplicated or changed")
		}
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-stale")
		if w.Code != 409 {
			t.Fatalf("stale receipt version accepted: %d", w.Code)
		}
		input["expected_version"] = 2
		input["decision"] = "rejected"
		input["reason"] = "更正为待处理交付问题"
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-correction")
		if w.Code != 201 {
			t.Fatalf("append receipt correction: %d %s", w.Code, w.Body.String())
		}
		input["expected_version"] = 3
		input["decision"] = "accepted"
		input["reason"] = "管理员代办接收"
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), owner, op.CSRFToken, "receipt-admin-no-reason")
		if w.Code != 400 {
			t.Fatalf("administrator acted without delegate reason: %d", w.Code)
		}
		input["on_behalf_reason"] = "依据接收部门的书面验收记录代办"
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), owner, op.CSRFToken, "receipt-admin")
		if w.Code != 201 {
			t.Fatalf("administrator delegate receipt %d %s", w.Code, w.Body.String())
		}
		w = workflowRequest(router, "GET", receiptPath, nil, consumer, "", "")
		if w.Code != 200 {
			t.Fatalf("receipt history %d", w.Code)
		}
		var history struct {
			Version int `json:"version"`
			Items   []struct {
				Actor     string `json:"actor_id"`
				Recipient string `json:"recipient_id"`
				Delegate  string `json:"on_behalf_reason"`
			} `json:"items"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &history); e != nil {
			t.Fatal(e)
		}
		if history.Version != 4 || len(history.Items) != 3 || history.Items[0].Actor != ownerID || history.Items[0].Recipient != userID || history.Items[0].Delegate == "" {
			t.Fatal("receipt history or delegate identity lost")
		}
		if e = db.Where("kind='etl_run' AND id=?", run.ID).Take(&record).Error; e != nil || record.State != "succeeded" {
			t.Fatal("receipt changed execution state")
		}
		if e = db.Exec("UPDATE " + schema + ".target SET name='external-change' WHERE id=1").Error; e != nil {
			t.Fatal(e)
		}
		w = workflowRequest(router, "POST", reportPath, []byte(`{}`), consumer, cp.CSRFToken, "receipt-recheck")
		if w.Code != 202 {
			t.Fatalf("receipt recheck %d", w.Code)
		}
		oldReportID := report.Latest.ID
		deadline = time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			w = workflowRequest(router, "GET", reportPath, nil, consumer, "", "")
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			if e = json.Unmarshal(w.Body.Bytes(), &report); e != nil {
				t.Fatal(e)
			}
			if report.Latest != nil && report.Latest.ID != oldReportID && report.Latest.State == "mismatched" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if report.Latest == nil || report.Latest.ID == oldReportID || report.Latest.State != "mismatched" {
			t.Fatal("changed delivery did not produce new mismatch report")
		}
		delete(input, "on_behalf_reason")
		input["expected_version"] = 4
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-old-report")
		if w.Code != 409 {
			t.Fatalf("old passed report accepted: %d", w.Code)
		}
		input["reconciliation_id"] = report.Latest.ID
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-mismatch")
		if w.Code != 409 {
			t.Fatalf("mismatched report accepted: %d", w.Code)
		}
		input["decision"] = "rejected"
		input["reason"] = "技术对账存在差异，暂不接收"
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), consumer, cp.CSRFToken, "receipt-mismatch-rejection")
		if w.Code != 201 {
			t.Fatalf("mismatch rejection %d %s", w.Code, w.Body.String())
		}
		if e = db.Model(&workflow.Record{}).Where("kind='grant' AND id=?", grantID).Update("state", "revoked").Error; e != nil {
			t.Fatal(e)
		}
		input["expected_version"] = 5
		input["on_behalf_reason"] = "依据书面记录代办"
		w = workflowRequest(router, "POST", receiptPath, jsonBody(t, input), owner, op.CSRFToken, "receipt-revoked")
		if w.Code != 403 {
			t.Fatalf("administrator accepted for revoked recipient: %d", w.Code)
		}
		return
	}
	definition.Mapping[1].Rule = "trim"
	w = workflowRequest(router, "POST", "/api/v1/etl-tasks", jsonBody(t, definition), consumer, cp.CSRFToken, "bulk-derived-denied")
	if w.Code != 403 {
		t.Fatalf("derived authorization missing: %d", w.Code)
	}
	if e = db.Model(&workflow.Record{}).Where("kind='grant' AND id=?", grantID).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "POST", "/api/v1/etl-tasks/"+task.ID+"/run", []byte(`{}`), consumer, cp.CSRFToken, "bulk-revoked")
	if w.Code != 403 {
		t.Fatalf("revoked authorization accepted %d %s", w.Code, w.Body.String())
	}
}

// This isolated archive fixture exercises reconstruction; live CDC completeness is tested separately.
func seedBulkRawArchive(t *testing.T, objectID, mode string, rowCount int) workflow.ArchiveData {
	t.Helper()
	ctx := context.Background()
	store, e := minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Region: "us-east-1"})
	if e != nil {
		t.Fatal(e)
	}
	exists, e := store.BucketExists(ctx, "datamesh-raw")
	if e != nil {
		t.Fatal(e)
	}
	if !exists {
		if e = store.MakeBucket(ctx, "datamesh-raw", minio.MakeBucketOptions{Region: "us-east-1"}); e != nil {
			t.Fatal(e)
		}
	}
	keys := []string{}
	t.Cleanup(func() {
		for _, key := range keys {
			if e := store.RemoveObject(context.Background(), "datamesh-raw", key, minio.RemoveObjectOptions{}); e != nil {
				t.Error("fixture cleanup failed")
			}
		}
	})
	meta := workflow.ArchiveData{ManifestVersion: 2, ObjectID: objectID, Topic: "fixture." + objectID, Start: 0, End: int64(rowCount + 3), Count: rowCount + 3, Key: "tests/" + objectID + "/manifest.json", Boundary: workflow.CDCSnapshotBoundary{State: "complete", ObjectVersion: 1, Partitions: []workflow.CDCPartitionRange{{Topic: "fixture." + objectID, Partition: 0, Start: 0, End: int64(rowCount + 3)}}}, Chunks: []workflow.ArchiveChunk{}}
	var raw bytes.Buffer
	start := 0
	upload := func(key string, data []byte) {
		t.Helper()
		if _, e := store.PutObject(ctx, "datamesh-raw", key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{}); e != nil {
			t.Fatal(e)
		}
		keys = append(keys, key)
	}
	flush := func(end int) {
		t.Helper()
		var packed bytes.Buffer
		w := gzip.NewWriter(&packed)
		if _, e := w.Write(raw.Bytes()); e != nil {
			t.Fatal(e)
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256(packed.Bytes())
		key := "tests/" + objectID + "/" + strconv.Itoa(start) + ".gz"
		upload(key, packed.Bytes())
		meta.Chunks = append(meta.Chunks, workflow.ArchiveChunk{Key: key, SHA256: hex.EncodeToString(hash[:]), Partition: 0, Start: int64(start), End: int64(end), Count: end - start, Bytes: int64(raw.Len())})
		raw.Reset()
		start = end
	}
	for offset := 0; offset < rowCount+3; offset++ {
		id := offset + 1
		op := "r"
		if offset == rowCount {
			id = 1
			op = "u"
		}
		if offset == rowCount+1 {
			id = 2
			op = "d"
		}
		if offset == rowCount+2 {
			id = rowCount + 1
			op = "c"
		}
		var after interface{} = map[string]interface{}{"id": id, "name": "sensitive-" + strconv.Itoa(id), "secret": "not-granted"}
		if op == "d" {
			after = nil
		}
		if mode == "raw_key_mismatch" && offset == rowCount+2 {
			after = map[string]interface{}{"id": 99999, "name": "wrong-key", "secret": "not-granted"}
		}
		row := workflow.RawRecord{Topic: meta.Topic, Partition: 0, Offset: int64(offset), Key: jsonBody(t, map[string]interface{}{"id": id}), Event: jsonBody(t, map[string]interface{}{"op": op, "after": after})}
		if e := json.NewEncoder(&raw).Encode(row); e != nil {
			t.Fatal(e)
		}
		if (rowCount <= 10001 && (offset+1)%500 == 0) || (rowCount > 10001 && raw.Len() > 4*1024*1024-1024) || offset == rowCount+2 {
			flush(offset + 1)
		}
	}
	if mode == "raw_corrupt" {
		meta.Chunks[len(meta.Chunks)-1].SHA256 = strings.Repeat("0", 64)
	}
	digest := sha256.Sum256(jsonBody(t, meta))
	meta.SHA256 = hex.EncodeToString(digest[:])
	upload(meta.Key, jsonBody(t, meta))
	return meta
}
