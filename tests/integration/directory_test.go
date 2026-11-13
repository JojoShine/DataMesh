package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"gorm.io/gorm"
)

func seedAssignment(t *testing.T, db *gorm.DB, user, dept, cap string) {
	t.Helper()
	id := fmt.Sprintf("scope-%s-%s", user, cap)
	v := workflow.ResourceAssignment{UserID: user, DepartmentID: dept, ResourceID: "*", Capability: cap, Reason: "隔离集成测试", ExpiresAt: time.Now().Add(time.Hour)}
	if e := db.Create(&workflow.Record{Kind: "assignment", ID: id, DepartmentID: dept, OwnerID: user, State: "active", Version: 1, Body: jsonBody(t, v)}).Error; e != nil {
		t.Fatal(e)
	}
}

// These snapshots test authorization transactions only; no runtime success is claimed.
func seedSharingFixture(t *testing.T, db *gorm.DB, owner, dept string, ref workflow.Resource, fields []workflow.Field) workflow.SharingSnapshot {
	t.Helper()
	stem := ref.ID
	di, dr, mi, mr := "dir-"+stem, "dr-"+stem, "mount-"+stem, "mr-"+stem
	items := []workflow.DirectoryItem{}
	mapping := map[string]string{}
	for _, f := range fields {
		items = append(items, workflow.DirectoryItem{Name: f.Name, BusinessName: f.Name, LogicalType: f.LogicalType, Sensitive: f.Sensitive})
		mapping[f.Name] = f.Name
	}
	dictionaryID := "dict-" + stem
	def := workflow.DirectoryDefinition{ResourceCode: "TEST-" + stem, DictionaryID: dictionaryID, ProviderName: dept, ProviderCode: "TEST", ResponsibleUserID: owner, UpdateFrequency: "每日", UsageRestrictions: "本地隔离验证", SecurityClassification: "一般数据（测试）", DepartmentApprovalRef: "LOCAL-TEST", AssessmentRef: "LOCAL-TEST", ClassificationPath: "department/test", Name: "隔离目录", Description: "权限事务测试，不代表正式编目或运行校验", Classification: "department", Format: "database", DataLayer: ref.Type, Items: items, SharingType: "conditional", SharingConditions: "测试", SharingMethod: "query", OpenToPublic: true, OpenConditions: "仅隔离测试摘要"}
	for _, r := range []workflow.Record{
		{Kind: "directory_dictionary", ID: dictionaryID, DepartmentID: dept, OwnerID: owner, State: "active", Version: 1, Body: jsonBody(t, workflow.DirectoryDictionary{Name: "隔离测试字典", Version: "test", Source: "local-test", Verified: true, TestOnly: true, CodePattern: "^TEST-[A-Za-z0-9_-]+$", Classifications: []string{"department/test"}, VerificationRef: "test"})},
		{Kind: "directory", ID: di, DepartmentID: dept, OwnerID: owner, State: "published", Version: 1, Body: jsonBody(t, workflow.DirectoryData{DraftID: dr, PublishedID: dr})},
		{Kind: "directory_revision", ID: dr, DepartmentID: dept, OwnerID: owner, ParentID: di, State: "approved", Version: 1, Body: jsonBody(t, workflow.DirectoryRevisionData{Definition: def})},
		{Kind: "mount", ID: mi, DepartmentID: dept, OwnerID: owner, ParentID: di, State: "active", Version: 1, Body: jsonBody(t, workflow.MountData{DraftID: mr, ActiveID: mr, ResourceKey: fmt.Sprintf("%s:%s:%d", ref.Type, ref.ID, ref.Version)})},
		{Kind: "mount_revision", ID: mr, DepartmentID: dept, OwnerID: owner, ParentID: mi, State: "approved", Version: 1, Body: jsonBody(t, workflow.MountRevisionData{DirectoryRevisionID: dr, Resource: ref, Mapping: mapping})},
	} {
		if e := db.Create(&r).Error; e != nil {
			t.Fatal(e)
		}
	}
	return workflow.SharingSnapshot{DirectoryID: di, DirectoryRevisionID: dr, MountID: mi, MountRevisionID: mr}
}
func TestDirectoryReviewLifecycle(t *testing.T) {
	db, service, router := fixture(t)
	dept := "目录测试"
	ownerID, ownerName := businessUser(t, service, "catalog_", dept, []string{"provider", "operator"})
	reviewerID, reviewerName := businessUser(t, service, "review_", dept, []string{"provider"})
	adminID, adminName := businessUser(t, service, "authority_", "主管测试", []string{"admin"})
	seedAssignment(t, db, ownerID, dept, "manage_resource")
	seedAssignment(t, db, ownerID, dept, "department_review")
	seedAssignment(t, db, reviewerID, dept, "department_review")
	seedAssignment(t, db, adminID, "*", "authority_review")
	owner, op := login(t, router, ownerName)
	reviewer, rp := login(t, router, reviewerName)
	authority, ap := login(t, router, adminName)
	call := func(cookie *http.Cookie, p identity.Principal, path string, v interface{}, status int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, v), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	dictionary := call(authority, ap, "/api/v1/directory-dictionaries", workflow.DirectoryDictionary{Name: "本地测试字典", Version: "test-v1", Source: "local-test", Verified: true, TestOnly: true, CodePattern: "^TEST-[a-z0-9]+$", Classifications: []string{"department/test"}, VerificationRef: "test-only"}, 201)
	def := workflow.DirectoryDefinition{Name: "测试资源目录", Description: "隔离数据库编目审批验证", Classification: "department", ClassificationPath: "department/test", ResourceCode: fmt.Sprintf("TEST-%d", time.Now().UnixNano()), DictionaryID: dictionary["id"].(string), ProviderName: dept, ProviderCode: "TEST", ResponsibleUserID: ownerID, UpdateFrequency: "每日", Format: "database", DataLayer: "raw_object", Items: []workflow.DirectoryItem{{Name: "id", BusinessName: "编号", LogicalType: "integer"}}, SharingType: "conditional", SharingConditions: "本地验证", SharingMethod: "query", UsageRestrictions: "测试", SecurityClassification: "一般数据", DepartmentApprovalRef: "test-dept", AssessmentRef: "test-assess"}
	created := call(owner, op, "/api/v1/resource-directories", def, 201)
	id := created["directory_id"].(string)
	call(owner, op, "/api/v1/resource-directories/"+id+"/submit", workflow.VersionAction{ExpectedVersion: 1}, 200)
	var rev workflow.Record
	if e := db.Where("kind='review' AND body->>'target_id'=? AND state='pending'", id).Take(&rev).Error; e != nil {
		t.Fatal(e)
	}
	call(owner, op, "/api/v1/directory-reviews/"+rev.ID+"/approve", workflow.VersionAction{ExpectedVersion: 1}, 403)
	call(reviewer, rp, "/api/v1/directory-reviews/"+rev.ID+"/approve", workflow.VersionAction{ExpectedVersion: 1}, 200)
	rev = workflow.Record{}
	if e := db.Where("kind='review' AND body->>'target_id'=? AND state='pending'", id).Take(&rev).Error; e != nil {
		t.Fatal(e)
	}
	call(authority, ap, "/api/v1/directory-reviews/"+rev.ID+"/approve", workflow.VersionAction{ExpectedVersion: 1}, 200)
	w := workflowRequest(router, "GET", "/api/v1/resource-directories/"+id, nil, owner, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var view struct {
		State     string `json:"state"`
		Available bool   `json:"available"`
		Version   int    `json:"version"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	if view.State != "published" || view.Available {
		t.Fatal("published metadata must not imply mounted data")
	}
	call(owner, op, "/api/v1/resource-directories/"+id+"/submit", workflow.VersionAction{ExpectedVersion: 1}, 409)
	def.Description = "变更草稿"
	call(owner, op, "/api/v1/resource-directories/"+id+"/revisions", map[string]interface{}{"expected_version": view.Version, "definition": def}, 200)
	var stored workflow.Record
	if e := db.Where("kind='directory' AND id=?", id).Take(&stored).Error; e != nil {
		t.Fatal(e)
	}
	if stored.State != "published" {
		t.Fatal("draft must not withdraw active publication")
	}
	current := workflowRequest(router, "GET", "/api/v1/resource-directories/"+id, nil, owner, "", "")
	var currentView struct {
		Definition workflow.DirectoryDefinition `json:"definition"`
	}
	if e := json.Unmarshal(current.Body.Bytes(), &currentView); e != nil {
		t.Fatal(e)
	}
	if currentView.Definition.Description == "变更草稿" {
		t.Fatal("unapproved draft replaced discoverable publication")
	}
	editing := workflowRequest(router, "GET", "/api/v1/resource-directories/"+id+"?scope=mine", nil, owner, "", "")
	if e := json.Unmarshal(editing.Body.Bytes(), &currentView); e != nil {
		t.Fatal(e)
	}
	if currentView.Definition.Description != "变更草稿" {
		t.Fatal("editing view lost draft revision")
	}
	filtered := workflowRequest(router, "GET", "/api/v1/resource-directories?search=测试资源&classification=department", nil, owner, "", "")
	if filtered.Code != 200 {
		t.Fatalf("directory filters=%d %s", filtered.Code, filtered.Body.String())
	}
	var filteredPage struct {
		Total int `json:"total"`
	}
	if e := json.Unmarshal(filtered.Body.Bytes(), &filteredPage); e != nil {
		t.Fatal(e)
	}
	if filteredPage.Total != 1 {
		t.Fatalf("published search returned %d instead of the matching resource", filteredPage.Total)
	}

	anonymous := workflowRequest(router, "GET", "/api/v1/resource-directories", nil, nil, "", "")
	if anonymous.Code != 401 {
		t.Fatalf("anonymous=%d", anonymous.Code)
	}
}
func TestResourceCapabilityIsolation(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "unscoped_", "权限测试", []string{"provider"})
	cookie, p := login(t, router, name)
	def := workflow.DirectoryDefinition{Name: "无权限", Classification: "department", Format: "database", DataLayer: "raw_object", SharingType: "conditional"}
	w := workflowRequest(router, "POST", "/api/v1/resource-directories", jsonBody(t, def), cookie, p.CSRFToken, "unscoped")
	if w.Code != 403 {
		t.Fatalf("provider must not implicitly manage resources: %d", w.Code)
	}
	seedAssignment(t, db, uid, p.DepartmentID, "manage_resource")
	w = workflowRequest(router, "POST", "/api/v1/resource-directories", jsonBody(t, def), cookie, p.CSRFToken, "scoped")
	if w.Code != 201 {
		t.Fatalf("explicit scope=%d %s", w.Code, w.Body.String())
	}
	var first struct {
		ID string `json:"directory_id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &first); e != nil {
		t.Fatal(e)
	}
	second := workflowRequest(router, "POST", "/api/v1/resource-directories", jsonBody(t, def), cookie, p.CSRFToken, "second")
	if second.Code != 201 {
		t.Fatal(second.Body.String())
	}
	var next struct {
		ID string `json:"directory_id"`
	}
	if e := json.Unmarshal(second.Body.Bytes(), &next); e != nil {
		t.Fatal(e)
	}
	scoped := workflow.ResourceAssignment{UserID: uid, DepartmentID: p.DepartmentID, ResourceID: first.ID, Capability: "share_approve", ExpiresAt: time.Now().Add(time.Hour), Reason: "only one directory"}
	if e := db.Create(&workflow.Record{Kind: "assignment", ID: "authority-" + uid, DepartmentID: p.DepartmentID, OwnerID: uid, State: "active", Version: 1, Body: jsonBody(t, scoped)}).Error; e != nil {
		t.Fatal(e)
	}
	for _, target := range []struct {
		id   string
		want bool
	}{{first.ID, true}, {next.ID, false}} {
		check := workflowRequest(router, "GET", "/api/v1/resource-access?resource_type=directory&resource_id="+target.id, nil, cookie, "", "")
		if check.Code != 200 {
			t.Fatal(check.Body.String())
		}
		var caps map[string]bool
		if e := json.Unmarshal(check.Body.Bytes(), &caps); e != nil {
			t.Fatal(e)
		}
		if caps["share_approve"] != target.want {
			t.Fatal("specific reviewer scope widened to other resource")
		}
	}
	if e := db.Model(&workflow.Record{}).Where("kind='assignment' AND body->>'user_id'=?", uid).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "POST", "/api/v1/resource-directories", jsonBody(t, def), cookie, p.CSRFToken, "revoked")
	if w.Code != 403 {
		t.Fatalf("revoked scope=%d", w.Code)
	}
}

