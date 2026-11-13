package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceWorkspaceArchiveRace(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "space_race_", "spacerace-"+fmt.Sprint(time.Now().UnixNano()), []string{"admin"})
	cookie, p := login(t, router, name)
	for iteration := 0; iteration < 5; iteration++ {
		w := workflowRequest(router, "POST", "/api/v1/governance/workspaces", jsonBody(t, map[string]interface{}{"name": "归档竞争"}), cookie, p.CSRFToken, fmt.Sprintf("race-space-%s-%d", uid, iteration))
		if w.Code != 201 {
			t.Fatal(w.Body.String())
		}
		var space workflow.GovernanceWorkspace
		if e := json.Unmarshal(w.Body.Bytes(), &space); e != nil {
			t.Fatal(e)
		}
		resourceID := fmt.Sprintf("race-input-%s-%d", uid, iteration)
		fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
		if e := db.Create(&workflow.Record{Kind: "table_resource", ID: resourceID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: "归档检查来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: resourceID, Fields: fields})})}).Error; e != nil {
			t.Fatal(e)
		}
		resource := workflow.Resource{Type: "table_resource", ID: resourceID, Version: 1}
		w = workflowRequest(router, "POST", "/api/v1/governance/workspaces/"+space.ID+"/sources", jsonBody(t, map[string]interface{}{"workspace_id": space.ID, "resource": resource}), cookie, p.CSRFToken, fmt.Sprintf("race-source-%s-%d", uid, iteration))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var wg sync.WaitGroup
		wg.Add(2)
		codes := [2]int{}
		go func() {
			defer wg.Done()
			response := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, map[string]interface{}{"workspace_id": space.ID, "resource": resource, "fields": []string{"id"}, "mode": "sample", "sample_rows": 20}), cookie, p.CSRFToken, fmt.Sprintf("race-profile-%s-%d", uid, iteration))
			codes[0] = response.Code
		}()
		go func() {
			defer wg.Done()
			response := workflowRequest(router, "POST", "/api/v1/governance/workspaces/"+space.ID+"/archive", jsonBody(t, map[string]interface{}{"base_version": 1}), cookie, p.CSRFToken, fmt.Sprintf("race-archive-%s-%d", uid, iteration))
			codes[1] = response.Code
		}()
		wg.Wait()
		if !((codes[0] == 201 && codes[1] == 409) || (codes[0] == 409 && codes[1] == 200)) {
			t.Fatalf("archive and queue did not serialize: %v", codes)
		}
		var current workflow.GovernanceWorkspace
		if e := db.Where("id=?", space.ID).Take(&current).Error; e != nil {
			t.Fatal(e)
		}
		var runs []workflow.GovernanceRun
		if e := db.Where("workspace_id=? AND state IN ('queued','running')", space.ID).Find(&runs).Error; e != nil {
			t.Fatal(e)
		}
		if current.State == "archived" && len(runs) != 0 {
			t.Fatal("archived space has a newly queued task")
		}
		for _, run := range runs {
			w = workflowRequest(router, "POST", "/api/v1/governance/runs/"+run.ID+"/cancel", jsonBody(t, map[string]interface{}{"workspace_id": space.ID}), cookie, p.CSRFToken, "race-cancel-"+run.ID)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
		}
	}
}

