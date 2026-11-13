package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGovernanceWorkspaceActorPrivacyAndArchiveSchedules(t *testing.T) {
	db, service, router := fixture(t, false)
	dept := "spaceprivacy-" + fmt.Sprint(time.Now().UnixNano())
	uid, name := businessUser(t, service, "space_privacy_a_", dept, []string{"governor"})
	otherID, otherName := businessUser(t, service, "space_privacy_b_", dept, []string{"governor"})
	cookie, actor := login(t, router, name)
	other, otherActor := login(t, router, otherName)
	spaces := []workflow.GovernanceWorkspace{}
	for i := 0; i < 2; i++ {
		w := workflowRequest(router, "POST", "/api/v1/governance/workspaces", jsonBody(t, map[string]interface{}{"name": fmt.Sprint("隐私空间", i)}), cookie, actor.CSRFToken, fmt.Sprintf("privacy-space-%s-%d", uid, i))
		if w.Code != 201 {
			t.Fatal(w.Body.String())
		}
		var space workflow.GovernanceWorkspace
		if e := json.Unmarshal(w.Body.Bytes(), &space); e != nil {
			t.Fatal(e)
		}
		spaces = append(spaces, space)
		id := fmt.Sprintf("privacy-workflow-%s-%d", uid, i)
		if e := db.Create(&workflow.GovernanceDefinitionRecord{ID: id, Kind: "workflow", DepartmentID: dept, LatestVersion: 1, WorkspaceID: &space.ID}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&workflow.GovernanceVersion{DefinitionID: id, Version: 1, State: "published", Definition: json.RawMessage(`{"name":"调度归属验证"}`), CreatedBy: uid}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&workflow.GovernanceSchedule{ID: id, WorkspaceID: space.ID, DepartmentID: dept, ActorID: uid, WorkflowID: id, WorkflowVersion: 1, Parameters: json.RawMessage(`{}`), Timezone: "UTC", IntervalSeconds: 3600, ConcurrencyPolicy: "forbid", MissedPolicy: "skip", NextRunAt: time.Now().Add(time.Hour), State: "active", Version: 1}).Error; e != nil {
			t.Fatal(e)
		}
	}
	runID := "private-run-" + uid
	if e := db.Create(&workflow.GovernanceRun{ID: runID, WorkspaceID: spaces[0].ID, DepartmentID: dept, ActorID: uid, Kind: "profile", State: "succeeded", Definition: json.RawMessage(`{}`), Result: json.RawMessage(`{"private":"actor-only-profile"}`)}).Error; e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/api/v1/governance/workspaces/" + spaces[0].ID + "/overview", "/api/v1/governance/runs?workspace_id=" + spaces[0].ID} {
		w := workflowRequest(router, "GET", path, nil, other, "", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), runID) || strings.Contains(w.Body.String(), "actor-only-profile") {
			t.Fatalf("actor privacy: %d %s", w.Code, w.Body.String())
		}
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"?workspace_id="+spaces[0].ID, nil, other, "", "")
	if w.Code != 403 {
		t.Fatalf("another actor read profile: %d", w.Code)
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/workspaces/"+spaces[0].ID+"/archive", []byte(`{"base_version":1}`), other, otherActor.CSRFToken, "privacy-not-owner-"+otherID)
	if w.Code != 403 {
		t.Fatalf("non-owner archived: %d", w.Code)
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/workspaces/"+spaces[0].ID+"/archive", []byte(`{"base_version":1}`), cookie, actor.CSRFToken, "privacy-archive-"+uid)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for i, space := range spaces {
		var schedule workflow.GovernanceSchedule
		if e := db.Where("workspace_id=?", space.ID).Take(&schedule).Error; e != nil {
			t.Fatal(e)
		}
		expected := "active"
		if i == 0 {
			expected = "paused"
		}
		if schedule.State != expected {
			t.Fatalf("archive changed wrong schedules: %d %s", i, schedule.State)
		}
	}
}
