package integration

import (
	"context"
	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAssetConsumerJourney(t *testing.T) {
	db, service, router := fixture(t)
	providerID, providerName := businessUser(t, service, "asset_provider_", "asset-dept", []string{"provider", "governor"})
	consumerName := fmt.Sprintf("asset_consumer_%d", time.Now().UnixNano())
	consumerID, err := service.CreateUser(context.Background(), identity.NewUser{Username: consumerName, Password: password, DepartmentID: "asset-dept", Platforms: []string{"assets"}, Roles: []string{"consumer"}})
	if err != nil {
		t.Fatal(err)
	}
	provider, pp := login(t, router, providerName)
	consumer, cp := login(t, router, consumerName)
	seedAssignment(t, db, providerID, "asset-dept", "share_approve")
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	ref := workflow.Resource{Type: "raw_object", ID: "asset-object-" + consumerID, Version: 1}
	if e := db.Create(&workflow.Record{Kind: "object", ID: ref.ID, DepartmentID: "asset-dept", OwnerID: providerID, State: "available", Version: 1, Body: jsonBody(t, workflow.Object{ObjectID: ref.ID, SourceID: "fixture", QualifiedName: "public.example", SchemaVersion: 1, Fields: fields})}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot := seedSharingFixture(t, db, providerID, "asset-dept", ref, fields)
	// A published but unmounted directory must not enter discovery.
	if e := db.Create(&workflow.Record{Kind: "directory", ID: "unshared-" + consumerID, DepartmentID: "asset-dept", OwnerID: consumerID, State: "published", Version: 1, Body: jsonBody(t, workflow.DirectoryData{PublishedID: snapshot.DirectoryRevisionID})}).Error; e != nil {
		t.Fatal(e)
	}
	read := func(path string, cookie *http.Cookie, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "GET", path, nil, cookie, "", "")
		if w.Code != want {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var v map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	write := func(path string, body interface{}, cookie *http.Cookie, p identity.Principal, want int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != want {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
		var v map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	discovered := read("/api/v1/assets/directories?resource_id="+ref.ID, consumer, 200)
	if discovered["total"] != float64(1) {
		t.Fatalf("discovery count=%v", discovered["total"])
	}
	// Reviewed metadata is available without exposing source internals or invented freshness.
	detail := read("/api/v1/assets/resource-detail?resource_type=raw_object&resource_id="+ref.ID+"&contract_version=1", consumer, 200)
	if detail["data_cutoff"] != nil || detail["generated_at"] != nil || detail["owner"] != providerName || len(detail["fields"].([]interface{})) != 1 {
		t.Fatalf("unexpected evidence: %v", detail)
	}
	read("/api/v1/assets/resource-detail?resource_type=raw_object&resource_id="+ref.ID+"&contract_version=2", consumer, 409)
	feedbackBody := map[string]interface{}{"resource": ref, "category": "meaning", "title": "字段口径疑问", "description": "请说明标识字段口径", "fields": []string{"id"}}
	feedback := write("/api/v1/assets/feedback", feedbackBody, consumer, cp, 201)
	feedbackID := feedback["id"].(string)
	read("/api/v1/assets/feedback/"+feedbackID, provider, 403)
	if own := read("/api/v1/assets/feedback", provider, 200); own["total"] != float64(0) {
		t.Fatal("peer sees private feedback")
	}
	read("/api/v1/governance/asset-feedback", consumer, 403)
	_, outsiderName := businessUser(t, service, "asset_outsider_", "other-dept", []string{"governor"})
	outsider, _ := login(t, router, outsiderName)
	read("/api/v1/governance/asset-feedback/"+feedbackID, outsider, 403)
	if inbox := read("/api/v1/governance/asset-feedback", outsider, 200); inbox["total"] != float64(0) {
		t.Fatal("other department sees feedback")
	}
	inbox := read("/api/v1/governance/asset-feedback", provider, 200)
	if inbox["total"] != float64(1) {
		t.Fatal(inbox)
	}
	write("/api/v1/governance/asset-feedback/"+feedbackID+"/reply", map[string]interface{}{"base_version": 1, "state": "in_progress", "response": "正在核对口径"}, provider, pp, 200)
	write("/api/v1/governance/asset-feedback/"+feedbackID+"/reply", map[string]interface{}{"base_version": 1, "state": "resolved", "response": "旧版本回复不得覆盖"}, provider, pp, 409)
	write("/api/v1/governance/asset-feedback/"+feedbackID+"/remediate", map[string]interface{}{"base_version": 2}, provider, pp, 409)
	write("/api/v1/governance/asset-feedback/"+feedbackID+"/reply", map[string]interface{}{"base_version": 2, "state": "resolved", "response": "此字段是来源系统记录标识"}, provider, pp, 200)
	history := read("/api/v1/assets/feedback/"+feedbackID, consumer, 200)
	if history["state"] != "resolved" || len(history["events"].([]interface{})) != 3 {
		t.Fatal(history)
	}
	feedbackBody["fields"] = []string{"not_shared"}
	write("/api/v1/assets/feedback", feedbackBody, consumer, cp, 403)
	feedbackBody["fields"] = []string{"id"}
	read("/api/v1/assets/directories?scope=mine", consumer, 400)
	read("/api/v1/resource-directories", consumer, 403)
	read("/api/v1/data-services", consumer, 403)
	read("/api/v1/assets/directories", nil, 401)
	read("/delivery/v1/raw/"+ref.ID+"/records?fields=id&contract_version=1", consumer, 403)
	request := workflow.GrantRequest{Resource: ref, DepartmentID: "asset-dept", Purpose: "共享查询", Fields: []string{"id"}, ExpiresAt: time.Now().Add(time.Hour), Materials: &workflow.ApplicationMaterials{LegalBasis: "测试依据", Scenario: "资产使用", Scope: "id", Method: "query", DepartmentApprovalRef: "test"}}
	app := write("/api/v1/applications", request, consumer, cp, 201)
	id := app["application_id"].(string)
	write("/api/v1/applications/"+id+"/approve", map[string]string{}, consumer, cp, 403)
	write("/api/v1/applications/"+id+"/request-supplement", map[string]string{"reason": "补充依据"}, provider, pp, 200)
	write("/api/v1/applications/"+id+"/resubmit", request.Materials, consumer, cp, 200)
	write("/api/v1/applications/"+id+"/approve", map[string]string{}, provider, pp, 200)
	if mine := read("/api/v1/assets/applications?scope=mine", consumer, 200); mine["total"] != float64(1) {
		t.Fatal(mine)
	}
	if peers := read("/api/v1/assets/applications", provider, 200); peers["total"] != float64(0) {
		t.Fatal("provider sees peer application in my list")
	}
	grants := read("/api/v1/assets/grants", consumer, 200)
	if grants["total"] != float64(1) {
		t.Fatal(grants)
	}
	grant := grants["items"].([]interface{})[0].(map[string]interface{})
	if grant["resource_name"] != "隔离目录" || grant["available"] != true {
		t.Fatal(grant)
	}
	if peers := read("/api/v1/assets/grants", provider, 200); peers["total"] != float64(0) {
		t.Fatal("provider sees peer grant in my list")
	}
	write("/api/v1/grants/"+grant["grant_id"].(string)+"/revoke", map[string]string{}, consumer, cp, 403)
	// Authorization succeeds, but an unarchived source must still be rejected.
	response := read("/delivery/v1/raw/"+ref.ID+"/records?fields=id&contract_version=1", consumer, 409)
	if response["code"] != "RAW_NOT_ARCHIVED" {
		t.Fatal(response)
	}

	preferenceBody := map[string]interface{}{"resource": ref, "base_version": 0, "bookmarked": true, "following": true}
	saved := write("/api/v1/assets/saved", preferenceBody, consumer, cp, 200)
	if saved["version"] != float64(1) || saved["bookmarked"] != true || saved["following"] != true {
		t.Fatal(saved)
	}
	write("/api/v1/assets/saved", preferenceBody, consumer, cp, 409)
	peerState := read("/api/v1/assets/saved-state?resource_type=raw_object&resource_id="+ref.ID+"&contract_version=1", provider, 200)
	if peerState["version"] != float64(0) {
		t.Fatal("peer preference exposed")
	}
	if own := read("/api/v1/assets/saved?scope=bookmarks", provider, 200); own["total"] != float64(0) {
		t.Fatal("peer collection exposed")
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	if notifications := read("/api/v1/assets/notices", consumer, 200); notifications["total"] != float64(1) {
		t.Fatalf("expected grant expiry notice: %v", notifications)
	}
	if e := db.Model(&workflow.Record{}).Where("id=?", snapshot.MountID).Update("state", "paused").Error; e != nil {
		t.Fatal(e)
	}
	grants = read("/api/v1/assets/grants", consumer, 200)
	if grants["items"].([]interface{})[0].(map[string]interface{})["available"] != false {
		t.Fatal("suspended sharing remained available")
	}
	read("/delivery/v1/raw/"+ref.ID+"/records?fields=id&contract_version=1", consumer, 409)
	read("/api/v1/assets/resource-detail?resource_type=raw_object&resource_id="+ref.ID+"&contract_version=1", consumer, 409)
	write("/api/v1/assets/feedback", feedbackBody, consumer, cp, 409)
	feedbackBody["grant_id"] = grant["grant_id"]
	feedbackBody["category"] = "access"
	write("/api/v1/assets/feedback", feedbackBody, consumer, cp, 201)
	write("/api/v1/assets/feedback", feedbackBody, provider, pp, 403)
	read("/api/v1/assets/feedback/"+feedbackID, consumer, 200)

	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	notices := read("/api/v1/assets/notices", consumer, 200)
	if notices["total"] != float64(2) || notices["unread"] != float64(2) {
		t.Fatal("refresh duplicated notice", notices)
	}
	noticeID := notices["items"].([]interface{})[0].(map[string]interface{})["id"].(string)
	write("/api/v1/assets/notices/"+noticeID+"/read", map[string]interface{}{}, provider, pp, 404)
	write("/api/v1/assets/notices/"+noticeID+"/read", map[string]interface{}{}, consumer, cp, 200)
	if unread := read("/api/v1/assets/notices?unread=true", consumer, 200); unread["total"] != float64(1) {
		t.Fatal(unread)
	}
	if own := read("/api/v1/assets/notices", provider, 200); own["total"] != float64(0) {
		t.Fatal("peer notices exposed")
	}
	preferenceBody["base_version"] = 1
	preferenceBody["following"] = false
	write("/api/v1/assets/saved", preferenceBody, consumer, cp, 200)
	preferenceBody["base_version"] = 2
	preferenceBody["following"] = true
	write("/api/v1/assets/saved", preferenceBody, consumer, cp, 409)
	if e := db.Model(&workflow.Record{}).Where("id=?", snapshot.MountID).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	if stopped := read("/api/v1/assets/notices", consumer, 200); stopped["total"] != float64(2) {
		t.Fatal("cancelled watch generated notice")
	}
	write("/api/v1/assets/saved", preferenceBody, consumer, cp, 200)
	if e := db.Model(&workflow.Record{}).Where("id=?", snapshot.MountID).Update("state", "paused").Error; e != nil {
		t.Fatal(e)
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	if e := db.Model(&workflow.Record{}).Where("id=?", snapshot.MountID).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	restored := read("/api/v1/assets/notices", consumer, 200)
	if restored["total"] != float64(4) {
		t.Fatal("repeated transition lost", restored)
	}
	found := false
	for _, raw := range restored["items"].([]interface{}) {
		if raw.(map[string]interface{})["category"] == "sharing_restored" {
			found = true
		}
	}
	if !found {
		t.Fatal("restoration notice missing")
	}

	// A newly approved mount revision must be detected even if the resource stays available.
	var mountRevision workflow.Record
	if e := db.Where("kind='mount_revision' AND id=?", snapshot.MountRevisionID).Take(&mountRevision).Error; e != nil {
		t.Fatal(e)
	}
	mountRevision.ID += "-replacement"
	if e := db.Create(&mountRevision).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&workflow.Record{}).Where("kind='mount' AND id=?", snapshot.MountID).Update("body", jsonBody(t, workflow.MountData{DraftID: mountRevision.ID, ActiveID: mountRevision.ID, ResourceKey: fmt.Sprintf("%s:%s:%d", ref.Type, ref.ID, ref.Version)})).Error; e != nil {
		t.Fatal(e)
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	changed := read("/api/v1/assets/notices", consumer, 200)
	if changed["items"].([]interface{})[0].(map[string]interface{})["category"] != "sharing_changed" {
		t.Fatal(changed)
	}
	var grantRecord workflow.Record
	if e := db.Where("kind='grant' AND id=?", grant["grant_id"]).Take(&grantRecord).Error; e != nil {
		t.Fatal(e)
	}
	var grantData workflow.GrantData
	if e := json.Unmarshal(grantRecord.Body, &grantData); e != nil {
		t.Fatal(e)
	}
	grantData.Scope.ExpiresAt = time.Now().Add(-time.Hour)
	if e := db.Model(&grantRecord).Update("body", jsonBody(t, grantData)).Error; e != nil {
		t.Fatal(e)
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	expired := read("/api/v1/assets/notices", consumer, 200)
	if expired["items"].([]interface{})[0].(map[string]interface{})["category"] != "grant_expired" {
		t.Fatal(expired)
	}
	write("/api/v1/assets/notices/refresh", map[string]interface{}{}, consumer, cp, 200)
	if again := read("/api/v1/assets/notices", consumer, 200); again["total"] != expired["total"] {
		t.Fatal("expired grant reminder duplicated")
	}
	if e := db.Model(&workflow.Record{}).Where("id=?", snapshot.MountID).Update("state", "paused").Error; e != nil {
		t.Fatal(e)
	}
	discovered = read("/api/v1/assets/directories?resource_id="+ref.ID, consumer, 200)
	if discovered["total"] != float64(0) {
		t.Fatal("paused resource remains discoverable")
	}
}

func verifyGovernedAssetFeedback(t *testing.T, router http.Handler, owner *http.Cookie, op identity.Principal, consumer *http.Cookie, cp identity.Principal, ref workflow.Resource, assignee string) {
	t.Helper()
	call := func(method, path string, body interface{}, cookie *http.Cookie, p identity.Principal, want int) map[string]interface{} {
		t.Helper()
		var payload []byte
		if body != nil {
			payload = jsonBody(t, body)
		}
		w := workflowRequest(router, method, path, payload, cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	detail := call("GET", "/api/v1/assets/resource-detail?resource_type=governed_dataset&resource_id="+ref.ID+"&contract_version=1", nil, consumer, cp, 200)
	if detail["quality_checks"] != float64(1) || detail["output_validated"] != true || detail["output_rows"] != float64(1) || detail["rejected_rows"] != float64(1) || detail["generated_at"] == nil || detail["data_cutoff"] != nil {
		t.Fatalf("real evidence %v", detail)
	}
	for _, key := range []string{"definition", "sql", "storage", "inputs", "credentials", "lineage"} {
		if _, ok := detail[key]; ok {
			t.Fatalf("internal field exposed: %s", key)
		}
	}
	body := map[string]interface{}{"resource": ref, "category": "quality", "title": "核对汇总口径", "description": "请核对汇总结果的口径", "fields": []string{"id"}}
	first := call("POST", "/api/v1/assets/feedback", body, consumer, cp, 201)
	id := first["id"].(string)
	call("POST", "/api/v1/governance/asset-feedback/"+id+"/reply", map[string]interface{}{"base_version": 1, "state": "resolved", "response": "未授权人员不得处理"}, consumer, cp, 403)
	linked := call("POST", "/api/v1/governance/asset-feedback/"+id+"/remediate", map[string]interface{}{"base_version": 1, "assignee_id": assignee, "due_at": time.Now().Add(24 * time.Hour)}, owner, op, 200)
	issue := linked["issue_id"].(string)
	call("POST", "/api/v1/governance/asset-feedback/"+id+"/reply", map[string]interface{}{"base_version": 2, "state": "resolved", "response": "未复测不能完成"}, owner, op, 409)
	call("POST", "/api/v1/governance/quality-issues/"+issue+"/close", map[string]interface{}{"base_version": 1}, owner, op, 409)
	second := call("POST", "/api/v1/assets/feedback", body, consumer, cp, 201)
	reused := call("POST", "/api/v1/governance/asset-feedback/"+second["id"].(string)+"/remediate", map[string]interface{}{"base_version": 1}, owner, op, 200)
	if reused["issue_id"] != issue {
		t.Fatal("duplicate remediation created for same run")
	}
	call("POST", "/api/v1/governance/quality-issues/"+issue+"/notes", map[string]interface{}{"base_version": 1, "note": "internal-only-diagnostic"}, owner, op, 200)
	own := call("GET", "/api/v1/assets/feedback/"+id, nil, consumer, cp, 200)
	encoded := string(jsonBody(t, own))
	if strings.Contains(encoded, "internal-only-diagnostic") || strings.Contains(encoded, "run_id") {
		t.Fatal("internal remediation detail leaked")
	}
	if own["issue_state"] != "in_progress" {
		t.Fatal(own)
	}
}