func TestGovernanceWorkspaceConcurrencyFairness(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "space_fair_", "spacefair-"+fmt.Sprint(time.Now().UnixNano()), []string{"admin"})
	cookie, p := login(t, router, name)
	var spaces []workflow.GovernanceWorkspace
	for i := 0; i < 2; i++ {
		w := workflowRequest(router, "POST", "/api/v1/governance/workspaces", jsonBody(t, map[string]interface{}{"name": fmt.Sprint("并发空间", i), "max_concurrent_runs": 1}), cookie, p.CSRFToken, fmt.Sprintf("fair-space-%s-%d", uid, i))
		if w.Code != 201 {
			t.Fatal(w.Body.String())
		}
		var space workflow.GovernanceWorkspace
		if e := json.Unmarshal(w.Body.Bytes(), &space); e != nil {
			t.Fatal(e)
		}
		spaces = append(spaces, space)
	}
	now := time.Now().UTC()
	rows := []workflow.GovernanceRun{{ID: "fair-active-" + uid, WorkspaceID: spaces[0].ID, DepartmentID: p.DepartmentID, ActorID: uid, Kind: "profile", State: "running", Definition: json.RawMessage(`{}`), Result: json.RawMessage(`null`), LeaseOwner: "fixture", LeaseExpiresAt: now.Add(time.Minute)}, {ID: "fair-queued-a-" + uid, WorkspaceID: spaces[0].ID, DepartmentID: p.DepartmentID, ActorID: uid, Kind: "profile", State: "queued", Definition: json.RawMessage(`{}`), Result: json.RawMessage(`null`), CreatedAt: now.Add(-time.Minute)}, {ID: "fair-queued-b-" + uid, WorkspaceID: spaces[1].ID, DepartmentID: p.DepartmentID, ActorID: uid, Kind: "profile", State: "queued", Definition: json.RawMessage(`{}`), Result: json.RawMessage(`null`), CreatedAt: now}}
	if e := db.Create(&rows).Error; e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Model(&workflow.GovernanceRun{}).Where("actor_id=? AND id LIKE 'fair-%'", uid).Updates(map[string]interface{}{"state": "cancelled", "cancel_requested": true, "finished_at": time.Now().UTC()}).Error; e != nil {
			t.Error(e)
		}
	})
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		var a, b workflow.GovernanceRun
		if e := db.Where("id=?", rows[1].ID).Take(&a).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Where("id=?", rows[2].ID).Take(&b).Error; e != nil {
			t.Fatal(e)
		}
		if a.State != "queued" {
			t.Fatal("full space was scheduled")
		}
		if b.State != "queued" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("another space was starved by the full space")
		}
	}
}

func TestGovernanceWorkspaceLegacyInsertCompatibility(t *testing.T) {
	db, service, _ := fixture(t, false)
	uid, _ := businessUser(t, service, "space_legacy_", "spacelegacy-"+fmt.Sprint(time.Now().UnixNano()), []string{"governor"})
	var department string
	if e := db.Table("users").Where("id=?", uid).Select("department_id").Scan(&department).Error; e != nil {
		t.Fatal(e)
	}
	row := workflow.GovernanceDefinitionRecord{ID: "legacy-model-" + uid, DepartmentID: department, Kind: "model", LatestVersion: 1}
	if e := db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Where("id=?", row.ID).Take(&row).Error; e != nil || row.WorkspaceID == nil {
		t.Fatal("legacy insert lost default space")
	}
	var count int64
	if e := db.Model(&workflow.GovernanceWorkspace{}).Where("department_id=? AND is_legacy", department).Count(&count).Error; e != nil || count != 1 {
		t.Fatal("legacy workspace not unique")
	}
	dataset := workflow.Record{Kind: "dataset", ID: "legacy-result-" + uid, DepartmentID: department, OwnerID: uid, State: "draft", Version: 1, Body: json.RawMessage(`{"definition":{"name":"历史成果"}}`)}
	if e := db.Create(&dataset).Error; e != nil {
		t.Fatal(e)
	}
	var mapped string
	if e := db.Table("governance_workspace_legacy_datasets").Where("dataset_id=?", dataset.ID).Select("workspace_id").Scan(&mapped).Error; e != nil || mapped != *row.WorkspaceID {
		t.Fatal("legacy dataset not mapped")
	}
	var reloaded workflow.Record
	if e := db.Where("kind='dataset' AND id=?", dataset.ID).Take(&reloaded).Error; e != nil || !strings.Contains(string(reloaded.Body), "历史成果") {
		t.Fatal("migration changed original dataset body")
	}
}
