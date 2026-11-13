package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestMountValidationAndActivation(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "mount_", "挂载测试", []string{"operator", "provider"})
	cookie, p := login(t, router, name)
	seedAssignment(t, db, uid, p.DepartmentID, "operate_resource")
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	objID := "mount-object-" + uid
	ref := workflow.Resource{Type: "raw_object", ID: objID, Version: 1}
	object := workflow.Record{Kind: "object", ID: objID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: objID, SourceID: "isolated", QualifiedName: "public.customers", SchemaVersion: 1, Fields: fields})}
	if e := db.Create(&object).Error; e != nil {
		t.Fatal(e)
	}
	snapshot := seedSharingFixture(t, db, uid, p.DepartmentID, ref, fields)
	// A newly registered directory can save mappings before publication.
	if e := db.Model(&workflow.Record{}).Where("kind='directory' AND id=?", snapshot.DirectoryID).Updates(map[string]interface{}{"state": "draft", "body": jsonBody(t, workflow.DirectoryData{DraftID: snapshot.DirectoryRevisionID})}).Error; e != nil {
		t.Fatal(e)
	}

	w := workflowRequest(router, "POST", "/api/v1/resource-mounts", jsonBody(t, workflow.MountInput{DirectoryID: snapshot.DirectoryID, Resource: ref, Mapping: map[string]string{"id": "id"}}), cookie, p.CSRFToken, "mount-draft")
	if w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID      string `json:"mount_id"`
		Version int    `json:"version"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	for _, action := range []string{"validate", "submit", "activate"} {
		w = workflowRequest(router, "POST", "/api/v1/resource-mounts/"+created.ID+"/"+action, jsonBody(t, workflow.VersionAction{ExpectedVersion: 1}), cookie, p.CSRFToken, "draft-"+action)
		if w.Code != 409 {
			t.Fatalf("unpublished %s=%d %s", action, w.Code, w.Body.String())
		}
	}
	if e := db.Model(&workflow.Record{}).Where("kind='directory' AND id=?", snapshot.DirectoryID).Updates(map[string]interface{}{"state": "published", "body": jsonBody(t, workflow.DirectoryData{PublishedID: snapshot.DirectoryRevisionID})}).Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "POST", "/api/v1/resource-mounts/"+created.ID+"/validate", jsonBody(t, workflow.VersionAction{ExpectedVersion: 1}), cookie, p.CSRFToken, "validate-no-archive")
	if w.Code != 200 {
		t.Fatalf("validate=%d %s", w.Code, w.Body.String())
	}
	var view struct {
		Version    int                        `json:"version"`
		Definition workflow.MountRevisionData `json:"definition"`
		State      string                     `json:"state"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	if view.Definition.Validation == nil || view.Definition.Validation.Passed || view.State == "active" {
		t.Fatal("absent runtime evidence must not become success")
	}
	for _, action := range []string{"submit", "activate"} {
		w = workflowRequest(router, "POST", "/api/v1/resource-mounts/"+created.ID+"/"+action, jsonBody(t, workflow.VersionAction{ExpectedVersion: view.Version}), cookie, p.CSRFToken, action+fmt.Sprint(time.Now().UnixNano()))
		if w.Code != 409 {
			t.Fatalf("unvalidated %s=%d %s", action, w.Code, w.Body.String())
		}
	}
	w = workflowRequest(router, "POST", "/api/v1/resource-mounts", jsonBody(t, workflow.MountInput{DirectoryID: snapshot.DirectoryID, Resource: ref, Mapping: map[string]string{"id": "nonexistent"}}), cookie, p.CSRFToken, "bad-mapping")
	if w.Code != 400 {
		t.Fatalf("invalid mapping=%d", w.Code)
	}
}

