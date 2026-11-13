package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
)

func TestAssetProductLifecycle(t *testing.T) {
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "product_owner_", "product-dept", []string{"provider"})
	_, reviewerName := businessUser(t, service, "product_reviewer_", "product-dept", []string{"governor"})
	_, outsiderName := businessUser(t, service, "product_outsider_", "other-product-dept", []string{"governor"})
	consumerName := fmt.Sprintf("product_consumer_%d", time.Now().UnixNano())
	if _, e := service.CreateUser(context.Background(), identity.NewUser{Username: consumerName, Password: password, DepartmentID: "consumer-dept", Platforms: []string{"assets"}, Roles: []string{"consumer"}}); e != nil {
		t.Fatal(e)
	}
	owner, op := login(t, router, ownerName)
	reviewer, rp := login(t, router, reviewerName)
	outsider, xp := login(t, router, outsiderName)
	consumer, cp := login(t, router, consumerName)
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "secret", LogicalType: "string", Sensitive: true}}
	ref := workflow.Resource{Type: "raw_object", ID: "product-source-" + ownerID, Version: 1}
	if e := db.Create(&workflow.Record{Kind: "object", ID: ref.ID, DepartmentID: "product-dept", OwnerID: ownerID, State: "available", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: ref.ID, SourceID: "private-host", QualifiedName: "private.schema", SchemaVersion: 1, Fields: fields})}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot := seedSharingFixture(t, db, ownerID, "product-dept", ref, fields)
	read := func(path string, cookie *http.Cookie, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "GET", path, nil, cookie, "", "")
		if w.Code != want {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	post := func(path string, body interface{}, cookie *http.Cookie, p identity.Principal, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != want {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	def := map[string]interface{}{"name": "客户数据产品", "description": "已共享的客户标识", "domain": nil, "terms": []interface{}{}, "resources": []workflow.Resource{ref}, "contract": map[string]interface{}{"freshness_seconds": 3600, "max_failure_rate": 0.01}, "usage_restrictions": "限内部使用"}
	post("/api/v1/assets/products", def, consumer, cp, 403)
	product := post("/api/v1/assets/products", def, owner, op, 201)
	id := product["id"].(string)
	base := "/api/v1/assets/products/" + id
	if product["state"] != "draft" || product["version"] != float64(1) {
		t.Fatal(product)
	}
	read(base, consumer, 404)
	if list := read("/api/v1/assets/products?search=客户数据产品", consumer, 200); list["total"] != float64(0) {
		t.Fatal("draft leaked")
	}
	read("/api/v1/assets/products?manage=true", consumer, 403)
	post(base+"/submit", map[string]interface{}{"expected_version": 1, "reason": "提交产品审查"}, owner, op, 200)
	post(base+"/revisions", map[string]interface{}{"expected_version": 2, "definition": def}, owner, op, 409)
	approve := map[string]interface{}{"expected_version": 2, "reason": "共享版本与业务条件已核对", "valid_until": time.Now().Add(24 * time.Hour).UTC()}
	post(base+"/approve", approve, owner, op, 403)
	post(base+"/approve", approve, outsider, xp, 403)
	expiredApproval := map[string]interface{}{"expected_version": 2, "reason": "过期认证不能发布", "valid_until": time.Now().Add(-time.Hour).UTC()}
	post(base+"/approve", expiredApproval, reviewer, rp, 400)
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "suspended").Error; e != nil {
		t.Fatal(e)
	}
	post(base+"/approve", approve, reviewer, rp, 409)
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	published := post(base+"/approve", approve, reviewer, rp, 200)
	if published["state"] != "published" || published["certification_status"] != "valid" {
		t.Fatal(published)
	}
	if e := db.Table("asset_product_revisions").Where("id=?", published["revision_id"]).Update("valid_until", time.Now().Add(-time.Hour)).Error; e != nil {
		t.Fatal(e)
	}
	if expired := read(base, consumer, 200); expired["certification_status"] != "expired" {
		t.Fatal(expired)
	}
	if e := db.Table("asset_product_revisions").Where("id=?", published["revision_id"]).Update("valid_until", time.Now().Add(time.Hour)).Error; e != nil {
		t.Fatal(e)
	}
	public := read(base, consumer, 200)
	text := string(jsonBody(t, public))
	if strings.Contains(text, "private-host") || strings.Contains(text, "private.schema") {
		t.Fatal("internal source leaked")
	}
	post(base+"/check", map[string]int{"expected_version": 2}, consumer, cp, 409)
	checked := post(base+"/check", map[string]int{"expected_version": 3}, consumer, cp, 201)
	if checked["state"] != "unknown" {
		t.Fatalf("unmeasured freshness/quality must be unknown: %v", checked)
	}
	if list := read(base+"/checks", owner, 200); list["total"] != float64(0) {
		t.Fatal("another user's check exposed")
	}
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "suspended").Error; e != nil {
		t.Fatal(e)
	}
	checked = post(base+"/check", map[string]int{"expected_version": 3}, consumer, cp, 201)
	if checked["state"] != "violated" {
		t.Fatal(checked)
	}
	post(base+"/check", map[string]int{"expected_version": 3}, consumer, cp, 201)
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	notices := read("/api/v1/assets/notices", consumer, 200)
	if notices["total"] != float64(1) {
		t.Fatal("identical violation notices duplicated", notices)
	}
	// Editing a draft must not replace the version already visible to consumers.
	def["name"] = "内部未审查新名称"
	revised := post(base+"/revisions", map[string]interface{}{"expected_version": 3, "definition": def}, owner, op, 200)
	if revised["version"] != float64(4) {
		t.Fatal(revised)
	}
	public = read(base, consumer, 200)
	if public["definition"].(map[string]interface{})["name"] != "客户数据产品" {
		t.Fatal("unreviewed draft replaced published product")
	}
	post(base+"/submit", map[string]interface{}{"expected_version": 3, "reason": "旧版本提交"}, owner, op, 409)
	post(base+"/submit", map[string]interface{}{"expected_version": 4, "reason": "提交下一版"}, owner, op, 200)
	post(base+"/deprecate", map[string]interface{}{"expected_version": 5, "reason": "不能覆盖待审状态", "replacement_id": ""}, owner, op, 409)
	post(base+"/reject", map[string]interface{}{"expected_version": 5, "reason": "本轮暂不更新"}, reviewer, rp, 200)
	// Deprecation does not imply data grant revocation; retirement removes public discovery.
	post(base+"/deprecate", map[string]interface{}{"expected_version": 6, "reason": "准备替换", "replacement_id": id}, owner, op, 400)
	post(base+"/deprecate", map[string]interface{}{"expected_version": 6, "reason": "停止新增使用", "replacement_id": ""}, owner, op, 200)
	if public = read(base, consumer, 200); public["state"] != "deprecated" {
		t.Fatal(public)
	}
	post(base+"/retire", map[string]interface{}{"expected_version": 7, "reason": "完成停用"}, owner, op, 200)
	read(base, consumer, 404)
	read(base+"?manage=true", owner, 200)
	read("/api/v1/assets/products?manage=unexpected", owner, 400)
	read("/api/v1/assets/products", nil, 401)
}

