package integration

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceMetadataObservations(t *testing.T) {
	u, err := url.Parse(os.Getenv("TEST_APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	database := strings.TrimPrefix(u.Path, "/")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "meta_admin_", "metadata-dept", []string{"admin"})
	governorID, governorName := businessUser(t, service, "meta_governor_", "metadata-dept", []string{"governor"})
	_, unassignedName := businessUser(t, service, "meta_unassigned_", "metadata-dept", []string{"governor"})
	adminCookie, administrator := login(t, router, ownerName)
	cookie, actor := login(t, router, governorName)
	unassigned, _ := login(t, router, unassignedName)
	pass, _ := u.User.Password()
	sourceInput := workflow.SourceInput{Name: "元数据来源", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: ownerID, Credentials: workflow.Credential{Username: u.User.Username(), Password: pass}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, sourceInput), adminCookie, administrator.CSRFToken, "metadata-source")
	if w.Code != 201 {
		t.Fatalf("source=%d %s", w.Code, w.Body.String())
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("CREATE TABLE public.governance_metadata_fixture(customer_id bigint PRIMARY KEY, customer_name text); GRANT SELECT ON public.governance_metadata_fixture TO datamesh_app").Error; err != nil {
		t.Fatal(err)
	}
	objectID := "metadata-object-" + source.ID
	obj := workflow.Object{ObjectID: objectID, SourceID: source.ID, QualifiedName: "public.governance_metadata_fixture", SchemaVersion: 1, Fields: []workflow.Field{{Name: "customer_id", LogicalType: "integer", PrimaryKey: true}, {Name: "customer_name", LogicalType: "string", Nullable: true}}}
	if err = db.Create(&workflow.Record{Kind: "object", ID: objectID, DepartmentID: "metadata-dept", OwnerID: ownerID, ParentID: source.ID, State: "available", Version: 1, Body: jsonBody(t, obj)}).Error; err != nil {
		t.Fatal(err)
	}
	seedAssignment(t, db, governorID, "metadata-dept", "govern_resource")
	path := "/api/v1/governance/metadata/" + objectID
	post := func(path string, body interface{}, key string, want int) map[string]json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, actor.CSRFToken, key)
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		result := map[string]json.RawMessage{}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for i := 1; i <= 2; i++ {
		if i == 2 {
			if err = db.Exec("ALTER TABLE public.governance_metadata_fixture ADD COLUMN status varchar(32)").Error; err != nil {
				t.Fatal(err)
			}
		}
		result := post(path+"/refresh", map[string]int{"source_version": 1, "object_version": 1}, fmt.Sprint("observe-", i), 201)
		var fields []workflow.Field
		if err = json.Unmarshal(result["fields"], &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != i+1 {
			t.Fatalf("real fields=%d", len(fields))
		}
	}

	if err = db.Exec("ALTER TABLE public.governance_metadata_fixture ALTER COLUMN status TYPE varchar(64)").Error; err != nil {
		t.Fatal(err)
	}
	physical := post(path+"/refresh", map[string]int{"source_version": 1, "object_version": 1}, "observe-length", 201)
	if !strings.Contains(string(physical["changes"]), "field_database_type_changed") {
		t.Fatalf("native type change lost: %s", physical["changes"])
	}
	w = workflowRequest(router, "GET", path+"/observations", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("observations=%d %s", w.Code, w.Body.String())
	}
	var history struct {
		Items []struct {
			ID      string           `json:"id"`
			Version int              `json:"version"`
			Fields  []workflow.Field `json:"fields"`
		} `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 3 || history.Items[0].Version != 3 || len(history.Items[2].Fields) != 2 {
		t.Fatal("observation history overwritten")
	}
	annotation := map[string]interface{}{"name": "客户元数据", "owner_id": governorID, "steward_id": governorID, "metadata": map[string]interface{}{"object_id": objectID, "observation_id": history.Items[0].ID, "fields": []map[string]interface{}{{"name": "status", "description": "客户状态"}}}}
	record := post("/api/v1/governance/metadata-definitions", map[string]interface{}{"definition": annotation}, "metadata-definition", 201)
	var definitionID string
	if err = json.Unmarshal(record["id"], &definitionID); err != nil {
		t.Fatal(err)
	}
	post("/api/v1/governance/metadata-definitions/"+definitionID+"/versions/1/publish", map[string]interface{}{}, "metadata-publish", 200)

	w = workflowRequest(router, "GET", "/api/v1/governance/metadata-definitions?object_id=another-object", nil, cookie, "", "")
	var filtered struct {
		Total int `json:"total"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &filtered); err != nil || w.Code != 200 || filtered.Total != 0 {
		t.Fatalf("metadata object filter=%d %s", w.Code, w.Body.String())
	}
	annotation["metadata"] = map[string]interface{}{"object_id": objectID, "observation_id": history.Items[0].ID, "fields": []map[string]interface{}{{"name": "missing_field", "description": "不存在"}}}
	post("/api/v1/governance/metadata-definitions", map[string]interface{}{"definition": annotation}, "missing-field", 400)
	if w := workflowRequest(router, "GET", path+"/observations", nil, unassigned, "", ""); w.Code != 403 {
		t.Fatalf("unassigned metadata read=%d", w.Code)
	}
	var unchanged workflow.Record
	if err = db.Where("kind='object' AND id=?", objectID).Take(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != 1 || !jsonEquivalent(unchanged.Body, jsonBody(t, obj)) {
		t.Fatal("refresh silently activated a new exchange schema")
	}
	post(path+"/refresh", map[string]int{"source_version": 2, "object_version": 1}, "stale-source", 409)
}

func assertNativeMetadataColumn(t *testing.T, db *gorm.DB, router http.Handler, cookie *http.Cookie, p identity.Principal, object workflow.Object, column string, length int64) {
	t.Helper()
	if err := db.Create(&workflow.Record{Kind: "object", ID: object.ObjectID, DepartmentID: p.DepartmentID, OwnerID: p.UserID, ParentID: object.SourceID, State: "available", Version: 1, Body: jsonBody(t, object)}).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := workflowRequest(router, "POST", "/api/v1/governance/metadata/"+object.ObjectID+"/refresh", []byte(`{"source_version":1,"object_version":1}`), cookie, p.CSRFToken, "native-metadata-"+object.ObjectID)
	if response.Code != 201 {
		t.Fatalf("native metadata refresh=%d elapsed=%s %s", response.Code, time.Since(started), response.Body.String())
	}
	var observation workflow.MetadataObservation
	if err := json.Unmarshal(response.Body.Bytes(), &observation); err != nil {
		t.Fatal(err)
	}
	var columns []workflow.MetadataColumn
	if err := json.Unmarshal(observation.Columns, &columns); err != nil {
		t.Fatal(err)
	}
	for _, field := range columns {
		if field.Name == column {
			if field.NativeType == "" || field.Length == nil || *field.Length != length {
				t.Fatalf("native metadata mismatch: %+v", field)
			}
			assertGovernanceNativeProfile(t, db, router, cookie, p, object.SourceID, observation, column)
			return
		}
	}
	t.Fatal("native metadata column missing")
}

func assertGovernanceNativeProfile(t *testing.T, db *gorm.DB, router http.Handler, cookie *http.Cookie, p identity.Principal, sourceID string, observation workflow.MetadataObservation, column string) {
	t.Helper()
	var fields []workflow.Field
	if err := json.Unmarshal(observation.Fields, &fields); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, field := range fields {
		if field.PrimaryKey {
			keys = append(keys, field.Name)
		}
	}
	id := "native-profile-" + observation.ID
	managed := workflow.ManagedResource{Name: "原生数据库剖析", DataLayer: "raw", Adapter: "database", Fields: fields, Config: jsonBody(t, workflow.TableConfig{SourceID: sourceID, Schema: observation.Schema, Table: observation.Table, Fields: fields, Keys: keys})}
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: p.DepartmentID, OwnerID: p.UserID, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
		t.Fatal(err)
	}
	w := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: id, Version: 1}, "fields": []string{column}, "mode": "full"}), cookie, p.CSRFToken, "native-profile-"+observation.ID)
	if w.Code != 201 {
		t.Fatalf("native profile enqueue=%d %s", w.Code, w.Body.String())
	}
	var run workflow.GovernanceRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		w = workflowRequest(router, "GET", "/api/v1/governance/runs/"+run.ID, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("native profile read=%d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.State == "failed" {
			t.Fatalf("native profile failed code=%s", run.ErrorCode)
		}
		if run.State != "succeeded" {
			continue
		}
		var report workflow.ProfileReport
		if err := json.Unmarshal(run.Result, &report); err != nil || report.Rows < 1 || report.Rows != report.TotalRows || report.Coverage != "full" || len(report.Fields) != 1 || report.Fields[0].Name != column {
			t.Fatalf("native profile coverage invalid: %s", run.Result)
		}
		return
	}
	t.Fatal("native profile did not finish")
}
