package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
)

func TestDataSecurityLifecycleAndDelivery(t *testing.T) {
	t.Setenv("SECURITY_WATERMARK_KEY_ID", "test-v1")
	t.Setenv("SECURITY_WATERMARK_KEYS", `{"test-v1":"`+strings.Repeat("ab", 32)+`"}`)
	db, service, router := fixture(t)
	adminID, adminName := businessUser(t, service, "security_admin_", "security", []string{"admin"})
	_, reviewerName := businessUser(t, service, "security_review_", "security", []string{"admin"})
	consumerID, consumerName := businessUser(t, service, "security_consume_", "consumer", []string{"consumer"})
	_, outsiderName := businessUser(t, service, "security_outside_", "other", []string{"consumer"})
	_, auditorName := businessUser(t, service, "security_audit_", "audit", []string{"auditor"})
	admin, ap := login(t, router, adminName)
	reviewer, rp := login(t, router, reviewerName)
	consumer, cp := login(t, router, consumerName)
	outsider, xp := login(t, router, outsiderName)
	auditor, aup := login(t, router, auditorName)
	post := func(path string, body interface{}, cookie *http.Cookie, p identity.Principal, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	get := func(path string, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := workflowRequest(router, "GET", path, nil, cookie, "", "")
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":9007199254740993,"phone":"private"}]`))
	}))
	defer upstream.Close()
	t.Setenv("API_ALLOWED_TARGETS", fmt.Sprintf(`[{"origin":%q,"addresses":["127.0.0.1"]}]`, upstream.URL))
	ref := workflow.Resource{Type: "api_resource", ID: "security-api-" + adminID, Version: 1}
	fields := []workflow.Field{{Name: "id", LogicalType: "integer"}, {Name: "phone", LogicalType: "string"}}
	managed := workflow.ManagedResource{Name: "安全测试接口", DataLayer: "raw_object", Fields: fields, Adapter: "api", Config: jsonBody(t, workflow.APIConfig{URL: upstream.URL, Method: "GET", AuthType: "none", QueryOnly: true, Parameters: []workflow.APIParameter{}, ResponseFields: map[string]string{"id": "id", "phone": "phone"}})}
	if e := db.Create(&workflow.Record{Kind: ref.Type, ID: ref.ID, OwnerID: adminID, DepartmentID: "security", State: "ready", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot := seedSharingFixture(t, db, adminID, "security", ref, fields)
	var revision workflow.Record
	if e := db.Where("id=?", snapshot.DirectoryRevisionID).Take(&revision).Error; e != nil {
		t.Fatal(e)
	}
	var directory workflow.DirectoryRevisionData
	json.Unmarshal(revision.Body, &directory)
	directory.Definition.DataLayer = "raw_object"
	directory.Definition.Format = "api"
	directory.Definition.SharingMethod = "invoke"
	if e := db.Model(&revision).Update("body", jsonBody(t, directory)).Error; e != nil {
		t.Fatal(e)
	}
	grant := workflow.GrantData{Snapshot: &snapshot, ApplicationID: "security-application-" + consumerID, GranteeUserID: consumerID, ApprovedBy: adminID, ApprovedAt: time.Now(), Scope: workflow.GrantRequest{Resource: ref, DepartmentID: cp.DepartmentID, Fields: []string{"id", "phone"}, Purpose: "安全测试", ExpiresAt: time.Now().Add(time.Hour), Materials: &workflow.ApplicationMaterials{Method: "invoke"}}}
	grantID := "security-grant-" + consumerID
	if e := db.Create(&workflow.Record{Kind: "grant", ID: grantID, ParentID: grant.ApplicationID, OwnerID: consumerID, DepartmentID: cp.DepartmentID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; e != nil {
		t.Fatal(e)
	}
	invoke := func(cookie *http.Cookie, p identity.Principal, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := workflowRequest(router, "POST", "/delivery/v1/apis/"+ref.ID+"/invoke", jsonBody(t, map[string]interface{}{"contract_version": 1, "parameters": map[string]string{}, "fields": []string{"id", "phone"}}), cookie, p.CSRFToken, "")
		if w.Code != want {
			t.Fatalf("invoke: %d %s", w.Code, w.Body.String())
		}
		return w
	}
	control := func(kind string, d workflow.SecurityDefinition) map[string]interface{} {
		t.Helper()
		return post("/api/v1/security/controls", map[string]interface{}{"kind": kind, "definition": d}, admin, ap, 201)
	}
	publish := func(row map[string]interface{}) map[string]interface{} {
		t.Helper()
		base := "/api/v1/security/controls/" + row["id"].(string)
		post(base+"/submit", map[string]interface{}{"expected_version": row["generation"], "reason": "提交独立复核"}, admin, ap, 200)
		post(base+"/approve", map[string]interface{}{"expected_version": 2, "reason": "不得自批"}, admin, ap, 403)
		return post(base+"/approve", map[string]interface{}{"expected_version": 2, "reason": "已独立核对"}, reviewer, rp, 200)
	}
	get("/api/v1/security/overview", consumer, 403)
	get("/api/v1/security/overview", auditor, 200)
	post("/api/v1/security/controls", map[string]interface{}{"kind": "watermark", "definition": workflow.SecurityDefinition{Name: "只读测试", Text: "test"}}, auditor, aup, 403)
	template := publish(control("watermark", workflow.SecurityDefinition{Name: "受控水印", Text: "{recipient} · {department} · {resource} · {delivery_id}"}))
	policyDefinition := workflow.SecurityDefinition{Name: "接口脱敏及留痕", Resource: &ref, Actions: []string{"invoke"}, MaskFields: []string{"phone"}, RequireProof: true, Visible: true, Template: &workflow.GovernanceRef{ID: template["id"].(string), Version: 1}}
	policy := control("policy", policyDefinition)
	if !strings.Contains(invoke(consumer, cp, 200).Body.String(), "private") {
		t.Fatal("draft changed delivery")
	}
	policy = publish(policy)
	before := calls.Load()
	invoke(outsider, xp, 403)
	if calls.Load() != before {
		t.Fatal("policy granted unauthorized upstream access")
	}
	result := invoke(consumer, cp, 200)
	if strings.Contains(result.Body.String(), "private") || !strings.Contains(result.Body.String(), "9007199254740993") {
		t.Fatal("mask or numeric fidelity", result.Body.String())
	}
	proofID := result.Header().Get("X-DataMesh-Delivery-ID")
	if proofID == "" {
		t.Fatal("proof header missing")
	}
	mark, e := url.QueryUnescape(result.Header().Get("X-DataMesh-Watermark"))
	if e != nil || !strings.Contains(mark, cp.Username) {
		t.Fatal("recipient mark missing")
	}
	request := httptest.NewRequest("POST", "https://localhost:18443/delivery/v1/apis/"+ref.ID+"/invoke", strings.NewReader(string(jsonBody(t, map[string]interface{}{"contract_version": 1, "parameters": map[string]string{}, "fields": []string{"id", "phone"}}))))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", cp.CSRFToken)
	request.AddCookie(consumer)
	interrupted := &securityInterruptedWriter{ResponseRecorder: httptest.NewRecorder()}
	router.ServeHTTP(interrupted, request)
	var interruptedProof workflow.SecurityDeliveryProof
	if e := db.Where("id=?", interrupted.Header().Get("X-DataMesh-Delivery-ID")).Take(&interruptedProof).Error; e != nil || interruptedProof.State != "interrupted" {
		t.Fatal("transmission interruption was not persisted", e)
	}
	if e := db.Exec("CREATE FUNCTION reject_security_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='security_delivery_prepared' THEN RAISE EXCEPTION 'test audit unavailable'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_security_delivery_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_security_delivery_audit()").Error; e != nil {
		t.Fatal(e)
	}
	unavailable := invoke(consumer, cp, 503)
	if unavailable.Header().Get("X-DataMesh-Delivery-ID") != "" || strings.Contains(unavailable.Body.String(), "private") {
		t.Fatal("audit failure exposed content")
	}
	if e := db.Exec("DROP TRIGGER reject_security_delivery_audit ON audit_records; DROP FUNCTION reject_security_delivery_audit()").Error; e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(result.Body.Bytes())
	digest := hex.EncodeToString(sum[:])
	proofPath := "/api/v1/security/delivery-proofs/" + proofID
	get(proofPath, outsider, 403)
	get(proofPath, auditor, 200)
	verified := post(proofPath+"/verify", map[string]string{"sha256": digest}, auditor, aup, 200)
	if verified["result"] != "matched" || verified["delivery_state"] != "completed" {
		t.Fatal(verified)
	}
	if v := post(proofPath+"/verify", map[string]string{"sha256": strings.Repeat("0", 64)}, consumer, cp, 200); v["result"] != "content_changed" {
		t.Fatal(v)
	}
	t.Setenv("SECURITY_WATERMARK_KEYS", `{"test-v2":"`+strings.Repeat("cd", 32)+`"}`)
	if v := post(proofPath+"/verify", map[string]string{"sha256": digest}, consumer, cp, 200); v["result"] != "unverifiable" {
		t.Fatal(v)
	}
	invoke(consumer, cp, 503)
	t.Setenv("SECURITY_WATERMARK_KEYS", `{"test-v1":"`+strings.Repeat("ab", 32)+`","test-v2":"`+strings.Repeat("cd", 32)+`"}`)
	t.Setenv("SECURITY_WATERMARK_KEY_ID", "test-v2")
	if v := post(proofPath+"/verify", map[string]string{"sha256": digest}, consumer, cp, 200); v["result"] != "matched" {
		t.Fatal(v)
	}
	if invoke(admin, ap, 200).Header().Get("X-DataMesh-Delivery-ID") == proofID {
		t.Fatal("cross-recipient proof reused")
	}
	base := "/api/v1/security/controls/" + policy["id"].(string)
	policyDefinition.Deny = true
	newVersion := post(base+"/versions", map[string]interface{}{"expected_version": 3, "definition": policyDefinition}, admin, ap, 200)
	invoke(consumer, cp, 200)
	if newVersion["version"] != float64(2) {
		t.Fatal(newVersion)
	}
	post(base+"/submit", map[string]interface{}{"expected_version": 4, "reason": "收紧交付"}, admin, ap, 200)
	post(base+"/approve", map[string]interface{}{"expected_version": 5, "reason": "独立复核收紧"}, reviewer, rp, 200)
	before = calls.Load()
	invoke(consumer, cp, 403)
	if before != calls.Load() {
		t.Fatal("denied policy reached upstream")
	}
	post(base+"/retire", map[string]interface{}{"expected_version": 6, "reason": "结束测试策略"}, admin, ap, 200)
	incident := post("/api/v1/security/incidents", map[string]interface{}{"resource": ref, "title": "核实风险", "reason": "隔离测试证据"}, admin, ap, 201)
	incidentBase := "/api/v1/security/incidents/" + incident["id"].(string)
	post(incidentBase+"/freeze", map[string]interface{}{"expected_version": 1, "reason": "保护对应资源版本"}, admin, ap, 200)
	invoke(admin, ap, 403)
	post(incidentBase+"/resolve", map[string]interface{}{"expected_version": 2, "reason": "不得自行恢复"}, admin, ap, 403)
	post(incidentBase+"/resolve", map[string]interface{}{"expected_version": 2, "reason": "独立复核通过"}, reviewer, rp, 200)
	invoke(consumer, cp, 200)
	scan := post("/api/v1/security/scans", map[string]interface{}{"resource": ref}, admin, ap, 201)
	if strings.Contains(string(jsonBody(t, scan)), "private") || len(scan["candidates"].([]interface{})) != 1 {
		t.Fatal(scan)
	}
	// Published protection never extends a revoked grant.
	if e := db.Model(&workflow.Record{}).Where("id=?", grantID).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	invoke(consumer, cp, 403)
}

type securityInterruptedWriter struct{ *httptest.ResponseRecorder }

func (w *securityInterruptedWriter) Write(b []byte) (int, error) {
	return 0, errors.New("simulated transmission interruption")
}

func TestDataSecurityScopedClassification(t *testing.T) {
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "label_owner_", "label-dept", []string{"provider"})
	reviewerID, reviewerName := businessUser(t, service, "label_review_", "label-dept", []string{"provider"})
	seedAssignment(t, db, ownerID, "label-dept", "manage_resource")
	seedAssignment(t, db, ownerID, "label-dept", "share_approve")
	seedAssignment(t, db, reviewerID, "label-dept", "share_approve")
	owner, op := login(t, router, ownerName)
	reviewer, rp := login(t, router, reviewerName)
	ref := workflow.Resource{Type: "raw_object", ID: "label-source-" + ownerID, Version: 1}
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "phone", LogicalType: "string"}}
	if e := db.Create(&workflow.Record{Kind: "object", ID: ref.ID, OwnerID: ownerID, DepartmentID: "label-dept", State: "ready", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: ref.ID, SourceID: "private-source", QualifiedName: "private.table", SchemaVersion: 1, Fields: fields})}).Error; e != nil {
		t.Fatal(e)
	}
	seedSharingFixture(t, db, ownerID, "label-dept", ref, fields)
	if w := workflowRequest(router, "GET", "/health/ready", nil, nil, "", ""); w.Code != 200 {
		t.Fatalf("security schema not ready: %d", w.Code)
	}
	post := func(path string, body interface{}, cookie *http.Cookie, p identity.Principal, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	definition := workflow.SecurityDefinition{Name: "手机号保护", Resource: &ref, Field: "phone", Level: "sensitive"}
	created := post("/api/v1/resource-security/controls", map[string]interface{}{"kind": "classification", "definition": definition}, owner, op, 201)
	post("/api/v1/resource-security/controls", map[string]interface{}{"kind": "policy", "definition": definition}, owner, op, 403)
	base := "/api/v1/resource-security/controls/" + created["id"].(string)
	post(base+"/submit", map[string]interface{}{"expected_version": 1, "reason": "请独立复核"}, owner, op, 200)
	post(base+"/approve", map[string]interface{}{"expected_version": 2, "reason": "作者不可自批"}, owner, op, 403)
	post(base+"/approve", map[string]interface{}{"expected_version": 2, "reason": "已核对字段与用途"}, reviewer, rp, 200)
	definition.Level = "public"
	post(base+"/versions", map[string]interface{}{"expected_version": 3, "definition": definition}, owner, op, 200)
	contextPath := "/api/v1/assets/security-context?resource_type=raw_object&resource_id=" + ref.ID + "&contract_version=1"
	w := workflowRequest(router, "GET", contextPath, nil, owner, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sensitive"`) || !strings.Contains(w.Body.String(), `"phone"`) {
		t.Fatalf("new draft weakened effective label: %d %s", w.Code, w.Body.String())
	}
	w = workflowRequest(router, "GET", base+"/versions", nil, reviewer, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sensitive"`) {
		t.Fatalf("resource reviewer could not inspect immutable history: %d", w.Code)
	}
	derived := workflow.Resource{Type: "governed_dataset", ID: "label-derived-" + ownerID, Version: 1}
	data := workflow.DatasetData{Definition: workflow.DatasetInput{Name: "字段更名成果", SourceObjectID: ref.ID, Fields: []workflow.Field{{Name: "phone_alias", LogicalType: "string"}}}, QualityStatus: "passed", Quality: json.RawMessage("null"), LatestWatermark: json.RawMessage("null")}
	if e := db.Create(&workflow.Record{Kind: "dataset", ID: derived.ID, OwnerID: ownerID, DepartmentID: "label-dept", State: "published", Version: 1, Body: jsonBody(t, data)}).Error; e != nil {
		t.Fatal(e)
	}
	seedSharingFixture(t, db, ownerID, "label-dept", derived, data.Definition.Fields)
	w = workflowRequest(router, "GET", "/api/v1/assets/security-context?resource_type=governed_dataset&resource_id="+derived.ID+"&contract_version=1", nil, owner, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"phone_alias"`) {
		t.Fatalf("derived field lost source protection: %d %s", w.Code, w.Body.String())
	}
	if e := db.Model(&workflow.Record{}).Where("kind='object' AND id=?", ref.ID).Updates(map[string]interface{}{"version": 2, "body": jsonBody(t, workflow.Object{ObjectID: ref.ID, SourceID: "private-source", QualifiedName: "private.table", SchemaVersion: 2, Fields: fields})}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("UPDATE business_records SET body=jsonb_set(body,'{resource,contract_version}','2'::jsonb) WHERE kind='mount_revision' AND id=?", "mr-"+ref.ID).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("UPDATE business_records SET body=jsonb_set(body,'{resource_key}',?::jsonb) WHERE kind='mount' AND id=?", string(jsonBody(t, "raw_object:"+ref.ID+":2")), "mount-"+ref.ID).Error; e != nil {
		t.Fatal(e)
	}
	newContext := strings.Replace(contextPath, "contract_version=1", "contract_version=2", 1)
	w = workflowRequest(router, "GET", newContext, nil, owner, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pending_review"`) {
		t.Fatalf("new version silently dropped protection: %d %s", w.Code, w.Body.String())
	}
	ref.Version = 2
	definition.Resource = &ref
	definition.Level = "sensitive"
	post(base+"/versions", map[string]interface{}{"expected_version": 4, "definition": definition}, owner, op, 200)
	post(base+"/submit", map[string]interface{}{"expected_version": 5, "reason": "新版本字段复核"}, owner, op, 200)
	post(base+"/approve", map[string]interface{}{"expected_version": 6, "reason": "独立核对新版本"}, reviewer, rp, 200)
	w = workflowRequest(router, "GET", newContext, nil, owner, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"blocked":true`) {
		t.Fatalf("reviewed new version remained blocked: %d %s", w.Code, w.Body.String())
	}
	if w = workflowRequest(router, "GET", "/api/v1/security/controls?kind=policy", nil, owner, "", ""); w.Code != 403 {
		t.Fatal("resource ownership widened global security access")
	}
}