func TestAssetUsagePersonalScope(t *testing.T) {
	db, service, router := fixture(t)
	id, name := businessUser(t, service, "usage_owner_", "usage-dept", []string{"consumer"})
	otherID, otherName := businessUser(t, service, "usage_peer_", "usage-dept", []string{"consumer"})
	cookie, _ := login(t, router, name)
	other, _ := login(t, router, otherName)
	for _, v := range []identity.AuditRecord{{ActorID: &id, Action: "deliver_raw_object", ObjectID: "resource", Result: "success"}, {ActorID: &id, Action: "invoke_api", ObjectID: "resource", Result: "success"}, {ActorID: &id, Action: "deliver_raw_object", ObjectID: "resource", Result: "failed"}, {ActorID: &otherID, Action: "deliver_raw_object", ObjectID: "resource", Result: "success"}} {
		if e := db.Create(&v).Error; e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []struct {
		cookie *http.Cookie
		want   float64
	}{{cookie, 2}, {other, 1}} {
		w := workflowRequest(router, "GET", "/api/v1/assets/usage?days=30", nil, v.cookie, "", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var data map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
			t.Fatal(e)
		}
		if data["total"] != v.want || data["cost"] != nil || data["value"] != nil {
			t.Fatal(data)
		}
	}
	if w := workflowRequest(router, "GET", "/api/v1/assets/usage?days=91", nil, cookie, "", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestAssetProductPublishedSemantics(t *testing.T) {
	db, service, router := fixture(t)
	ownerID, name := businessUser(t, service, "semantic_owner_", "semantic-dept", []string{"provider", "governor"})
	cookie, _ := login(t, router, name)
	id := "semantic-domain-" + ownerID
	head := workflow.GovernanceDefinitionRecord{ID: id, Kind: "domain", DepartmentID: "semantic-dept", LatestVersion: 2}
	if e := db.Create(&head).Error; e != nil {
		t.Fatal(e)
	}
	for _, v := range []workflow.GovernanceVersion{{DefinitionID: id, Version: 1, State: "published", CreatedBy: ownerID, Definition: jsonBody(t, workflow.GovernanceDefinition{Name: "已发布业务域", Description: "可公开的业务说明", OwnerID: ownerID, StewardID: ownerID})}, {DefinitionID: id, Version: 2, State: "draft", CreatedBy: ownerID, Definition: jsonBody(t, workflow.GovernanceDefinition{Name: "内部未审查名称", Description: "未发布", OwnerID: ownerID, StewardID: ownerID})}} {
		if e := db.Create(&v).Error; e != nil {
			t.Fatal(e)
		}
	}
	w := workflowRequest(router, "GET", "/api/v1/assets/product-semantics?kind=domain", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "已发布业务域") || strings.Contains(w.Body.String(), "内部未审查名称") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A consumer cannot enumerate internal published semantics independently of a reviewed product.
	consumerName := fmt.Sprintf("semantic_consumer_%d", time.Now().UnixNano())
	if _, e := service.CreateUser(context.Background(), identity.NewUser{Username: consumerName, Password: password, DepartmentID: "semantic-dept", Platforms: []string{"assets"}, Roles: []string{"consumer"}}); e != nil {
		t.Fatal(e)
	}
	consumer, _ := login(t, router, consumerName)
	w = workflowRequest(router, "GET", "/api/v1/assets/product-semantics?kind=domain", nil, consumer, "", "")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	// Published versions from a different department are omitted.
	_, otherName := businessUser(t, service, "semantic_peer_", "other-semantic-dept", []string{"provider"})
	other, _ := login(t, router, otherName)
	w = workflowRequest(router, "GET", "/api/v1/assets/product-semantics?kind=domain", nil, other, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "已发布业务域") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAssetProductUnavailableAPIApproval(t *testing.T) {
	db, service, router := fixture(t)
	ownerID, name := businessUser(t, service, "api_product_owner_", "api-product-dept", []string{"provider"})
	_, reviewerName := businessUser(t, service, "api_product_review_", "api-product-dept", []string{"governor"})
	cookie, p := login(t, router, name)
	reviewer, rp := login(t, router, reviewerName)
	fields := []workflow.Field{{Name: "id", LogicalType: "integer"}}
	ref := workflow.Resource{Type: "api_resource", ID: "api-product-" + ownerID, Version: 1}
	managed := workflow.ManagedResource{Name: "客户查询API", DataLayer: "raw_object", Fields: fields, Adapter: "api", Config: jsonBody(t, workflow.APIConfig{URL: "https://example.invalid/query", Method: "GET", QueryOnly: true})}
	if e := db.Create(&workflow.Record{Kind: "api_resource", ID: ref.ID, DepartmentID: p.DepartmentID, OwnerID: ownerID, State: "ready", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot := seedSharingFixture(t, db, ownerID, p.DepartmentID, ref, fields)
	var directory workflow.Record
	if e := db.Where("kind='directory_revision' AND id=?", snapshot.DirectoryRevisionID).Take(&directory).Error; e != nil {
		t.Fatal(e)
	}
	var definition workflow.DirectoryRevisionData
	if e := json.Unmarshal(directory.Body, &definition); e != nil {
		t.Fatal(e)
	}
	definition.Definition.Format = "api"
	definition.Definition.DataLayer = "raw_object"
	definition.Definition.SharingMethod = "invoke"
	if e := db.Model(&directory).Update("body", jsonBody(t, definition)).Error; e != nil {
		t.Fatal(e)
	}
	input := map[string]interface{}{"name": "API数据产品", "description": "不能认证已知不可交付的来源", "resources": []workflow.Resource{ref}, "contract": map[string]interface{}{"freshness_seconds": 0, "max_failure_rate": nil}}
	post := func(path string, v interface{}, cookie *http.Cookie, p identity.Principal, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, v), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != want {
			t.Fatalf("%s got %d want %d %s", path, w.Code, want, w.Body.String())
		}
		out := map[string]interface{}{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	created := post("/api/v1/assets/products", input, cookie, p, 201)
	base := "/api/v1/assets/products/" + created["id"].(string)
	post(base+"/submit", map[string]interface{}{"expected_version": 1, "reason": "申请审核"}, cookie, p, 200)
	// The API remains shared, but its managed source becomes unavailable after submission.
	managed.Config = jsonBody(t, workflow.APIConfig{DataService: &workflow.DataServiceQuery{DatasetID: "missing-source", Mode: "snapshot", SourceFields: fields}})
	if e := db.Model(&workflow.Record{}).Where("kind='api_resource' AND id=?", ref.ID).Update("body", jsonBody(t, managed)).Error; e != nil {
		t.Fatal(e)
	}
	post(base+"/approve", map[string]interface{}{"expected_version": 2, "reason": "不能将警告当作成功证据", "valid_until": time.Now().Add(time.Hour)}, reviewer, rp, 409)
	if w := workflowRequest(router, "GET", base, nil, cookie, "", ""); w.Code != 404 {
		t.Fatal("unavailable API product was published", w.Code, w.Body.String())
	}
}