func TestAdministratorFullResourceAccess(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "full_admin_", "平台管理", []string{"admin"})
	cookie, p := login(t, router, name)
	// An admin does not need per-platform or per-resource assignments.
	if e := db.Where("user_id=?", uid).Delete(&identity.PlatformAccess{}).Error; e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/api/v1/resource-access", "/api/v1/sources", "/api/v1/rules", "/api/v1/datasets", "/api/v1/accounts"} {
		w := workflowRequest(router, "GET", path, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("admin %s=%d %s", path, w.Code, w.Body.String())
		}
		if path == "/api/v1/resource-access" {
			var caps map[string]bool
			if e := json.Unmarshal(w.Body.Bytes(), &caps); e != nil {
				t.Fatal(e)
			}
			for cap, enabled := range caps {
				if !enabled {
					t.Fatalf("admin capability %s denied", cap)
				}
			}
		}
	}
	def := workflow.DirectoryDefinition{Name: "管理员目录", Classification: "department", Format: "database", DataLayer: "raw_object", SharingType: "conditional"}
	w := workflowRequest(router, "POST", "/api/v1/resource-directories", jsonBody(t, def), cookie, p.CSRFToken, "admin-create")
	if w.Code != 201 {
		t.Fatalf("admin create=%d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"directory_id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&workflow.Record{}).Where("kind='directory' AND id=?", created.ID).Update("department_id", "其他部门").Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "GET", "/api/v1/resource-directories?scope=mine", nil, cookie, "", "")
	var listed struct {
		Items []struct {
			ID string `json:"directory_id"`
		} `json:"items"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &listed); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, item := range listed.Items {
		found = found || item.ID == created.ID
	}
	if !found {
		t.Fatal("admin cannot see cross-department draft")
	}
	w = workflowRequest(router, "POST", "/api/v1/resource-directories/"+created.ID+"/revisions", jsonBody(t, map[string]interface{}{"expected_version": 1, "definition": def}), cookie, p.CSRFToken, "admin-edit-other")
	if w.Code != 200 {
		t.Fatalf("admin edit=%d %s", w.Code, w.Body.String())
	}

	dictionary := workflow.DirectoryDictionary{Name: "管理员测试字典", Version: "test", Source: "local-test", Verified: true, TestOnly: true, CodePattern: "^TEST-[a-z0-9]+$", Classifications: []string{"department/test"}, VerificationRef: "local"}
	w = workflowRequest(router, "POST", "/api/v1/directory-dictionaries", jsonBody(t, dictionary), cookie, p.CSRFToken, "admin-dict")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var dict struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &dict); e != nil {
		t.Fatal(e)
	}
	def = workflow.DirectoryDefinition{Name: "管理员审核目录", Description: "权限回归", Classification: "department", ClassificationPath: "department/test", ResourceCode: fmt.Sprintf("TEST-%d", time.Now().UnixNano()), DictionaryID: dict.ID, ProviderName: p.DepartmentID, ProviderCode: "TEST", ResponsibleUserID: uid, UpdateFrequency: "每日", Format: "database", DataLayer: "raw_object", Items: []workflow.DirectoryItem{{Name: "id", BusinessName: "编号", LogicalType: "integer"}}, SharingType: "conditional", SharingConditions: "本地验证", SharingMethod: "query", UsageRestrictions: "测试", SecurityClassification: "一般数据", DepartmentApprovalRef: "test", AssessmentRef: "test"}
	w = workflowRequest(router, "POST", "/api/v1/resource-directories", jsonBody(t, def), cookie, p.CSRFToken, "admin-review-dir")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if e := json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "POST", "/api/v1/resource-directories/"+created.ID+"/submit", jsonBody(t, workflow.VersionAction{ExpectedVersion: 1}), cookie, p.CSRFToken, "admin-submit")
	if w.Code != 200 {
		t.Fatalf("admin submit=%d %s", w.Code, w.Body.String())
	}
	for _, stage := range []string{"department", "authority"} {
		var review workflow.Record
		if e := db.Where("kind='review' AND body->>'target_id'=? AND body->>'stage'=? AND state='pending'", created.ID, stage).Take(&review).Error; e != nil {
			t.Fatal(e)
		}
		w = workflowRequest(router, "POST", "/api/v1/directory-reviews/"+review.ID+"/approve", jsonBody(t, workflow.VersionAction{ExpectedVersion: 1}), cookie, p.CSRFToken, "admin-"+stage)
		if w.Code != 200 {
			t.Fatalf("admin %s review=%d %s", stage, w.Code, w.Body.String())
		}
	}
	if e := db.Where("user_id=? AND role='admin'", uid).Delete(&identity.UserRole{}).Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "GET", "/api/v1/resource-access", nil, cookie, "", "")
	if w.Code != 403 {
		t.Fatalf("revoked admin role still effective=%d", w.Code)
	}
}
