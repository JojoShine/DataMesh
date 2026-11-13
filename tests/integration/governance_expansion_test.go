package integration

import (
	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGovernanceExpansion(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "expansion_owner_", "expansion-dept", []string{"admin"})
	cookie, actor := login(t, router, name)
	_, reviewerName := businessUser(t, service, "expansion_review_", "expansion-dept", []string{"admin"})
	reviewer, reviewActor := login(t, router, reviewerName)
	serial := 0
	postAs := func(path string, body interface{}, want int, cookie *http.Cookie, p identity.Principal) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprintf("expansion-%s-%d", uid, serial))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		return postAs(path, body, want, cookie, actor)
	}
	idOf := func(v map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if e := json.Unmarshal(v["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	get := func(path string, target interface{}) {
		t.Helper()
		w := workflowRequest(router, "GET", "/api/v1/governance/"+path, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		if e := json.Unmarshal(w.Body.Bytes(), target); e != nil {
			t.Fatal(e)
		}
	}
	wait := func(id string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			var run workflow.GovernanceRun
			get("runs/"+id, &run)
			if run.State == "succeeded" {
				return run
			}
			if run.State == "failed" || run.State == "interrupted" {
				t.Fatalf("run %s: %s", id, run.ErrorCode)
			}
		}
		t.Fatal("run did not finish")
		return workflow.GovernanceRun{}
	}
	now := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}, {Name: "status", LogicalType: "integer"}, {Name: "updated", LogicalType: "datetime"}, {Name: "secret", LogicalType: "string", Sensitive: true}}
	sources := []workflow.Resource{}
	for i, rows := range [][]map[string]interface{}{{{"id": 1, "name": "甲", "status": 0, "updated": time.Now().Add(-2 * time.Minute).In(time.FixedZone("offset", 10*3600)).Format(time.RFC3339), "secret": "private-one"}, {"id": 2, "name": "丙", "status": 1, "updated": now, "secret": "private-two"}}, {{"id": "1", "name": "乙", "status": 1, "updated": now, "secret": "private-three"}, {"id": 3, "name": "丁", "status": 1, "updated": now, "secret": "private-four"}}} {
		id := fmt.Sprintf("expansion-source-%s-%d", uid, i)
		for j, row := range rows {
			if e := db.Create(&workflow.ImportRow{BatchID: id, RowNumber: j + 1, Data: jsonBody(t, row)}).Error; e != nil {
				t.Fatal(e)
			}
		}
		directoryID := id + "-directory"
		publishedID := id + "-published"
		for _, record := range []workflow.Record{{Kind: "directory", ID: directoryID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "published", Version: 1, Body: jsonBody(t, map[string]interface{}{"published_id": publishedID})}, {Kind: "directory_revision", ID: publishedID, DepartmentID: actor.DepartmentID, OwnerID: uid, ParentID: directoryID, State: "published", Version: 1, Body: jsonBody(t, map[string]interface{}{"definition": map[string]interface{}{"name": "主体资料", "items": []map[string]string{{"name": "id", "business_name": "主体编号"}, {"name": "name", "business_name": "主体名称"}}}})}} {
			if e := db.Create(&record).Error; e != nil {
				t.Fatal(e)
			}
		}
		managed := workflow.ManagedResource{Name: "业务资料", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields})}
		if e := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, ParentID: directoryID, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
			t.Fatal(e)
		}
		sources = append(sources, workflow.Resource{Type: "table_resource", ID: id, Version: 1})
	}
	// A complete profile becomes a fixed baseline; the second concurrent request is refused.
	policy := workflow.GovernanceMonitorPolicy{Resource: sources[0], Fields: []string{"id", "status", "updated"}, TimeField: "updated", FreshnessSeconds: 3600, MaxNullRate: 0, MaxDuplicateRate: 1, MaxVolumeChange: .2, MaxDistributionChange: 1}
	monitorID := idOf(post("monitors", map[string]interface{}{"name": "资料持续监测", "policy": policy, "interval_minutes": 15, "enabled": false}, 201))
	observation := post("monitors/"+monitorID+"/run", map[string]int{"expected_version": 1}, 201)
	var profileID string
	if e := json.Unmarshal(observation["run_id"], &profileID); e != nil {
		t.Fatal(e)
	}
	post("monitors/"+monitorID+"/run", map[string]int{"expected_version": 1}, 409)
	profileRun := wait(profileID)
	var timeReport struct {
		BusinessTime *time.Time `json:"business_time"`
		Status       string     `json:"business_time_status"`
	}
	if e := json.Unmarshal(profileRun.Result, &timeReport); e != nil || timeReport.BusinessTime == nil || timeReport.BusinessTime.Format(time.RFC3339) != now || timeReport.Status != "verified" {
		t.Fatal("business time used lexical maximum instead of actual instant")
	}
	observe := func(want string) workflow.GovernanceMonitorObservation {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			var out struct {
				Items []workflow.GovernanceMonitorObservation `json:"items"`
			}
			get("monitors/"+monitorID+"/observations", &out)
			if len(out.Items) > 0 && out.Items[0].State == want {
				return out.Items[0]
			}
		}
		t.Fatalf("no %s observation", want)
		return workflow.GovernanceMonitorObservation{}
	}
	baseline := observe("passed")
	if e := db.Create(&workflow.ImportRow{BatchID: sources[0].ID, RowNumber: 3, Data: jsonBody(t, map[string]interface{}{"id": 4, "name": "戊", "status": 1, "updated": now, "secret": "private-five"})}).Error; e != nil {
		t.Fatal(e)
	}
	observation = post("monitors/"+monitorID+"/run", map[string]int{"expected_version": 1}, 201)
	_ = json.Unmarshal(observation["run_id"], &profileID)
	wait(profileID)
	alert := observe("alert")
	post("monitors/"+monitorID+"/resolve", map[string]interface{}{"observation_id": alert.ID, "expected_version": alert.Version, "retest_id": baseline.ID, "note": "旧基线不能充当复测"}, 409)
	if e := db.Where("batch_id=? AND row_number=3", sources[0].ID).Delete(&workflow.ImportRow{}).Error; e != nil {
		t.Fatal(e)
	}
	observation = post("monitors/"+monitorID+"/run", map[string]int{"expected_version": 1}, 201)
	_ = json.Unmarshal(observation["run_id"], &profileID)
	wait(profileID)
	retest := observe("passed")
	monitorIssue := idOf(post("quality-issues", map[string]interface{}{"run_id": *alert.RunID, "assignee_id": uid, "due_at": time.Now().Add(time.Hour)}, 201))
	post("monitors/"+monitorID+"/resolve", map[string]interface{}{"observation_id": alert.ID, "expected_version": alert.Version, "retest_id": retest.ID, "note": "关联工单后不能绕过统一复测"}, 409)
	var frozenSource workflow.Record
	if e := db.Where("id=?", sources[0].ID).Take(&frozenSource).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&workflow.Record{}).Where("id=?", sources[0].ID).Update("body", json.RawMessage(strings.Replace(string(frozenSource.Body), "业务资料", "调整后的业务资料", 1))).Error; e != nil {
		t.Fatal(e)
	}
	post("quality-issues/"+monitorIssue+"/retest", map[string]interface{}{"base_version": 1}, 409)
	if e := db.Model(&workflow.Record{}).Where("id=?", sources[0].ID).Update("body", frozenSource.Body).Error; e != nil {
		t.Fatal(e)
	}
	monitorRetest := post("quality-issues/"+monitorIssue+"/retest", map[string]interface{}{"base_version": 1}, 201)
	var issueRun string
	_ = json.Unmarshal(monitorRetest["retest_run_id"], &issueRun)
	wait(issueRun)
	observe("passed")
	post("quality-issues/"+monitorIssue+"/close", map[string]interface{}{"base_version": 2, "note": "真实复测恢复固定基线"}, 200)
	var closedObservation workflow.GovernanceMonitorObservation
	if e := db.Where("id=?", alert.ID).Take(&closedObservation).Error; e != nil || closedObservation.ResolutionState != "closed" {
		t.Fatal("monitor issue closure not synchronized")
	}
	post("quality-issues", map[string]interface{}{"run_id": *alert.RunID, "assignee_id": uid, "due_at": time.Now().Add(time.Hour)}, 409)
	// Real standard validation reports ordinals, never private field values.
	standard := workflow.GovernanceDefinition{Name: "有效状态", OwnerID: uid, StewardID: uid, Standard: &workflow.GovernanceStandard{Code: "expansion.status", LogicalType: "integer", Values: []string{"1"}, ValueMeanings: map[string]string{"1": "有效"}}}
	standardID := idOf(post("standards", map[string]interface{}{"definition": standard}, 201))
	post("standards/"+standardID+"/versions/1/publish", map[string]interface{}{}, 200)
	privateStandard := workflow.GovernanceDefinition{Name: "内部说明格式", OwnerID: uid, StewardID: uid, Standard: &workflow.GovernanceStandard{Code: "expansion.private", LogicalType: "string", Pattern: "^[a-z-]+$"}}
	privateStandardID := idOf(post("standards", map[string]interface{}{"definition": privateStandard}, 201))
	post("standards/"+privateStandardID+"/versions/1/publish", map[string]interface{}{}, 200)
	auditID := idOf(post("standard-audits", map[string]interface{}{"resource": sources[0], "bindings": []map[string]interface{}{{"field": "status", "standard": workflow.GovernanceRef{ID: standardID, Version: 1}}, {"field": "secret", "standard": workflow.GovernanceRef{ID: privateStandardID, Version: 1}}}}, 201))
	audit := wait(auditID)
	var auditResult struct {
		Audit struct {
			Rows     int `json:"rows"`
			Rejected int `json:"rejected_rows"`
		} `json:"standard_audit"`
	}
	if e := json.Unmarshal(audit.Result, &auditResult); e != nil || auditResult.Audit.Rows != 2 || auditResult.Audit.Rejected != 1 {
		t.Fatalf("wrong standard evidence: %s", audit.Result)
	}
	if !strings.Contains(string(audit.Result), `"status":"redacted"`) || strings.Contains(string(audit.Result), "private-one") {
		t.Fatal("sensitive audit value disclosed or falsely verified")
	}
	// Standard issues require new complete evidence, and reject a still-failing retest.
	standardIssue := idOf(post("quality-issues", map[string]interface{}{"run_id": auditID, "assignee_id": uid, "due_at": time.Now().Add(time.Hour)}, 201))
	retestIssue := post("quality-issues/"+standardIssue+"/retest", map[string]interface{}{"base_version": 1}, 201)
	_ = json.Unmarshal(retestIssue["retest_run_id"], &issueRun)
	wait(issueRun)
	post("quality-issues/"+standardIssue+"/close", map[string]interface{}{"base_version": 2}, 409)
	if e := db.Model(&workflow.ImportRow{}).Where("batch_id=? AND row_number=1", sources[0].ID).Update("data", jsonBody(t, map[string]interface{}{"id": 1, "name": "甲", "status": 1, "updated": now, "secret": "private-one"})).Error; e != nil {
		t.Fatal(e)
	}
	retestIssue = post("quality-issues/"+standardIssue+"/retest", map[string]interface{}{"base_version": 2}, 201)
	_ = json.Unmarshal(retestIssue["retest_run_id"], &issueRun)
	wait(issueRun)
	post("quality-issues/"+standardIssue+"/close", map[string]interface{}{"base_version": 3}, 200)
	if e := db.Model(&workflow.ImportRow{}).Where("batch_id=? AND row_number=1", sources[0].ID).Update("data", jsonBody(t, map[string]interface{}{"id": 1, "name": "甲", "status": 0, "updated": now, "secret": "private-one"})).Error; e != nil {
		t.Fatal(e)
	}
	var issueNotices struct {
		Total int `json:"total"`
	}
	get("issue-notices", &issueNotices)
	if issueNotices.Total < 4 {
		t.Fatal("assignment and progress notices missing")
	}
	// Seen timestamps are stable and unread filtering follows the recipient.
	var unread struct {
		Items []workflow.GovernanceIssueNotice `json:"items"`
		Total int                              `json:"total"`
	}
	get("issue-notices?seen=false", &unread)
	if len(unread.Items) == 0 {
		t.Fatal("unread notices missing")
	}
	seen := post("issue-notices/"+unread.Items[0].ID+"/seen", map[string]interface{}{}, 200)
	seenAgain := post("issue-notices/"+unread.Items[0].ID+"/seen", map[string]interface{}{}, 200)
	if string(seen["seen_at"]) != string(seenAgain["seen_at"]) {
		t.Fatal("read timestamp overwritten")
	}
	get("issue-notices?seen=true", &unread)
	if unread.Total != 1 {
		t.Fatal("read filter ignored")
	}
	// A new basis can supersede an old issue; an unchanged basis cannot.
	freshAudit := idOf(post("standard-audits", map[string]interface{}{"resource": sources[0], "bindings": []map[string]interface{}{{"field": "status", "standard": workflow.GovernanceRef{ID: standardID, Version: 1}}}}, 201))
	wait(freshAudit)
	migratedIssue := idOf(post("quality-issues", map[string]interface{}{"run_id": freshAudit, "assignee_id": uid, "due_at": time.Now().Add(time.Hour)}, 201))
	unchangedAudit := idOf(post("standard-audits", map[string]interface{}{"resource": sources[0], "bindings": []map[string]interface{}{{"field": "status", "standard": workflow.GovernanceRef{ID: standardID, Version: 1}}}}, 201))
	wait(unchangedAudit)
	post("quality-issues/"+migratedIssue+"/supersede", map[string]interface{}{"base_version": 1, "replacement_run_id": unchangedAudit, "note": "不能用未变化依据替代"}, 409)
	nextStandard := standard
	nextStandard.Standard = &workflow.GovernanceStandard{Code: "expansion.status", LogicalType: "integer", Values: []string{"0", "1"}, ValueMeanings: map[string]string{"0": "待确认", "1": "有效"}}
	post("standards/"+standardID+"/versions", map[string]interface{}{"base_version": 1, "definition": nextStandard, "change_note": "明确待确认状态纳入业务范围"}, 201)
	post("standards/"+standardID+"/versions/2/publish", map[string]interface{}{"migration_note": "旧核验与模型保留固定标准1，新标准2用于重新核验及审计替代"}, 200)
	newAudit := idOf(post("standard-audits", map[string]interface{}{"resource": sources[0], "bindings": []map[string]interface{}{{"field": "status", "standard": workflow.GovernanceRef{ID: standardID, Version: 2}}}}, 201))
	wait(newAudit)
	superseded := post("quality-issues/"+migratedIssue+"/supersede", map[string]interface{}{"base_version": 1, "replacement_run_id": newAudit, "note": "新版本保留原检查字段并明确待确认状态；原异常证据仍保留"}, 200)
	if string(superseded["state"]) != `"superseded"` {
		t.Fatal("supersession mislabeled as passed closure")
	}
	post("quality-issues/"+migratedIssue+"/retest", map[string]interface{}{"base_version": 2}, 409)
	// A monitor policy migration is terminal handoff, never a pass of its original baseline.
	if e := db.Create(&workflow.ImportRow{BatchID: sources[0].ID, RowNumber: 3, Data: jsonBody(t, map[string]interface{}{"id": 4, "name": "戊", "status": 1, "updated": now, "secret": "private-five"})}).Error; e != nil {
		t.Fatal(e)
	}
	monitorOld := post("monitors/"+monitorID+"/run", map[string]int{"expected_version": 1}, 201)
	_ = json.Unmarshal(monitorOld["run_id"], &profileID)
	wait(profileID)
	oldAlert := observe("alert")
	policyIssue := idOf(post("quality-issues", map[string]interface{}{"run_id": *oldAlert.RunID, "assignee_id": uid, "due_at": time.Now().Add(time.Hour)}, 201))
	policy.MaxVolumeChange = .25
	post("monitors/"+monitorID, map[string]interface{}{"name": "资料持续监测", "policy": policy, "interval_minutes": 15, "enabled": false, "expected_version": 1}, 200)
	post("quality-issues/"+policyIssue+"/retest", map[string]interface{}{"base_version": 1}, 409)
	newObservation := post("monitors/"+monitorID+"/run", map[string]int{"expected_version": 2}, 201)
	_ = json.Unmarshal(newObservation["run_id"], &profileID)
	wait(profileID)
	observe("passed")
	post("quality-issues/"+policyIssue+"/supersede", map[string]interface{}{"base_version": 1, "replacement_run_id": profileID, "note": "业务范围扩展后明确调整监测配置及基线，原异常保留"}, 200)
	if e := db.Where("batch_id=? AND row_number=3", sources[0].ID).Delete(&workflow.ImportRow{}).Error; e != nil {
		t.Fatal(e)
	}
	// Down must stop before destructive DDL once a replacement link exists.
	down, e := os.ReadFile("../../migrations/000046_governance_readiness.down.sql")
	if e != nil {
		t.Fatal(e)
	}
	rollback := db.Begin()
	if rollback.Error != nil {
		t.Fatal(rollback.Error)
	}
	downgradeError := rollback.Exec(string(down)).Error
	rollback.Rollback()
	if downgradeError == nil || !db.Migrator().HasColumn(&workflow.GovernanceQualityIssue{}, "replacement_run_id") {
		t.Fatal("downgrade discarded replacement evidence")
	}
	// Published model supplies the entity contract; conflicting values require a real choice.
	entityFields := []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}}
	model := workflow.GovernanceDefinition{Name: "基础主体", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: entityFields}}
	modelID := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	labelGraph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: sources[0], Fields: []string{"id", "name"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Model: &workflow.GovernanceRef{ID: modelID, Version: 1}}}}
	labelFlowID := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "主体字段核对", OwnerID: uid, StewardID: uid, Dataflow: &labelGraph}}, 201))
	post("dataflows/"+labelFlowID+"/versions/1/publish", map[string]interface{}{}, 200)
	wait(idOf(post("dataflows/"+labelFlowID+"/versions/1/run", map[string]interface{}{}, 201)))
	var originalModel workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=1", modelID).Take(&originalModel).Error; e != nil {
		t.Fatal(e)
	}
	var modelLabels struct {
		Labels map[string]string `json:"field_labels"`
	}
	get("models/"+modelID+"/versions/1/field-labels", &modelLabels)
	if modelLabels.Labels["id"] != "主体编号" || modelLabels.Labels["name"] != "主体名称" {
		t.Fatalf("model labels missing: %+v", modelLabels)
	}
	var unchangedModel workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=1", modelID).Take(&unchangedModel).Error; e != nil || string(originalModel.Definition) != string(unchangedModel.Definition) {
		t.Fatal("label lookup rewrote immutable model")
	}
	master := workflow.GovernanceDefinition{Name: "主体主数据", OwnerID: uid, StewardID: uid, Master: &workflow.GovernanceMaster{ModelRef: &workflow.GovernanceRef{ID: modelID, Version: 1}, Keys: []string{"id"}, Sources: []workflow.GovernanceMasterSource{{ID: "registration", Resource: sources[0], RecordID: "id", Mapping: map[string]string{"id": "id", "name": "name"}}, {ID: "business", Resource: sources[1], RecordID: "id", Mapping: map[string]string{"id": "id", "name": "name"}}}}}
	masterID := idOf(post("masters", map[string]interface{}{"definition": master}, 201))
	post("masters/"+masterID+"/versions/1/publish", map[string]interface{}{}, 200)
	masterRunID := idOf(post("masters/"+masterID+"/match", map[string]int{"version": 1}, 201))
	wait(masterRunID)
	var candidates struct {
		Items []workflow.GovernanceMasterCandidate `json:"items"`
		Total int                                  `json:"total"`
	}
	get("master-runs/"+masterRunID+"/candidates?conflicts=true", &candidates)
	if candidates.Total != 1 {
		t.Fatalf("expected one conflicting entity: %+v", candidates)
	}
	candidate := candidates.Items[0]
	confirm := map[string]interface{}{"expected_version": 0, "choices": map[string]int{"id": 0, "name": 1}, "reason": "以业务受理来源核实的名称为准"}
	post("master-candidates/"+candidate.ID+"/confirm", confirm, 201)
	post("master-candidates/"+candidate.ID+"/confirm", confirm, 409)
	var revisions struct {
		Items []workflow.GovernanceMasterRevision `json:"items"`
	}
	get("master-candidates/"+candidate.ID+"/history", &revisions)
	if len(revisions.Items) != 1 || string(revisions.Items[0].Payload) == "null" {
		t.Fatal("confirmed history missing")
	}
	// Confirmed entity revisions become an ordinary governed result with original lineage.
	snapshotResult := post("masters/"+masterID+"/results", map[string]interface{}{"version": 1, "name": "主体确认成果"}, 201)
	var snapshotFlow struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if e := json.Unmarshal(snapshotResult["flow"], &snapshotFlow); e != nil || snapshotFlow.ID == "" {
		t.Fatal("missing master result draft")
	}
	confirm["expected_version"] = 1
	confirm["choices"] = map[string]int{"id": 0, "name": 0}
	post("master-candidates/"+candidate.ID+"/confirm", confirm, 201)
	post("dataflows/"+snapshotFlow.ID+"/versions/1/publish", map[string]interface{}{}, 200)
	confirmedRun := wait(idOf(post("dataflows/"+snapshotFlow.ID+"/versions/1/run", map[string]interface{}{}, 201)))
	var confirmedReport struct {
		Rows    int                                         `json:"output_rows"`
		Lineage map[string][]workflow.GovernanceFieldOrigin `json:"lineage"`
	}
	if e := json.Unmarshal(confirmedRun.Result, &confirmedReport); e != nil || confirmedReport.Rows != 1 || len(confirmedReport.Lineage["name"]) != 1 {
		t.Fatalf("master result lineage: %s", confirmedRun.Result)
	}
	var confirmedRows struct {
		Items []map[string]interface{} `json:"items"`
	}
	get("runs/"+confirmedRun.ID+"/records", &confirmedRows)
	if len(confirmedRows.Items) != 1 || confirmedRows.Items[0]["name"] != "乙" {
		t.Fatal("snapshot changed after a newer confirmation")
	}
	var snapshotID string
	_ = json.Unmarshal(snapshotResult["snapshot_id"], &snapshotID)
	otherRead := workflowRequest(router, "GET", "/api/v1/governance/metadata/"+snapshotID+"/contract", nil, reviewer, "", "")
	if otherRead.Code != 403 {
		t.Fatal("private master snapshot became visible to another administrator")
	}
	// Managed releases cannot bypass independent review; a trial creates no shared output.
	metric := workflow.GovernanceDefinition{Name: "状态主体数", OwnerID: uid, StewardID: uid, Metric: &workflow.GovernanceMetric{Source: workflow.GovernanceFlowSource{Resource: sources[0], Fields: []string{"id", "status"}}, Dimensions: []string{"status"}, Aggregates: []workflow.GovernanceFlowAggregate{{Operation: "count", Source: "id", Target: "entity_count"}}, Period: "all_time"}}
	metricID := idOf(post("metrics", map[string]interface{}{"definition": metric}, 201))
	proposalID := idOf(post("releases", map[string]interface{}{"kind": "dataflow", "candidate": workflow.GovernanceRef{ID: metricID, Version: 1}, "reason": "首次发布状态统计口径"}, 201))
	post("dataflows/"+metricID+"/versions/1/publish", map[string]interface{}{}, 409)
	post("releases/"+proposalID+"/approve", map[string]interface{}{"expected_version": 1, "note": "本人不能批准"}, 403)
	postAs("releases/"+proposalID+"/approve", map[string]interface{}{"expected_version": 1, "note": "缺少运行证据"}, 409, reviewer, reviewActor)
	trialID := idOf(post("releases/"+proposalID+"/trial", map[string]interface{}{"expected_version": 1}, 201))
	trial := wait(trialID)
	var trialResult struct {
		DatasetID      string                  `json:"dataset_id"`
		GeneratedModel *workflow.GovernanceRef `json:"generated_model"`
		Rows           int                     `json:"output_rows"`
	}
	if e := json.Unmarshal(trial.Result, &trialResult); e != nil || trialResult.DatasetID != "" || trialResult.GeneratedModel != nil || trialResult.Rows != 2 {
		t.Fatalf("trial published evidence incorrectly: %s", trial.Result)
	}
	var outputs int64
	if e := db.Table("governance_outputs").Where("run_id=?", trialID).Count(&outputs).Error; e != nil || outputs != 0 {
		t.Fatal("trial registered shared output")
	}
	postAs("releases/"+proposalID+"/approve", map[string]interface{}{"expected_version": 2, "note": "完整试运行及口径已复核"}, 200, reviewer, reviewActor)
	post("releases/"+proposalID+"/publish", map[string]interface{}{"expected_version": 3}, 200)
	resultID := idOf(post("dataflows/"+metricID+"/versions/1/run", map[string]interface{}{}, 201))
	result := wait(resultID)
	var metricResult struct {
		Rows  int                     `json:"output_rows"`
		Model *workflow.GovernanceRef `json:"generated_model"`
	}
	if e := json.Unmarshal(result.Result, &metricResult); e != nil || metricResult.Rows != 2 || metricResult.Model == nil {
		t.Fatalf("metric real output: %s", result.Result)
	}
	// Scenario instantiation creates new definitions and never inherits input grants.
	scenario := workflow.GovernanceDefinition{Name: "主体状态统计方案", OwnerID: uid, StewardID: uid, Scenario: &workflow.GovernanceScenario{Kind: "dataflow", Reference: workflow.GovernanceRef{ID: metricID, Version: 1}, Acceptance: "按状态汇总有编号记录数，核对来源行数"}}
	scenarioID := idOf(post("scenarios", map[string]interface{}{"definition": scenario}, 201))
	post("scenarios/"+scenarioID+"/versions/1/publish", map[string]interface{}{}, 200)
	instance := post("scenarios/"+scenarioID+"/instantiate", map[string]interface{}{"version": 1, "name": "本批主体统计", "bindings": []map[string]interface{}{{"task_id": "", "node_id": "source", "resource": sources[1]}}}, 201)
	var instanceDef workflow.GovernanceDefinition
	if e := json.Unmarshal(instance["definition"], &instanceDef); e != nil || instanceDef.Dataflow.Nodes[0].Source.Resource != sources[1] || instanceDef.Metric != nil {
		t.Fatal("instance did not rebind to new source")
	}
	flowID := idOf(instance)
	post("dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
	wf := workflow.GovernanceDefinition{Name: "主体核验工作流", OwnerID: uid, StewardID: uid, Workflow: &workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{{ID: "calculate", Dataflow: workflow.GovernanceRef{ID: metricID, Version: 1}, Condition: "all_success"}}}}
	wfID := idOf(post("workflows", map[string]interface{}{"definition": wf}, 201))
	post("workflows/"+wfID+"/versions/1/publish", map[string]interface{}{}, 200)
	// A workflow candidate executes children fully, including private predecessor bindings and simulated approval.
	trialWorkflow := workflow.GovernanceDefinition{Name: "完整编排试跑", OwnerID: uid, StewardID: uid, Workflow: &workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{{ID: "first", Dataflow: workflow.GovernanceRef{ID: snapshotFlow.ID, Version: 1}}, {ID: "second", Dataflow: workflow.GovernanceRef{ID: snapshotFlow.ID, Version: 1}, DependsOn: []string{"first"}, Inputs: map[string]string{"confirmed_entities": "first"}}, {ID: "quality", Kind: "quality_check", InputTask: "second", DependsOn: []string{"second"}}, {ID: "approval", Kind: "publish_approval", InputTask: "second", DependsOn: []string{"second", "quality"}}}}}
	trialWorkflowID := idOf(post("workflows", map[string]interface{}{"definition": trialWorkflow}, 201))
	wfReleaseID := idOf(post("releases", map[string]interface{}{"kind": "workflow", "candidate": workflow.GovernanceRef{ID: trialWorkflowID, Version: 1}, "reason": "核验完整编排及审批分支"}, 201))
	wfTrialID := idOf(post("releases/"+wfReleaseID+"/trial", map[string]interface{}{"expected_version": 1}, 201))
	wait(wfTrialID)
	var trialTasks []workflow.GovernanceWorkflowTaskRun
	if e := db.Where("parent_run_id=?", wfTrialID).Find(&trialTasks).Error; e != nil || len(trialTasks) != 4 {
		t.Fatal("workflow trial tasks missing")
	}
	for _, task := range trialTasks {
		if task.State != "succeeded" {
			t.Fatalf("workflow trial %s: %s", task.TaskID, task.ErrorCode)
		}
		if task.ChildRunID != nil {
			var n int64
			if e := db.Table("business_records").Where("kind='dataset' AND id=?", *task.ChildRunID).Count(&n).Error; e != nil || n != 0 {
				t.Fatal("workflow trial published a dataset")
			}
		}
	}
	postAs("releases/"+wfReleaseID+"/approve", map[string]interface{}{"expected_version": 2, "note": "固定子版本与完整运行已核实"}, 200, reviewer, reviewActor)

	scenario.Scenario.Kind = "workflow"
	scenario.Scenario.Reference = workflow.GovernanceRef{ID: wfID, Version: 1}
	scenario.Name = "主体核验流程方案"
	wfScenarioID := idOf(post("scenarios", map[string]interface{}{"definition": scenario}, 201))
	post("scenarios/"+wfScenarioID+"/versions/1/publish", map[string]interface{}{}, 200)
	bundle := post("scenarios/"+wfScenarioID+"/instantiate", map[string]interface{}{"version": 1, "name": "本批主体核验", "bindings": []map[string]interface{}{{"task_id": "calculate", "node_id": "source", "resource": sources[1]}}}, 201)
	bundleID := idOf(bundle)
	var body struct {
		Definition workflow.GovernanceDefinition `json:"definition"`
	}
	if e := json.Unmarshal(bundle["body"], &body); e != nil {
		t.Fatal(e)
	}
	post("scenario-instances/"+bundleID+"/finalize", map[string]int{"expected_version": 1}, 409)
	child := body.Definition.Workflow.Tasks[0].Dataflow
	post("dataflows/"+child.ID+"/versions/1/publish", map[string]interface{}{}, 200)
	post("scenario-instances/"+bundleID+"/finalize", map[string]int{"expected_version": 1}, 201)
	// Superseded candidates cannot consume earlier approvals; recovery creates a new draft.
	post("releases/recover", map[string]interface{}{"kind": "dataflow", "source": workflow.GovernanceRef{ID: metricID, Version: 1}, "expected_version": 1, "reason": "核实旧口径恢复引用"}, 201)
	var old, newVersion struct {
		State   string `json:"state"`
		Version int    `json:"version"`
	}
	get("dataflows/"+metricID+"/versions/1", &old)
	get("dataflows/"+metricID+"/versions/2", &newVersion)
	if old.State != "published" || newVersion.State != "draft" {
		t.Fatal("recovery overwrote old publication")
	}
	// Mandatory release policy applies even without a preexisting proposal.
	var policySpace workflow.GovernanceWorkspace
	get("workspaces/"+profileRun.WorkspaceID, &policySpace)
	post("workspaces/"+policySpace.ID+"/update", map[string]interface{}{"base_version": policySpace.Version, "name": policySpace.Name, "description": policySpace.Description, "max_concurrent_runs": policySpace.MaxConcurrentRuns, "required_release_kinds": []string{"dataflow", "workflow"}, "release_policy_reason": "后续处理发布须独立审核"}, 200)
	gated := idOf(post("metrics", map[string]interface{}{"definition": metric}, 201))
	post("dataflows/"+gated+"/versions/1/publish", map[string]interface{}{}, 409)
	var dashboard struct {
		Capabilities struct {
			Monitors  int `json:"monitors"`
			Audits    int `json:"audits"`
			Metrics   int `json:"metrics"`
			Confirmed int `json:"confirmed_entities"`
		} `json:"capabilities"`
	}
	get("dashboard", &dashboard)
	if dashboard.Capabilities.Monitors < 1 || dashboard.Capabilities.Audits < 3 || dashboard.Capabilities.Metrics < 2 || dashboard.Capabilities.Confirmed < 1 {
		t.Fatalf("missing closure dashboard: %+v", dashboard)
	}

	govID, govName := businessUser(t, service, "expansion_governor_", "expansion-dept", []string{"governor", "provider"})
	govCookie, govActor := login(t, router, govName)
	seedAssignment(t, db, govID, "expansion-dept", "govern_resource")
	seedAssignment(t, db, govID, "expansion-dept", "manage_resource")
	postAs("scenarios/"+scenarioID+"/instantiate", map[string]interface{}{"version": 1, "name": "不得继承授权", "bindings": []map[string]interface{}{{"task_id": "", "node_id": "source", "resource": sources[1]}}}, 403, govCookie, govActor)
	// A governor's confirmed result retains original derived-sharing requirements.
	grantIDs := []string{}
	for index, source := range sources {
		grantID := fmt.Sprintf("master-share-%s-%d", govID, index)
		grantIDs = append(grantIDs, grantID)
		if e := db.Create(&workflow.Record{Kind: "processing_authorization", ID: grantID, DepartmentID: govActor.DepartmentID, OwnerID: uid, State: "active", Version: 1, Body: jsonBody(t, workflow.ProcessingAuthorization{UserID: govID, Resource: source, Fields: []string{"id", "name"}, Purpose: "数据治理", ExpiresAt: time.Now().Add(time.Hour), AllowDerived: true, AllowSharing: true, SharingBasis: "核对后的主体成果共享依据", ApprovalRef: "source-review"})}).Error; e != nil {
			t.Fatal(e)
		}
	}
	govPost := func(path string, body interface{}, status int) map[string]json.RawMessage {
		t.Helper()
		return postAs(path, body, status, govCookie, govActor)
	}
	govWait := func(id string) {
		t.Helper()
		for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			response := workflowRequest(router, "GET", "/api/v1/governance/runs/"+id, nil, govCookie, "", "")
			if response.Code != 200 {
				t.Fatalf("governor run read: %d %s", response.Code, response.Body)
			}
			var row workflow.GovernanceRun
			if e := json.Unmarshal(response.Body.Bytes(), &row); e != nil {
				t.Fatal(e)
			}
			if row.State == "succeeded" {
				return
			}
			if row.State == "failed" || row.State == "interrupted" {
				t.Fatalf("governor run: %s", row.ErrorCode)
			}
		}
		t.Fatal("governor run timeout")
	}
	govMaster := master
	govMaster.Name = "授权主体主数据"
	govMaster.OwnerID = govID
	govMaster.StewardID = govID
	govMasterID := idOf(govPost("masters", map[string]interface{}{"definition": govMaster}, 201))
	govPost("masters/"+govMasterID+"/versions/1/publish", map[string]interface{}{}, 200)
	govMatch := idOf(govPost("masters/"+govMasterID+"/match", map[string]int{"version": 1}, 201))
	govWait(govMatch)
	response := workflowRequest(router, "GET", "/api/v1/governance/master-runs/"+govMatch+"/candidates", nil, govCookie, "", "")
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	var govCandidates struct {
		Items []workflow.GovernanceMasterCandidate `json:"items"`
	}
	if e := json.Unmarshal(response.Body.Bytes(), &govCandidates); e != nil || len(govCandidates.Items) == 0 {
		t.Fatal("governor candidates missing")
	}
	govPost("master-candidates/"+govCandidates.Items[0].ID+"/confirm", map[string]interface{}{"expected_version": 0, "choices": map[string]int{"id": 0, "name": 0}, "reason": "核对授权来源并确认本次主体取值"}, 201)
	govSnapshot := govPost("masters/"+govMasterID+"/results", map[string]interface{}{"version": 1, "name": "授权主体确认成果"}, 201)
	var govFlow struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(govSnapshot["flow"], &govFlow); e != nil {
		t.Fatal(e)
	}
	govRelease := idOf(govPost("releases", map[string]interface{}{"kind": "dataflow", "candidate": workflow.GovernanceRef{ID: govFlow.ID, Version: 1}, "reason": "核对主体确认成果与原来源共享依据"}, 201))
	govTrial := idOf(govPost("releases/"+govRelease+"/trial", map[string]interface{}{"expected_version": 1}, 201))
	govWait(govTrial)
	postAs("releases/"+govRelease+"/approve", map[string]interface{}{"expected_version": 2, "note": "完整试跑与原来源授权已核对"}, 200, reviewer, reviewActor)
	govPost("releases/"+govRelease+"/publish", map[string]interface{}{"expected_version": 3}, 200)
	govResult := idOf(govPost("dataflows/"+govFlow.ID+"/versions/1/run", map[string]interface{}{}, 201))
	govWait(govResult)
	if e := db.Model(&workflow.Record{}).Where("id=?", grantIDs[0]).Update("state", "revoked").Error; e != nil {
		t.Fatal(e)
	}
	publishGov := func(want int) {
		t.Helper()
		serial++
		response := workflowRequest(router, "POST", "/api/v1/datasets/"+govResult+"/publish", jsonBody(t, map[string]interface{}{}), govCookie, govActor.CSRFToken, fmt.Sprintf("gov-result-publish-%s-%d", uid, serial))
		if want == 403 && !strings.Contains(response.Body.String(), "DERIVED_SHARING_BASIS_REQUIRED") {
			t.Fatalf("wrong sharing rejection: %s", response.Body)
		}
		if response.Code != want {
			t.Fatalf("governor result publish: %d %s", response.Code, response.Body)
		}
	}
	publishGov(403)
	if e := db.Model(&workflow.Record{}).Where("id=?", grantIDs[0]).Update("state", "active").Error; e != nil {
		t.Fatal(e)
	}
	publishGov(200)

	// Revoking an original input blocks reuse of the confirmed snapshot, including administrator dispatch.
	var originalManaged workflow.Record
	if e := db.Where("id=?", sources[0].ID).Take(&originalManaged).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&workflow.Record{}).Where("id=?", sources[0].ID).Update("state", "disabled").Error; e != nil {
		t.Fatal(e)
	}
	post("dataflows/"+snapshotFlow.ID+"/versions/1/run", map[string]interface{}{}, 409)
	if e := db.Model(&workflow.Record{}).Where("id=?", sources[0].ID).Update("state", originalManaged.State).Error; e != nil {
		t.Fatal(e)
	}

	// Bulk confirmation is atomic and checks the preview against current versions.
	get("master-runs/"+masterRunID+"/candidates?confirmed=false", &candidates)
	if candidates.Total != 2 {
		t.Fatalf("unconfirmed filter: %d", candidates.Total)
	}
	ids := []string{candidates.Items[0].ID, candidates.Items[1].ID}
	post("master-runs/"+masterRunID+"/batch-preview", map[string]interface{}{"candidate_ids": []string{candidate.ID}}, 409)
	bulk := post("master-runs/"+masterRunID+"/batch-preview", map[string]interface{}{"candidate_ids": ids}, 200)
	var token string
	_ = json.Unmarshal(bulk["preview_token"], &token)
	post("master-runs/"+masterRunID+"/batch-confirm", map[string]interface{}{"candidate_ids": ids, "preview_token": "outdated", "reason": "已核对来源"}, 409)
	post("master-candidates/"+ids[0]+"/confirm", map[string]interface{}{"expected_version": 0, "choices": map[string]int{"id": 0, "name": 0}, "reason": "先核对一个实体"}, 201)
	post("master-runs/"+masterRunID+"/batch-confirm", map[string]interface{}{"candidate_ids": ids, "preview_token": token, "reason": "过期批次应拒绝"}, 409)
	get("master-runs/"+masterRunID+"/candidates?confirmed=false", &candidates)
	if candidates.Total != 1 {
		t.Fatal("failed batch partially confirmed entities")
	}
	ids = []string{candidates.Items[0].ID}
	bulk = post("master-runs/"+masterRunID+"/batch-preview", map[string]interface{}{"candidate_ids": ids}, 200)
	_ = json.Unmarshal(bulk["preview_token"], &token)
	post("master-runs/"+masterRunID+"/batch-confirm", map[string]interface{}{"candidate_ids": ids, "preview_token": token, "reason": "已按预览核对全部字段与来源编号"}, 201)
	get("master-runs/"+masterRunID+"/candidates?confirmed=false", &candidates)
	if candidates.Total != 0 {
		t.Fatal("bulk confirmation did not persist")
	}

	_, outsiderName := businessUser(t, service, "expansion_outside_", "another-department", []string{"governor"})
	outsider, _ := login(t, router, outsiderName)
	for _, path := range []string{"master-runs/" + masterRunID + "/candidates", "master-candidates/" + candidate.ID + "/history", "monitors/" + monitorID + "/observations", "releases/" + proposalID} {
		w := workflowRequest(router, "GET", "/api/v1/governance/"+path, nil, outsider, "", "")
		if w.Code != 403 {
			t.Fatalf("cross-department %s: %d", path, w.Code)
		}
	}
}
