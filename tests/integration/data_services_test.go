package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"gorm.io/gorm"
)

// Reuses the real, published Doris output produced by the dataflow integration.
func verifyDataServiceDelivery(t *testing.T, db *gorm.DB, router http.Handler, owner *http.Cookie, actor identity.Principal, consumer *http.Cookie, cp identity.Principal, source string, fields []workflow.Field) {
	t.Helper()
	// Exercise actual Doris delivery with an asset-only consuming account.
	var removed []identity.PlatformAccess
	if e := db.Where("user_id=? AND platform<>?", cp.UserID, "assets").Find(&removed).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Where("user_id=? AND platform<>?", cp.UserID, "assets").Delete(&identity.PlatformAccess{}).Error; e != nil {
		t.Fatal(e)
	}
	defer func() {
		if len(removed) > 0 {
			if e := db.Create(&removed).Error; e != nil {
				t.Error(e)
			}
		}
	}()
	post := func(cookie *http.Cookie, p identity.Principal, path string, body interface{}, status int) map[string]json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s: got %d want %d %s", path, w.Code, status, w.Body.String())
		}
		var out map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	apiDirectory := func(snapshot workflow.SharingSnapshot) {
		t.Helper()
		var r workflow.Record
		if e := db.Where("kind='directory_revision' AND id=?", snapshot.DirectoryRevisionID).Take(&r).Error; e != nil {
			t.Fatal(e)
		}
		var rev workflow.DirectoryRevisionData
		if e := json.Unmarshal(r.Body, &rev); e != nil {
			t.Fatal(e)
		}
		rev.Definition.Format = "api"
		rev.Definition.DataLayer = "governed_dataset"
		rev.Definition.SharingMethod = "invoke"
		rev.Definition.Items = nil
		for _, f := range fields {
			rev.Definition.Items = append(rev.Definition.Items, workflow.DirectoryItem{Name: f.Name, BusinessName: f.Name, LogicalType: f.LogicalType, Nullable: f.Nullable, Sensitive: f.Sensitive, Precision: f.Precision, Scale: f.Scale})
		}
		if e := db.Model(&r).Update("body", jsonBody(t, rev)).Error; e != nil {
			t.Fatal(e)
		}
	}
	dir := seedSharingFixture(t, db, actor.UserID, actor.DepartmentID, workflow.Resource{Type: "governed_dataset", ID: "api-directory-" + source, Version: 1}, fields)
	apiDirectory(dir)
	input := map[string]interface{}{"name": "治理成果查询接口", "dataset_id": source, "mode": "snapshot", "sql": "SELECT * FROM result WHERE id >= :minimum ORDER BY id;", "parameters": map[string]string{"minimum": "0"}, "max_page_size": 100, "directory_id": dir.DirectoryID, "expected_directory_version": 1}
	preview := post(owner, actor, "/api/v1/data-services/validate", input, 200)
	if !strings.Contains(string(preview["records"]), "Alice") {
		t.Fatal("Doris preview did not return actual result")
	}
	var base workflow.Record
	if e := db.Where("kind='dataset' AND id=?", source).Take(&base).Error; e != nil {
		t.Fatal(e)
	}
	next := post(owner, actor, "/api/v1/governance/dataflows/"+base.ParentID+"/versions/1/run", map[string]interface{}{}, 201)
	var nextID string
	if e := json.Unmarshal(next["id"], &nextID); e != nil {
		t.Fatal(e)
	}
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var run workflow.GovernanceRun
		if e := db.Where("id=?", nextID).Take(&run).Error; e != nil {
			t.Fatal(e)
		}
		if run.State == "succeeded" {
			break
		}
		if run.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("latest source run state=%s", run.State)
		}
	}
	post(owner, actor, "/api/v1/datasets/"+nextID+"/publish", map[string]interface{}{}, 200)
	input["mode"] = "latest"
	latest := post(owner, actor, "/api/v1/data-services/validate", input, 200)
	var actual string
	if e := json.Unmarshal(latest["dataset_id"], &actual); e != nil || actual != nextID {
		t.Fatalf("latest mode returned old result: %s", latest["dataset_id"])
	}
	input["mode"] = "snapshot"
	fixed := post(owner, actor, "/api/v1/data-services/validate", input, 200)
	if e := json.Unmarshal(fixed["dataset_id"], &actual); e != nil || actual != source {
		t.Fatal("fixed source moved after newer publication")
	}

	verifySameCatalogService(t, db, router, owner, actor, consumer, cp, source, input)
	created := post(owner, actor, "/api/v1/data-services", input, 201)
	var ref workflow.Resource
	if e := json.Unmarshal(created["resource"], &ref); e != nil {
		t.Fatal(e)
	}
	// Generated adapters cannot be smuggled through external-API registration.
	post(owner, actor, "/api/v1/api-resources", map[string]interface{}{"directory_id": dir.DirectoryID, "expected_directory_version": 1, "config": map[string]interface{}{"data_service": input, "auth_type": "none"}}, 400)
	post(consumer, cp, "/api/v1/data-services/"+ref.ID+"/keys", map[string]int{"days": 30}, 409)
	sharing := seedSharingFixture(t, db, actor.UserID, actor.DepartmentID, ref, fields)
	apiDirectory(sharing)
	post(consumer, cp, "/api/v1/data-services/"+ref.ID+"/keys", map[string]int{"days": 30}, 403)
	grantID := "api-grant-" + source
	grant := workflow.GrantData{Snapshot: &sharing, ApplicationID: "api-application-" + source, GranteeUserID: cp.UserID, Scope: workflow.GrantRequest{Materials: &workflow.ApplicationMaterials{Method: "invoke"}, Resource: ref, DepartmentID: cp.DepartmentID, Fields: []string{"id", "name"}, Purpose: "接口查询", ExpiresAt: time.Now().Add(time.Hour)}, ApprovedBy: actor.UserID, ApprovedAt: time.Now()}
	if e := db.Create(&workflow.Record{Kind: "grant", ID: grantID, DepartmentID: cp.DepartmentID, OwnerID: cp.UserID, ParentID: grant.ApplicationID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; e != nil {
		t.Fatal(e)
	}
	issued := post(consumer, cp, "/api/v1/data-services/"+ref.ID+"/keys", map[string]int{"days": 30}, 201)
	var token string
	if e := json.Unmarshal(issued["token"], &token); e != nil {
		t.Fatal(e)
	}
	var key workflow.DataServiceKey
	if e := db.Where("service_id=?", ref.ID).Take(&key).Error; e != nil {
		t.Fatal(e)
	}
	if key.SecretHash == token || len(key.SecretHash) != 64 || key.ExpiresAt.After(grant.Scope.ExpiresAt.Add(time.Second)) {
		t.Fatal("secret persistence or expiry not constrained")
	}
	invoke := func(token, path string, body interface{}, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", origin+path, strings.NewReader(string(jsonBody(t, body))))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("invoke: %d %s", w.Code, w.Body.String())
		}
		return w
	}
	body := map[string]interface{}{"contract_version": 1, "parameters": map[string]string{"minimum": "0"}, "page_size": 1}
	path := "/delivery/v1/services/" + ref.ID + "/invoke"
	delivered := invoke(token, path, body, 200)
	if strings.Contains(delivered.Body.String(), "Alice") || strings.Contains(delivered.Body.String(), `"amount"`) || !strings.Contains(delivered.Body.String(), "***") {
		t.Fatalf("field scope or masking lost: %s", delivered.Body.String())
	}
	body["fields"] = []string{"amount"}
	invoke(token, path, body, 403)
	delete(body, "fields")
	body["parameters"] = map[string]string{"minimum": "0 OR 1=1"}
	invoke(token, path, body, 400)
	body["parameters"] = map[string]string{"minimum": "0", "sql": "SELECT * FROM result"}
	invoke(token, path, body, 400)
	body["parameters"] = map[string]string{"minimum": "0"}
	body["cursor"] = "forged"
	invoke(token, path, body, 400)
	delete(body, "cursor")
	invoke(token, "/delivery/v1/services/wrong-service/invoke", body, 401)
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", sharing.MountID).Update("state", "paused").Error; e != nil {
		t.Fatal(e)
	}
	invoke(token, path, body, 409)
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", sharing.MountID).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&workflow.Record{}).Where("kind='dataset' AND id=?", source).Update("state", "draft").Error; e != nil {
		t.Fatal(e)
	}
	invoke(token, path, body, 409)
	if e := db.Model(&workflow.Record{}).Where("kind='dataset' AND id=?", source).Update("state", "published").Error; e != nil {
		t.Fatal(e)
	}
	post(consumer, cp, "/api/v1/data-services/"+ref.ID+"/keys/"+key.ID+"/revoke", map[string]interface{}{}, 200)
	invoke(token, path, body, 401)
	issued = post(consumer, cp, "/api/v1/data-services/"+ref.ID+"/keys", map[string]int{"days": 30}, 201)
	if e := json.Unmarshal(issued["token"], &token); e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&workflow.Record{}).Where("kind='grant' AND id=?", grantID).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	invoke(token, path, body, 403)
	grant.ApplicationID += "-new"
	if e := db.Create(&workflow.Record{Kind: "grant", ID: grantID + "-new", DepartmentID: cp.DepartmentID, OwnerID: cp.UserID, ParentID: grant.ApplicationID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; e != nil {
		t.Fatal(e)
	}
	invoke(token, path, body, 403)
}

func verifySameCatalogService(t *testing.T, db *gorm.DB, router http.Handler, owner *http.Cookie, actor identity.Principal, consumer *http.Cookie, cp identity.Principal, source string, base map[string]interface{}) {
	t.Helper()
	post := func(path string, body interface{}, cookie *http.Cookie, p identity.Principal, status int) map[string]json.RawMessage {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("same catalog %s got=%d want=%d %s", path, w.Code, status, w.Body)
		}
		var result map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	catalogID := "dir-" + source
	input := map[string]interface{}{}
	for key, value := range base {
		input[key] = value
	}
	input["directory_id"] = catalogID
	input["sql"] = "SELECT id, name FROM result WHERE id >= :minimum;"
	created := post("/api/v1/data-services", input, owner, actor, 201)
	var ref workflow.Resource
	if e := json.Unmarshal(created["resource"], &ref); e != nil {
		t.Fatal(e)
	}
	var actualDirectory string
	if e := json.Unmarshal(created["directory_id"], &actualDirectory); e != nil || actualDirectory != catalogID {
		t.Fatal("service created another catalog")
	}
	mount := post("/api/v1/resource-mounts", workflow.MountInput{DirectoryID: catalogID, Resource: ref, Mapping: map[string]string{"id": "id", "name": "name"}}, owner, actor, 201)
	var mountID string
	if e := json.Unmarshal(mount["mount_id"], &mountID); e != nil {
		t.Fatal(e)
	}
	var stored workflow.Record
	if e := db.Where("id=?", mountID).Take(&stored).Error; e != nil {
		t.Fatal(e)
	}
	var head workflow.MountData
	if e := json.Unmarshal(stored.Body, &head); e != nil || !head.ServiceVariant {
		t.Fatal("service presentation not classified by server")
	}
	validated := post("/api/v1/resource-mounts/"+mountID+"/validate", workflow.VersionAction{ExpectedVersion: 1}, owner, actor, 200)
	var current struct {
		Version    int                        `json:"version"`
		Definition workflow.MountRevisionData `json:"definition"`
	}
	if e := json.Unmarshal(jsonBody(t, validated), &current); e != nil || current.Definition.Validation == nil || !current.Definition.Validation.Passed {
		t.Fatalf("service validation failed: %s", jsonBody(t, validated))
	}
	post("/api/v1/resource-mounts/"+mountID+"/submit", workflow.VersionAction{ExpectedVersion: current.Version}, owner, actor, 200)
	var review workflow.Record
	if e := db.Where("kind='review' AND body->>'target_id'=? AND state='pending'", mountID).Take(&review).Error; e != nil {
		t.Fatal(e)
	}
	post("/api/v1/directory-reviews/"+review.ID+"/approve", workflow.VersionAction{ExpectedVersion: review.Version}, owner, actor, 200)
	if e := db.Where("id=?", mountID).Take(&stored).Error; e != nil {
		t.Fatal(e)
	}
	post("/api/v1/resource-mounts/"+mountID+"/activate", workflow.VersionAction{ExpectedVersion: stored.Version}, owner, actor, 200)
	var active int64
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND parent_id=? AND state='active'", catalogID).Count(&active).Error; e != nil || active != 2 {
		t.Fatal("table and API must coexist")
	}
	// The consumer already holds a source-dataset grant; it must not authorize the API presentation.
	post("/api/v1/data-services/"+ref.ID+"/keys", map[string]int{"days": 30}, consumer, cp, 403)
	if e := db.Model(&workflow.Record{}).Where("id=?", "mount-"+source).Update("state", "suspended").Error; e != nil {
		t.Fatal(e)
	}
	w := workflowRequest(router, "GET", "/api/v1/resource-directories/"+catalogID, nil, owner, "", "")
	var view struct {
		Resources []struct {
			Resource  workflow.Resource `json:"resource"`
			Available bool              `json:"available"`
		} `json:"resources"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil || w.Code != 200 {
		t.Fatalf("catalog availability=%d", w.Code)
	}
	for _, resource := range view.Resources {
		if resource.Resource.ID == ref.ID && resource.Available {
			t.Fatal("API remained available after source sharing suspended")
		}
	}
	if e := db.Model(&workflow.Record{}).Where("id=?", "mount-"+source).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
}
