package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceDashboardScopeAndMetrics(t *testing.T) {
	db, service, router := fixture(t, false)
	owner, ownerName := businessUser(t, service, "dashboard_owner_", "dashboard-dept", []string{"governor"})
	coworker, coworkerName := businessUser(t, service, "dashboard_peer_", "dashboard-dept", []string{"governor"})
	other, _ := businessUser(t, service, "dashboard_other_", "dashboard-other", []string{"governor"})
	_, adminName := businessUser(t, service, "dashboard_admin_", "dashboard-admin", []string{"admin"})
	cookie, _ := login(t, router, ownerName)
	peerCookie, _ := login(t, router, coworkerName)
	adminCookie, _ := login(t, router, adminName)
	now := time.Now().UTC().Add(-time.Minute)
	for i, item := range []struct{ actor, dept string }{{owner, "dashboard-dept"}, {other, "dashboard-other"}} {
		id := fmt.Sprintf("dashboard-model-%d", i)
		if e := db.Create(&workflow.GovernanceDefinitionRecord{ID: id, Kind: "model", DepartmentID: item.dept, LatestVersion: 2}).Error; e != nil {
			t.Fatal(e)
		}
		for version := 1; version <= 2; version++ {
			state := "published"
			if version == 2 {
				state = "draft"
			}
			if e := db.Create(&workflow.GovernanceVersion{DefinitionID: id, Version: version, State: state, Definition: json.RawMessage(`{}`), CreatedBy: item.actor}).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	for i, item := range []struct {
		actor, dept, state string
		at                 time.Time
	}{{owner, "dashboard-dept", "succeeded", now}, {owner, "dashboard-dept", "failed", now}, {coworker, "dashboard-dept", "succeeded", now}, {other, "dashboard-other", "succeeded", now}, {owner, "dashboard-dept", "succeeded", now.AddDate(0, 0, -100)}} {
		run := workflow.GovernanceRun{ID: fmt.Sprintf("dashboard-run-%d", i), Kind: "dataflow", DepartmentID: item.dept, ActorID: item.actor, State: item.state, Definition: json.RawMessage(`{}`), Result: json.RawMessage(`null`), CreatedAt: item.at}
		if e := db.Create(&run).Error; e != nil {
			t.Fatal(e)
		}
	}
	for i, item := range []struct {
		run           string
		input, output int64
	}{{"dashboard-run-0", 10, 9}, {"dashboard-run-0", 9, 8}, {"dashboard-run-2", 100, 100}} {
		if e := db.Create(&workflow.GovernanceRunEvent{RunID: item.run, NodeID: fmt.Sprintf("quality-%d", i), Code: "QUALITY_CHECKED", InputCount: item.input, OutputCount: item.output, CreatedAt: now}).Error; e != nil {
			t.Fatal(e)
		}
	}
	for i, item := range []struct{ actor, dept string }{{owner, "dashboard-dept"}, {other, "dashboard-other"}} {
		id := fmt.Sprintf("dashboard-object-%d", i)
		if e := db.Create(&workflow.Record{Kind: "object", ID: id, DepartmentID: item.dept, OwnerID: item.actor, State: "available", Version: 1, Body: json.RawMessage(`{}`)}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&workflow.Record{Kind: "rule", ID: fmt.Sprintf("dashboard-rule-%d", i), DepartmentID: item.dept, OwnerID: item.actor, State: "published", Version: 1, Body: json.RawMessage(`{}`)}).Error; e != nil {
			t.Fatal(e)
		}
		for version := 1; version <= 2; version++ {
			if e := db.Create(&workflow.MetadataObservation{ResourceType: "raw_object", ObservationKind: "live_schema", ID: fmt.Sprintf("dashboard-observation-%d-%d", i, version), ObjectID: id, Version: version, SourceVersion: 1, ObjectVersion: 1, Fields: json.RawMessage(`[]`), Columns: json.RawMessage(`[]`), Changes: json.RawMessage(`[]`), ObservedBy: item.actor}).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	assignment := map[string]interface{}{"user_id": owner, "capability": "govern_resource", "resource_id": "dashboard-object-0", "expires_at": now.Add(24 * time.Hour)}
	if e := db.Create(&workflow.Record{Kind: "assignment", ID: "dashboard-assignment", DepartmentID: "dashboard-dept", OwnerID: owner, State: "active", Version: 1, Body: jsonBody(t, assignment)}).Error; e != nil {
		t.Fatal(e)
	}
	for i, item := range []struct{ dept, owner, state string }{{"dashboard-dept", owner, "draft"}, {"dashboard-other", other, "draft"}, {"dashboard-other", other, "published"}} {
		if e := db.Create(&workflow.Record{Kind: "dataset", ID: fmt.Sprintf("dashboard-output-%d", i), DepartmentID: item.dept, OwnerID: item.owner, State: item.state, Version: 1, Body: json.RawMessage(`{"quality_status":"passed","completeness_status":"complete"}`)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	type response struct {
		Scope       string `json:"scope"`
		QualityRuns int64  `json:"quality_runs"`
		FlowModes   []struct {
			Kind  string
			Count int64
		} `json:"flow_modes"`
		Lightweight struct {
			Units, Files, Checked, Passed, Rejected int64
			Runs                                    []struct {
				State string
				Count int64
			}
		} `json:"lightweight"`
		Definitions []struct {
			Kind, State string
			Count       int64
		} `json:"definitions"`
		Resources    []struct{ Count int64 }                     `json:"resources"`
		Observations int64                                       `json:"observations"`
		Runs         []struct{ Count int64 }                     `json:"runs"`
		Outputs      []struct{ Count int64 }                     `json:"outputs"`
		Quality      []struct{ Checked, Passed, Rejected int64 } `json:"quality_trend"`
	}
	get := func(session *http.Cookie) response {
		t.Helper()
		w := workflowRequest(router, "GET", "/api/v1/governance/dashboard?days=7&timezone=Asia%2FShanghai", nil, session, "", "")
		if w.Code != 200 {
			t.Fatalf("dashboard=%d %s", w.Code, w.Body.String())
		}
		var result response
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	owned := get(cookie)
	if owned.QualityRuns != 1 {
		t.Fatalf("quality runs must deduplicate nodes: %d", owned.QualityRuns)
	}
	if len(owned.Definitions) != 1 || owned.Definitions[0].Kind != "model" || owned.Definitions[0].State != "draft" || owned.Definitions[0].Count != 1 {
		t.Fatalf("latest definitions=%+v", owned.Definitions)
	}
	if len(owned.Resources) != 1 || owned.Resources[0].Count != 1 || owned.Observations != 2 {
		t.Fatalf("resource scope=%+v", owned)
	}
	var runs, outputs, checked, passed, rejected int64
	for _, row := range owned.Runs {
		runs += row.Count
	}
	for _, row := range owned.Outputs {
		outputs += row.Count
	}
	for _, row := range owned.Quality {
		checked += row.Checked
		passed += row.Passed
		rejected += row.Rejected
	}
	if runs != 2 || outputs != 2 || checked != 19 || passed != 17 || rejected != 2 || owned.Scope != "visible" {
		t.Fatalf("scoped metrics runs=%d outputs=%d quality=%d/%d/%d scope=%s", runs, outputs, checked, passed, rejected, owned.Scope)
	}
	peer := get(peerCookie)
	if len(peer.Resources) != 0 || peer.Observations != 0 {
		t.Fatalf("unassigned resources leaked: %+v", peer)
	}
	all := get(adminCookie)
	runs = 0
	for _, row := range all.Runs {
		runs += row.Count
	}
	if all.Scope != "all" || runs != 4 || len(all.Definitions) != 1 || all.Definitions[0].Count != 2 {
		t.Fatalf("global metrics=%+v", all)
	}
	for _, query := range []string{"days=0", "days=91", "timezone=Local", "timezone=Invalid%2FZone"} {
		w := workflowRequest(router, "GET", "/api/v1/governance/dashboard?"+query, nil, cookie, "", "")
		if w.Code != 400 {
			t.Fatalf("invalid %s=%d", query, w.Code)
		}
	}
	if w := workflowRequest(router, "GET", "/api/v1/governance/dashboard", nil, nil, "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated dashboard=%d", w.Code)
	}
	for i, item := range []struct{ actor, dept string }{{owner, "dashboard-dept"}, {other, "dashboard-other"}} {
		id := fmt.Sprintf("dashboard-shared-standard-%d", i)
		if e := db.Create(&workflow.GovernanceDefinitionRecord{ID: id, Kind: "standard", DepartmentID: item.dept, LatestVersion: 1}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&workflow.GovernanceVersion{DefinitionID: id, Version: 1, State: "published", Definition: json.RawMessage(`{}`), CreatedBy: item.actor}).Error; e != nil {
			t.Fatal(e)
		}
	}
	var spaceID string
	if e := db.Table("governance_definitions").Select("workspace_id").Where("id=?", "dashboard-model-0").Scan(&spaceID).Error; e != nil {
		t.Fatal(e)
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/dashboard?workspace_id="+spaceID, nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("workspace dashboard=%d %s", w.Code, w.Body.String())
	}
	var scoped response
	if e := json.Unmarshal(w.Body.Bytes(), &scoped); e != nil {
		t.Fatal(e)
	}
	counts := map[string]int64{}
	for _, row := range scoped.Definitions {
		counts[row.Kind] += row.Count
	}
	if counts["model"] != 1 || counts["standard"] != 1 {
		t.Fatalf("workspace private and shared definitions=%+v", counts)
	}

	for i, actor := range []string{owner, coworker} {
		unit := fmt.Sprintf("dashboard-light-unit-%d", i)
		for _, kind := range []string{"lightweight_unit", "lightweight_upload"} {
			if e := db.Create(&workflow.Record{Kind: kind, ID: unit + kind, DepartmentID: "dashboard-dept", OwnerID: actor, State: "active", Version: 1, Body: json.RawMessage(`{}`)}).Error; e != nil {
				t.Fatal(e)
			}
		}
		run := workflow.GovernanceRun{ID: unit, Kind: "dataflow", DepartmentID: "dashboard-dept", ActorID: actor, State: "succeeded", Definition: jsonBody(t, map[string]interface{}{"flow": map[string]interface{}{"id": unit, "version": 1}, "lightweight": map[string]interface{}{"unit_id": unit, "version": 1}}), Result: json.RawMessage(`null`), CreatedAt: now}
		if e := db.Create(&run).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&workflow.GovernanceRunEvent{RunID: unit, NodeID: "quality", Code: "QUALITY_CHECKED", InputCount: 5, OutputCount: 4, CreatedAt: now}).Error; e != nil {
			t.Fatal(e)
		}
	}
	private := get(cookie)
	if private.Lightweight.Units != 1 || private.Lightweight.Files != 1 || private.Lightweight.Checked != 5 || private.Lightweight.Passed != 4 || private.Lightweight.Rejected != 1 || len(private.Lightweight.Runs) != 1 || private.Lightweight.Runs[0].Count != 1 || private.QualityRuns != 1 {
		t.Fatalf("private metrics mixed or lost: %+v", private)
	}
	administrator := get(adminCookie)
	if administrator.Lightweight.Units != 0 || administrator.Lightweight.Files != 0 || administrator.Lightweight.Checked != 0 || len(administrator.Lightweight.Runs) != 0 {
		t.Fatalf("private metrics leaked to admin: %+v", administrator.Lightweight)
	}
	w = workflowRequest(router, "GET", "/api/v1/governance/dashboard?workspace_id="+spaceID, nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if e := json.Unmarshal(w.Body.Bytes(), &scoped); e != nil {
		t.Fatal(e)
	}
	if scoped.Lightweight.Units != 1 || scoped.Lightweight.Checked != 5 {
		t.Fatalf("workspace incorrectly filters private summary: %+v", scoped.Lightweight)
	}
	if e := db.Create(&workflow.GovernanceDefinitionRecord{ID: "dashboard-modes", Kind: "dataflow", DepartmentID: "dashboard-dept", LatestVersion: 2}).Error; e != nil {
		t.Fatal(e)
	}
	for version, mode := range []string{"batch", "continuous"} {
		if e := db.Create(&workflow.GovernanceVersion{DefinitionID: "dashboard-modes", Version: version + 1, State: "draft", ExecutionMode: mode, Definition: json.RawMessage(`{}`), CreatedBy: owner}).Error; e != nil {
			t.Fatal(e)
		}
	}
	modes := get(cookie).FlowModes
	if len(modes) != 1 || modes[0].Kind != "continuous" || modes[0].Count != 1 {
		t.Fatalf("modes must use latest version: %+v", modes)
	}

}
