package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExtendedSourceReadLifecycle(t *testing.T) {
	token := strings.Repeat("reader-integration-", 3)
	fields := []workflow.Field{{Name: "record_key", LogicalType: "string", PrimaryKey: true}, {Name: "document", LogicalType: "string"}}
	var changed atomic.Bool
	var calls atomic.Int32
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("reader control authorization missing")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/capabilities" {
			json.NewEncoder(w).Encode(map[string]interface{}{"items": []map[string]interface{}{{"kind": "redis", "ready": true, "status": "ready"}, {"kind": "dm", "ready": false, "status": "driver_missing"}}})
			return
		}
		var input struct {
			Action, Kind, Schema, Table string
			Credentials                 workflow.Credential
		}
		if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
			t.Error(e)
		}
		calls.Add(1)
		switch input.Action {
		case "probe":
			json.NewEncoder(w).Encode(map[string]interface{}{"passed": true})
		case "objects":
			json.NewEncoder(w).Encode(map[string]interface{}{"items": []workflow.SourceTable{{Schema: "0", Name: "keys", Kind: "KEYSPACE"}}})
		case "schema":
			current := fields
			if changed.Load() {
				current = []workflow.Field{{Name: "changed", LogicalType: "string"}}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"fields": current, "keys": []string{"record_key"}})
		case "read":
			json.NewEncoder(w).Encode(map[string]interface{}{"records": []map[string]interface{}{{"record_key": "YQ==", "document": "{\"业务编号\":\"1001\"}"}, {"record_key": "Yg==", "document": "{\"业务编号\":\"1002\"}"}}, "total": 2})
		default:
			t.Error("unknown action")
		}
	}))
	defer reader.Close()
	t.Setenv("SOURCE_READER_ORIGIN", reader.URL)
	t.Setenv("SOURCE_READER_TOKEN", token)
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "source_expand_", "expand-dept", []string{"admin"})
	_, outsideName := businessUser(t, service, "source_expand_out_", "outside-dept", []string{"consumer"})
	owner, p := login(t, router, ownerName)
	outside, _ := login(t, router, outsideName)
	post := func(path string, body interface{}, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), owner, p.CSRFToken, fmt.Sprint("source-expand-", path, calls.Load()))
		if w.Code != want {
			t.Fatalf("%s %d %s", path, w.Code, w.Body)
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	post("/api/v1/source-allowed-targets", workflow.SourceTarget{Kind: "redis", Host: "reader-fixture.internal", Port: 6379, Database: "0"}, 201)
	created := post("/api/v1/sources", workflow.SourceInput{Name: "业务缓存", Kind: "redis", Host: "reader-fixture.internal", Port: 6379, Database: "0", Scheme: "http", OwnerID: ownerID}, 201)
	sourceID := created["source_id"].(string)
	if strings.Contains(fmt.Sprint(created), "ciphertext") {
		t.Fatal("credential leaked")
	}
	post("/api/v1/sources/"+sourceID+"/test-connection", map[string]interface{}{}, 200)
	if w := workflowRequest(router, "GET", "/api/v1/sources/"+sourceID+"/tables", nil, outside, "", ""); w.Code != 403 {
		t.Fatalf("unauthorized enumeration=%d", w.Code)
	}
	catalog := workflowRequest(router, "GET", "/api/v1/source-types", nil, owner, "", "")
	if catalog.Code != 200 || !strings.Contains(catalog.Body.String(), "driver_missing") {
		t.Fatal("catalog/driver state missing")
	}
	objects := workflowRequest(router, "GET", "/api/v1/sources/"+sourceID+"/tables", nil, owner, "", "")
	if objects.Code != 200 || !strings.Contains(objects.Body.String(), "KEYSPACE") {
		t.Fatalf("objects=%d %s", objects.Code, objects.Body)
	}
	snapshot := seedSharingFixture(t, db, ownerID, p.DepartmentID, workflow.Resource{Type: "raw_object", ID: "external-" + ownerID, Version: 1}, fields)
	resource := post("/api/v1/table-resources", workflow.TableResourceInput{DirectoryID: snapshot.DirectoryID, SourceID: sourceID, Schema: "0", Table: "keys", ExpectedVersion: 1}, 201)
	ref := resource["resource"].(map[string]interface{})
	resourceID := ref["resource_id"].(string)
	previewPath := "/api/v1/table-resources/" + resourceID + "/preview"
	preview := workflowRequest(router, "GET", previewPath, nil, owner, "", "")
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "1002") {
		t.Fatalf("preview=%d %s", preview.Code, preview.Body)
	}
	if w := workflowRequest(router, "GET", previewPath, nil, outside, "", ""); w.Code != 403 {
		t.Fatal("preview authorization bypass")
	}
	// The mounted adapter must be consumable by real asynchronous governance workers.
	profile := post("/api/v1/governance/profiles", map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: resourceID, Version: 1}, "fields": []string{"document"}, "mode": "full"}, 201)
	runID := profile["id"].(string)
	deadline := time.Now().Add(15 * time.Second)
	for {
		w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID, nil, owner, "", "")
		var run workflow.GovernanceRun
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
			t.Fatalf("profile result=%d %s", w.Code, w.Body)
		}
		if run.State == "failed" {
			t.Fatalf("external source profile failed: %s", run.ErrorCode)
		}
		if run.State == "succeeded" {
			var report workflow.ProfileReport
			if e := json.Unmarshal(run.Result, &report); e != nil || report.Rows != 2 || report.TotalRows != 2 || report.Coverage != "full" || len(report.Fields) != 1 || report.Fields[0].Name != "document" {
				t.Fatalf("external source coverage=%s", run.Result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("external source profile did not complete")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Exercise the existing authorized ETL resource path, including real transactional target writes.
	resourceRef := workflow.Resource{Type: "table_resource", ID: resourceID, Version: 1}
	sharing := seedSharingFixture(t, db, ownerID, p.DepartmentID, resourceRef, fields)
	if e := db.Exec("UPDATE business_records SET body=jsonb_set(body,'{definition,data_layer}','\"raw_object\"'::jsonb) WHERE kind='directory_revision' AND id=?", sharing.DirectoryRevisionID).Error; e != nil {
		t.Fatal(e)
	}
	targetURL, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	targetPort, e := strconv.Atoi(targetURL.Port())
	if e != nil {
		t.Fatal(e)
	}
	targetDatabase := strings.TrimPrefix(targetURL.Path, "/")
	targetPassword, _ := targetURL.User.Password()
	post("/api/v1/source-allowed-targets", workflow.SourceTarget{Kind: "postgres", Host: targetURL.Hostname(), Port: targetPort, Database: targetDatabase}, 201)
	destination := post("/api/v1/sources", workflow.SourceInput{Name: "治理交换目标", Kind: "postgres", Host: targetURL.Hostname(), Port: targetPort, Database: targetDatabase, OwnerID: ownerID, Credentials: workflow.Credential{Username: targetURL.User.Username(), Password: targetPassword}}, 201)
	targetTable := "extended_source_" + strings.ToLower(strings.ReplaceAll(ownerID, "-", ""))
	if e := db.Exec("CREATE TABLE public." + targetTable + " (record_key text PRIMARY KEY, document text NOT NULL)").Error; e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/api/v1/sources/" + destination["source_id"].(string) + "/table-schema?schema=public&table=" + targetTable, "/api/v1/etl-capabilities?resource_type=table_resource&resource_id=" + resourceID + "&contract_version=1"} {
		w := workflowRequest(router, "GET", path, nil, owner, "", "")
		if w.Code != 200 {
			t.Fatalf("ETL prerequisite %s=%d %s", path, w.Code, w.Body)
		}
	}
	task := post("/api/v1/etl-tasks", workflow.ETLDefinition{Name: "扩展来源交换", SourceResource: &resourceRef, Target: workflow.TableConfig{SourceID: destination["source_id"].(string), Schema: "public", Table: targetTable}, Mapping: []workflow.ETLMapping{{Source: "record_key", Target: "record_key", Rule: "copy"}, {Source: "document", Target: "document", Rule: "copy"}}}, 201)
	taskID := task["id"].(string)
	post("/api/v1/etl-tasks/"+taskID+"/run", map[string]interface{}{}, 201)
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		w := workflowRequest(router, "GET", "/api/v1/etl-tasks/"+taskID+"/runs", nil, owner, "", "")
		var runs struct {
			Items []struct {
				State string          `json:"state"`
				Data  workflow.ETLRun `json:"data"`
			} `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &runs) != nil {
			t.Fatalf("ETL runs=%d %s", w.Code, w.Body)
		}
		if len(runs.Items) > 0 {
			if runs.Items[0].State == "failed" || runs.Items[0].State == "unknown" {
				t.Fatalf("external ETL failed: %s", w.Body)
			}
			if runs.Items[0].State == "succeeded" {
				if runs.Items[0].Data.Written != 2 {
					t.Fatalf("external ETL rows=%s", w.Body)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("external ETL did not complete")
		}
	}
	var written int64
	if e := db.Table(targetTable).Count(&written).Error; e != nil || written != 2 {
		t.Fatalf("target count=%d error=%v", written, e)
	}
	before := calls.Load()
	post("/api/v1/sources/"+sourceID+"/preflight-cdc", map[string]interface{}{}, 200)
	if calls.Load() != before {
		t.Fatal("new source entered CDC reader")
	}
	changed.Store(true)
	if w := workflowRequest(router, "GET", previewPath, nil, owner, "", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "SOURCE_SCHEMA_CHANGED") {
		t.Fatalf("schema change not blocked: %d %s", w.Code, w.Body)
	}
}