func TestSharingGateCannotBeBypassedAndSupplement(t *testing.T) {
	db, service, router := fixture(t)
	providerID, providerName := businessUser(t, service, "authority_", "共享提供", []string{"provider"})
	_, consumerName := businessUser(t, service, "applicant_", "共享使用", []string{"consumer"})
	_, otherName := businessUser(t, service, "colleague_", "共享使用", []string{"consumer"})
	_, adminName := businessUser(t, service, "admin_", "平台", []string{"admin"})
	provider, pp := login(t, router, providerName)
	consumer, cp := login(t, router, consumerName)
	other, _ := login(t, router, otherName)
	admin, ap := login(t, router, adminName)
	seedAssignment(t, db, providerID, pp.DepartmentID, "share_approve")
	objectID := "sharing-object-" + providerID
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	ref := workflow.Resource{Type: "raw_object", ID: objectID, Version: 1}
	r := workflow.Record{Kind: "object", ID: objectID, DepartmentID: pp.DepartmentID, OwnerID: providerID, State: "available", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: objectID, SourceID: "isolated", QualifiedName: "public.customers", SchemaVersion: 1, Fields: fields})}
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	request := workflow.GrantRequest{Resource: ref, DepartmentID: cp.DepartmentID, Purpose: "隔离共享验证", Fields: []string{"id"}, ExpiresAt: time.Now().Add(time.Hour)}
	w := workflowRequest(router, "POST", "/api/v1/applications", jsonBody(t, request), consumer, cp.CSRFToken, "no-mount")
	if w.Code != 409 {
		t.Fatalf("legacy bypass=%d %s", w.Code, w.Body.String())
	}
	snapshot := seedSharingFixture(t, db, providerID, pp.DepartmentID, ref, fields)
	w = workflowRequest(router, "POST", "/api/v1/applications", jsonBody(t, request), consumer, cp.CSRFToken, "apply")
	if w.Code != 201 {
		t.Fatalf("apply=%d %s", w.Code, w.Body.String())
	}
	var application struct {
		ID string `json:"application_id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &application); e != nil {
		t.Fatal(e)
	}
	approvePath := "/api/v1/applications/" + application.ID + "/approve"
	w = workflowRequest(router, "POST", approvePath, []byte("{}"), provider, pp.CSRFToken, "incomplete")
	if w.Code != 409 {
		t.Fatalf("missing materials=%d", w.Code)
	}
	w = workflowRequest(router, "POST", "/api/v1/applications/"+application.ID+"/request-supplement", jsonBody(t, map[string]string{"reason": "请一次补齐使用依据、场景及负责人同意记录"}), provider, pp.CSRFToken, "supplement")
	if w.Code != 200 {
		t.Fatalf("supplement=%d %s", w.Code, w.Body.String())
	}
	materials := workflow.ApplicationMaterials{LegalBasis: "测试依据", Scenario: "测试场景", Scope: "编号", Method: "query", DepartmentApprovalRef: "LOCAL-TEST"}
	w = workflowRequest(router, "POST", "/api/v1/applications/"+application.ID+"/resubmit", jsonBody(t, materials), consumer, cp.CSRFToken, "resubmit")
	if w.Code != 200 {
		t.Fatalf("resubmit=%d %s", w.Code, w.Body.String())
	}
	w = workflowRequest(router, "POST", approvePath, []byte("{}"), provider, pp.CSRFToken, "approve")
	if w.Code != 200 {
		t.Fatalf("approve=%d %s", w.Code, w.Body.String())
	}
	path := "/delivery/v1/raw/" + objectID + "/records?contract_version=1"
	for _, c := range []*http.Cookie{other} {
		w = workflowRequest(router, "GET", path, nil, c, "", "")
		if w.Code != 403 {
			t.Fatalf("ungranted user=%d", w.Code)
		}
	}
	w = workflowRequest(router, "GET", path, nil, admin, "", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "RAW_NOT_ARCHIVED") {
		t.Fatalf("admin must reach runtime validation without grant=%d %s", w.Code, w.Body.String())
	}
	request.DepartmentID = ap.DepartmentID
	w = workflowRequest(router, "POST", "/api/v1/applications", jsonBody(t, request), admin, ap.CSRFToken, "admin-apply")
	if w.Code != 201 {
		t.Fatalf("admin can apply=%d", w.Code)
	}
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "suspended").Error; e != nil {
		t.Fatal(e)
	}
	w = workflowRequest(router, "GET", path, nil, consumer, "", "")
	if w.Code != 409 {
		t.Fatalf("suspended service=%d %s", w.Code, w.Body.String())
	}
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	t.Setenv("DIRECTORY_TEST_MODE", "0")
	w = workflowRequest(router, "GET", path, nil, consumer, "", "")
	if w.Code != 409 {
		t.Fatalf("test dictionary escaped local mode=%d %s", w.Code, w.Body.String())
	}

}

func TestConcurrentMountActivation(t *testing.T) {
	db, service, _ := fixture(t)
	uid, _ := businessUser(t, service, "unique_", "并发挂载测试", []string{"operator"})
	result := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			r := workflow.Record{Kind: "mount", ID: fmt.Sprintf("unique-%s-%d", uid, i), DepartmentID: "并发挂载测试", OwnerID: uid, ParentID: "same-directory-" + uid, State: "active", Version: 1, Body: jsonBody(t, workflow.MountData{ResourceKey: "raw_object:" + uid + ":1"})}
			result <- db.Create(&r).Error
		}(i)
	}
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-result
		if e == nil {
			successes++
		} else {
			var pg *pgconn.PgError
			if !errors.Is(e, gorm.ErrDuplicatedKey) && (!errors.As(e, &pg) || pg.Code != "23505") {
				t.Fatal(e)
			}
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent mount uniqueness=%d/%d", successes, conflicts)
	}
}
