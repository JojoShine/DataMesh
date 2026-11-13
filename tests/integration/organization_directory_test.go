package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
)

func TestOrganizationDirectory(t *testing.T) {
	db, service, router := fixture(t, false)
	adminID, adminName := businessUser(t, service, "orgadmin_", "org-admin", []string{"admin"})
	personID, personName := businessUser(t, service, "orgperson_", "org-old", []string{"provider", "governor"})
	administrator, principal := login(t, router, adminName)
	person, personPrincipal := login(t, router, personName)
	var serial atomic.Int64
	call := func(method, path string, body interface{}, cookie *http.Cookie, p identity.Principal) *httptestResponse {
		var payload []byte
		if body != nil {
			payload = jsonBody(t, body)
		}
		w := workflowRequest(router, method, path, payload, cookie, p.CSRFToken, fmt.Sprintf("org-%s-%d", adminID, serial.Add(1)))
		return &httptestResponse{Code: w.Code, Body: w.Body.Bytes()}
	}
	if w := call("GET", "/api/v1/organization-units", nil, person, personPrincipal); w.Code != 403 {
		t.Fatalf("non-admin units=%d", w.Code)
	}
	if w := call("POST", "/api/v1/organization-units", map[string]interface{}{"kind": "organization", "name": "非法组织", "code": "FORGED", "active": true, "reason": "test"}, person, personPrincipal); w.Code != 403 {
		t.Fatalf("non-admin write=%d", w.Code)
	}
	createUnit := func(kind, name, code string, parent *string) identity.OrganizationUnit {
		w := call("POST", "/api/v1/organization-units", map[string]interface{}{"kind": kind, "name": name, "code": code, "parent_id": parent, "active": true, "reason": "integration organization"}, administrator, principal)
		if w.Code != 201 {
			t.Fatalf("create unit=%d %s", w.Code, w.Body)
		}
		var unit identity.OrganizationUnit
		if e := json.Unmarshal(w.Body, &unit); e != nil {
			t.Fatal(e)
		}
		return unit
	}
	org := createUnit("organization", "正式提供组织", "ORG001", nil)
	dept := createUnit("department", "业务部门", "DEPT001", &org.ID)
	sub := createUnit("department", "下级部门", "DEPT002", &dept.ID)
	if w := call("POST", "/api/v1/organization-units/"+org.ID+"/revisions", map[string]interface{}{"kind": org.Kind, "name": org.Name, "code": org.Code, "parent_id": nil, "active": false, "expected_version": org.Version, "reason": "stop"}, administrator, principal); w.Code != 409 {
		t.Fatalf("stop parent=%d", w.Code)
	}
	seedAssignment(t, db, personID, "org-old", "manage_resource")
	update := map[string]interface{}{"display_name": "业务人员", "department_id": sub.ID, "platforms": []string{"exchange", "assets", "security", "governance"}, "roles": []string{"provider", "governor"}, "expected_version": 1, "reason": "人员调岗"}
	if w := call("POST", "/api/v1/accounts/"+personID+"/revisions", update, administrator, principal); w.Code != 200 {
		t.Fatalf("transfer=%d %s", w.Code, w.Body)
	}
	if w := call("GET", "/api/v1/me", nil, person, personPrincipal); w.Code != 401 {
		t.Fatalf("old session=%d", w.Code)
	}
	var active int64
	if e := db.Model(&workflow.Record{}).Where("kind='assignment' AND state='active' AND body->>'user_id'=?", personID).Count(&active).Error; e != nil || active != 0 {
		t.Fatal("old department powers retained")
	}
	person, personPrincipal = login(t, router, personName)
	if personPrincipal.DepartmentID != sub.ID {
		t.Fatal("transfer not reflected")
	}
	if w := call("GET", "/api/v1/directory-provider", nil, person, personPrincipal); w.Code != 200 {
		t.Fatalf("provider=%d", w.Code)
	} else {
		var value identity.Affiliation
		if e := json.Unmarshal(w.Body, &value); e != nil {
			t.Fatal(e)
		}
		if value.ProviderName != org.Name || value.ProviderCode != org.Code || value.DepartmentName != sub.Name {
			t.Fatal("provider must resolve root organization")
		}
	}
	dict := workflow.DirectoryDictionary{Name: "隔离编码规则", Version: "1", Source: "integration-custom", Verified: true, CodePattern: `^ORG001-[0-9]{6}$`, Classifications: []string{"business/customer"}, VerificationRef: "integration-only", CodeTemplate: "", SequenceWidth: 0}
	w := call("POST", "/api/v1/directory-dictionaries", dict, administrator, principal)
	if w.Code != 201 {
		t.Fatalf("dictionary=%d %s", w.Code, w.Body)
	}
	var dictionary struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(w.Body, &dictionary); e != nil {
		t.Fatal(e)
	}
	coding := map[string]interface{}{"code_template": "{provider_code}-{sequence}", "sequence_width": 6, "expected_version": 1, "reason": "configure verified generation"}
	if w = call("POST", "/api/v1/directory-dictionaries/"+dictionary.ID+"/coding", coding, person, personPrincipal); w.Code != 403 {
		t.Fatalf("non-authority coding=%d", w.Code)
	}
	if w = call("POST", "/api/v1/directory-dictionaries/"+dictionary.ID+"/coding", coding, administrator, principal); w.Code != 200 {
		t.Fatalf("configure coding=%d %s", w.Code, w.Body)
	}
	if e := db.Create(&workflow.Record{Kind: "assignment", ID: "new-scope-" + personID, DepartmentID: sub.ID, OwnerID: personID, State: "active", Version: 1, Body: jsonBody(t, workflow.ResourceAssignment{UserID: personID, DepartmentID: sub.ID, ResourceID: "*", Capability: "manage_resource", Reason: "new department assignment", ExpiresAt: time.Now().Add(time.Hour)})}).Error; e != nil {
		t.Fatal(e)
	}
	definition := workflow.DirectoryDefinition{Name: "自动编码目录", Classification: "department", ClassificationPath: "business/customer", DictionaryID: dictionary.ID, ProviderName: "伪造单位", ProviderCode: "FORGED", Format: "database", DataLayer: "raw_object", SharingType: "conditional", UpdateFrequency: "每日", ResourceCode: "FORGED-MANUAL-CODE"}
	results := make(chan *httptestResponse, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- call("POST", "/api/v1/resource-directories", definition, person, personPrincipal)
		}()
	}
	wait.Wait()
	close(results)
	codes := map[string]bool{}
	var first struct {
		DirectoryID string                       `json:"directory_id"`
		Version     int                          `json:"version"`
		Definition  workflow.DirectoryDefinition `json:"definition"`
	}
	for result := range results {
		if result.Code != 201 {
			t.Fatalf("auto directory=%d %s", result.Code, result.Body)
		}
		if e := json.Unmarshal(result.Body, &first); e != nil {
			t.Fatal(e)
		}
		if first.Definition.ProviderName != org.Name || first.Definition.ProviderCode != org.Code || (first.Definition.ResourceCode == "FORGED-MANUAL-CODE" || first.Definition.ResourceCode == "") || codes[first.Definition.ResourceCode] {
			t.Fatal("provider spoofing or duplicate code")
		}
		codes[first.Definition.ResourceCode] = true
	}
	payload := jsonBody(t, definition)
	retryKey := "retry-directory-" + adminID
	original := workflowRequest(router, "POST", "/api/v1/resource-directories", payload, person, personPrincipal.CSRFToken, retryKey)
	replay := workflowRequest(router, "POST", "/api/v1/resource-directories", payload, person, personPrincipal.CSRFToken, retryKey)
	var originalValue, replayValue map[string]interface{}
	if e := json.Unmarshal(original.Body.Bytes(), &originalValue); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(replay.Body.Bytes(), &replayValue); e != nil {
		t.Fatal(e)
	}
	// PostgreSQL jsonb normalizes whitespace and key order in the replay response.
	if original.Code != 201 || replay.Code != 201 || !reflect.DeepEqual(originalValue, replayValue) {
		t.Fatalf("idempotent create differs: original=%d replay=%d", original.Code, replay.Code)
	}
	var countBefore int64
	if e := db.Table("directory_code_reservations").Count(&countBefore).Error; e != nil {
		t.Fatal(e)
	}
	coding["code_template"] = "{provider_code}-X{sequence}"
	coding["expected_version"] = 2
	if w = call("POST", "/api/v1/directory-dictionaries/"+dictionary.ID+"/coding", coding, administrator, principal); w.Code != 200 {
		t.Fatalf("change coding=%d", w.Code)
	}
	if w = call("POST", "/api/v1/resource-directories", definition, person, personPrincipal); w.Code != 400 {
		t.Fatalf("mismatched code must fail=%d", w.Code)
	}
	var countAfter int64
	if e := db.Table("directory_code_reservations").Count(&countAfter).Error; e != nil || countAfter != countBefore {
		t.Fatal("failed generation reserved a code")
	}
	coding["code_template"] = "{provider_code}-{sequence}"
	coding["expected_version"] = 3
	if w = call("POST", "/api/v1/directory-dictionaries/"+dictionary.ID+"/coding", coding, administrator, principal); w.Code != 200 {
		t.Fatalf("restore coding=%d", w.Code)
	}
	oldCode := first.Definition.ResourceCode
	first.Definition.ResourceCode = ""
	w = call("POST", "/api/v1/resource-directories/"+first.DirectoryID+"/revisions", map[string]interface{}{"expected_version": first.Version, "definition": first.Definition}, person, personPrincipal)
	if w.Code != 200 {
		t.Fatalf("revision=%d %s", w.Code, w.Body)
	}
	if e := json.Unmarshal(w.Body, &first); e != nil || first.Definition.ResourceCode != oldCode {
		t.Fatal("draft revision changed code")
	}
	self := map[string]interface{}{"display_name": "管理员", "department_id": "org-admin", "roles": []string{"consumer"}, "platforms": []string{"security"}, "expected_version": 1, "reason": "self demotion"}
	if w = call("POST", "/api/v1/accounts/"+adminID+"/revisions", self, administrator, principal); w.Code != 409 {
		t.Fatalf("self demotion=%d", w.Code)
	}
	if w = call("POST", "/api/v1/accounts/"+personID+"/disable", map[string]interface{}{}, administrator, principal); w.Code != 200 {
		t.Fatalf("disable=%d", w.Code)
	}
	if w = call("POST", "/api/v1/accounts/"+personID+"/enable", map[string]interface{}{"expected_version": 3, "reason": "恢复岗位"}, administrator, principal); w.Code != 200 {
		t.Fatalf("enable=%d %s", w.Code, w.Body)
	}
	if w = call("GET", "/api/v1/accounts?page=1", nil, administrator, principal); w.Code != 200 {
		t.Fatalf("account list=%d", w.Code)
	}
	var changes int64
	if e := db.Table("identity_change_records").Where("subject_id=?", personID).Count(&changes).Error; e != nil || changes < 2 {
		t.Fatal("identity change evidence missing")
	}
}

type httptestResponse struct {
	Code int
	Body []byte
}
