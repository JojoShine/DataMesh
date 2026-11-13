package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
)

func workflowRequest(router http.Handler, method, path string, body []byte, cookie *http.Cookie, csrf, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://localhost:18443"+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Idempotency-Key", key)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func jsonBody(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func businessUser(t *testing.T, service *identity.Service, name, department string, roles []string) (string, string) {
	t.Helper()
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	username := name + suffix
	id, e := service.CreateUser(context.Background(), identity.NewUser{Username: username, Password: password, DepartmentID: department, Platforms: []string{"exchange", "assets", "governance", "security"}, Roles: roles})
	if e != nil {
		t.Fatal(e)
	}
	return id, username
}
func TestBusinessAuthorizationAndConcurrentApproval(t *testing.T) {
	db, service, router := fixture(t)
	providerID, providerName := businessUser(t, service, "provide_", "provider-dept", []string{"provider", "governor", "operator"})
	consumerID, consumerName := businessUser(t, service, "consume_", "consumer-dept", []string{"consumer"})
	_, otherName := businessUser(t, service, "other_", "another-dept", []string{"provider"})
	_, auditorName := businessUser(t, service, "audit_", "audit-dept", []string{"auditor"})
	provider, providerPrincipal := login(t, router, providerName)
	consumer, consumerPrincipal := login(t, router, consumerName)
	other, otherPrincipal := login(t, router, otherName)
	auditor, _ := login(t, router, auditorName)
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Nullable: true, Sensitive: true}}
	objectID := "object-" + providerID
	datasetID := "dataset-" + providerID
	object := workflow.Record{Kind: "object", ID: objectID, DepartmentID: "provider-dept", OwnerID: providerID, State: "available", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: objectID, SourceID: "isolated-source", QualifiedName: "public.customers", SchemaVersion: 1, Fields: fields})}
	dataset := workflow.Record{Kind: "dataset", ID: datasetID, DepartmentID: "provider-dept", OwnerID: providerID, ParentID: objectID, State: "published", Version: 1, Body: jsonBody(t, workflow.DatasetData{Definition: workflow.DatasetInput{Name: "隔离权限样例", Description: "仅测试目录和审批事务，不作为实际处理证据", SourceObjectID: objectID, OwnerID: providerID, Fields: fields, UsageRestrictions: "测试"}, QualityStatus: "not_checked", CompletenessStatus: "pending", LatestWatermark: json.RawMessage("null"), Quality: json.RawMessage("null")})}
	for _, r := range []workflow.Record{object, dataset} {
		if e := db.Create(&r).Error; e != nil {
			t.Fatal(e)
		}
	}
	seedAssignment(t, db, providerID, "provider-dept", "share_approve")
	seedAssignment(t, db, providerID, "provider-dept", "manage_resource")
	seedSharingFixture(t, db, providerID, "provider-dept", workflow.Resource{Type: "governed_dataset", ID: datasetID, Version: 1}, fields)
	t.Run("roles_and_public_summary", func(t *testing.T) {
		if w := workflowRequest(router, "GET", "/api/v1/sources", nil, consumer, "", ""); w.Code != 403 {
			t.Fatalf("consumer sources=%d", w.Code)
		}
		w := workflowRequest(router, "GET", "/api/public/assets/"+datasetID, nil, nil, "", "")
		var summary struct {
			Name    string `json:"name"`
			Details struct {
				Items           []workflow.DirectoryItem `json:"items"`
				UpdateFrequency string                   `json:"update_frequency"`
			} `json:"details"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &summary); e != nil {
			t.Fatal(e)
		}
		if summary.Name != "隔离目录" {
			t.Fatalf("public content must use reviewed directory, got %s", summary.Name)
		}
		if len(summary.Details.Items) != len(fields) || summary.Details.UpdateFrequency != "每日" {
			t.Fatal("anonymous details must include reviewed fields and update declaration")
		}
		for _, forbidden := range []string{"responsible_user_id", "department_approval_ref", "assessment_ref", "reviewer", "records"} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatalf("public detail leaked internal metadata: %s", forbidden)
			}
		}
		if w.Code != 200 || strings.Contains(w.Body.String(), "owner_id") || strings.Contains(w.Body.String(), "password") {
			t.Fatalf("public summary boundary: %d", w.Code)
		}
		if w := workflowRequest(router, "GET", "/api/v1/audit", nil, consumer, "", ""); w.Code != 403 {
			t.Fatalf("consumer audit=%d", w.Code)
		}
		if w := workflowRequest(router, "GET", "/api/v1/audit", nil, auditor, "", ""); w.Code != 200 {
			t.Fatalf("auditor=%d", w.Code)
		}
	})
	application := workflow.GrantRequest{Materials: &workflow.ApplicationMaterials{LegalBasis: "隔离测试", Scenario: "权限测试", Scope: "测试字段", Method: "query", DepartmentApprovalRef: "test"}, Resource: workflow.Resource{Type: "governed_dataset", ID: datasetID, Version: 1}, DepartmentID: "consumer-dept", Purpose: "隔离事务验证", Fields: []string{"id", "name"}, ExpiresAt: time.Now().Add(time.Hour)}
	body := jsonBody(t, application)
	first := workflowRequest(router, "POST", "/api/v1/applications", body, consumer, consumerPrincipal.CSRFToken, "apply-once")
	if first.Code != 201 {
		t.Fatalf("application=%d %s", first.Code, first.Body.String())
	}
	var created struct {
		ID string `json:"application_id"`
	}
	if e := json.Unmarshal(first.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	t.Run("idempotency_payload_conflict_and_department", func(t *testing.T) {
		repeated := workflowRequest(router, "POST", "/api/v1/applications", body, consumer, consumerPrincipal.CSRFToken, "apply-once")
		if repeated.Code != 201 || !jsonEquivalent(first.Body.Bytes(), repeated.Body.Bytes()) {
			t.Fatal("same request must reuse original application")
		}
		modified := application
		modified.Purpose = "不同用途"
		if w := workflowRequest(router, "POST", "/api/v1/applications", jsonBody(t, modified), consumer, consumerPrincipal.CSRFToken, "apply-once"); w.Code != 409 {
			t.Fatalf("payload conflict=%d", w.Code)
		}
		if w := workflowRequest(router, "GET", "/api/v1/applications/"+created.ID, nil, other, "", ""); w.Code != 403 {
			t.Fatalf("cross department detail=%d", w.Code)
		}
		if w := workflowRequest(router, "POST", "/api/v1/applications/"+created.ID+"/approve", []byte("{}"), other, otherPrincipal.CSRFToken, "other-approve"); w.Code != 403 {
			t.Fatalf("cross department approval=%d", w.Code)
		}
		if w := workflowRequest(router, "POST", "/api/v1/applications/"+created.ID+"/approve", []byte("{}"), consumer, consumerPrincipal.CSRFToken, "self-approve"); w.Code != 403 || !strings.Contains(w.Body.String(), "SELF_APPROVAL_DENIED") {
			t.Fatalf("self approval=%d", w.Code)
		}
	})
	t.Run("concurrent_approval_has_one_grant", func(t *testing.T) {
		results := make(chan int, 2)
		var wg sync.WaitGroup
		for _, key := range []string{"approve-a", "approve-b"} {
			wg.Add(1)
			go func(key string) {
				defer wg.Done()
				w := workflowRequest(router, "POST", "/api/v1/applications/"+created.ID+"/approve", []byte("{}"), provider, providerPrincipal.CSRFToken, key)
				results <- w.Code
			}(key)
		}
		wg.Wait()
		close(results)
		success, conflicts := 0, 0
		for code := range results {
			if code == 200 {
				success++
			} else if code == 409 {
				conflicts++
			} else {
				t.Fatalf("approval=%d", code)
			}
		}
		if success != 1 || conflicts != 1 {
			t.Fatalf("approval outcomes=%d/%d", success, conflicts)
		}
		var count int64
		if e := db.Model(&workflow.Record{}).Where("kind='grant' AND parent_id=?", created.ID).Count(&count).Error; e != nil || count != 1 {
			t.Fatalf("grant uniqueness %d %v", count, e)
		}
	})
	var grant workflow.Record
	if e := db.Where("kind='grant' AND parent_id=?", created.ID).Take(&grant).Error; e != nil {
		t.Fatal(e)
	}
	t.Run("raw_layer_and_fields_do_not_inherit", func(t *testing.T) {
		for _, path := range []string{"/delivery/v1/raw/" + objectID + "/records?contract_version=1", "/delivery/v1/datasets/" + datasetID + "/records?contract_version=1&fields=unauthorized"} {
			w := workflowRequest(router, "GET", path, nil, consumer, "", "")
			if w.Code != 403 {
				t.Fatalf("scope denial=%d %s", w.Code, w.Body.String())
			}
		}
	})
	t.Run("expiry_and_revocation_are_immediate", func(t *testing.T) {
		originalBody := append([]byte(nil), grant.Body...)
		var grantData workflow.GrantData
		if err := json.Unmarshal(grant.Body, &grantData); err != nil {
			t.Fatal(err)
		}
		grantData.Scope.ExpiresAt = time.Now().Add(-time.Minute)
		grant.Body = jsonBody(t, grantData)
		if err := db.Save(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if w := workflowRequest(router, "GET", "/delivery/v1/datasets/"+datasetID+"/records?contract_version=1", nil, consumer, "", ""); w.Code != 403 {
			t.Fatalf("expired delivery=%d", w.Code)
		}
		grant.Body = originalBody
		if err := db.Save(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if w := workflowRequest(router, "POST", "/api/v1/grants/"+grant.ID+"/revoke", []byte("{}"), consumer, consumerPrincipal.CSRFToken, "bad-revoke"); w.Code != 403 {
			t.Fatalf("grantee revoke=%d", w.Code)
		}
		w := workflowRequest(router, "POST", "/api/v1/grants/"+grant.ID+"/revoke", []byte("{}"), provider, providerPrincipal.CSRFToken, "revoke")
		if w.Code != 200 {
			t.Fatalf("revoke=%d %s", w.Code, w.Body.String())
		}
		w = workflowRequest(router, "GET", "/delivery/v1/datasets/"+datasetID+"/records?contract_version=1", nil, consumer, "", "")
		if w.Code != 403 {
			t.Fatalf("revoked delivery=%d", w.Code)
		}
	})
	t.Run("list_and_publication_boundaries", func(t *testing.T) {
		for _, path := range []string{"/api/v1/datasets?page_size=101", "/api/v1/datasets?sort_by=id;DROP%20TABLE%20users"} {
			if w := workflowRequest(router, "GET", path, nil, consumer, "", ""); w.Code != 400 {
				t.Fatalf("list boundary=%d", w.Code)
			}
		}
		if w := workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/withdraw", []byte("{}"), consumer, consumerPrincipal.CSRFToken, "bad-withdraw"); w.Code != 403 {
			t.Fatalf("consumer withdrawal=%d", w.Code)
		}
		w := workflowRequest(router, "POST", "/api/v1/datasets/"+datasetID+"/withdraw", []byte("{}"), provider, providerPrincipal.CSRFToken, "withdraw")
		if w.Code != 200 {
			t.Fatalf("withdraw=%d", w.Code)
		}
		w = workflowRequest(router, "GET", "/api/public/assets/"+datasetID, nil, nil, "", "")
		if w.Code != 404 {
			t.Fatalf("withdrawn public asset=%d", w.Code)
		}
	})
	_ = consumerID
}

func jsonEquivalent(a, b []byte) bool {
	var left, right interface{}
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	aa, _ := json.Marshal(left)
	bb, _ := json.Marshal(right)
	return bytes.Equal(aa, bb)
}
