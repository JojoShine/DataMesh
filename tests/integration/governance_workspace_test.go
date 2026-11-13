package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceWorkspaceDefinitionScope(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "space_owner_", "space-"+fmt.Sprint(time.Now().UnixNano()), []string{"governor"})
	cookie, actor := login(t, router, name)
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("workspace-%s-%d", uid, serial))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	idOf := func(v map[string]json.RawMessage) string {
		var id string
		if e := json.Unmarshal(v["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	a := idOf(post("workspaces", map[string]interface{}{"name": "客户整理"}, 201))
	b := idOf(post("workspaces", map[string]interface{}{"name": "商品整理"}, 201))
	for _, id := range []string{a, b} {
		w := workflowRequest(router, "GET", "/api/v1/governance/workspaces/"+id+"/overview", nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var overview map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &overview); e != nil {
			t.Fatal(e)
		}
		for _, key := range []string{"run_states", "recent_runs", "recent_results"} {
			if string(overview[key]) != "[]" {
				t.Fatalf("empty overview %s must be [], got %s", key, overview[key])
			}
		}
	}

	model := map[string]interface{}{"name": "客户模型", "owner_id": uid, "steward_id": uid, "model": map[string]interface{}{"layer": "logical", "fields": []map[string]interface{}{{"name": "id", "logical_type": "integer", "primary_key": true}}}}
	mid := idOf(post("models", map[string]interface{}{"workspace_id": a, "definition": model}, 201))
	post("models/"+mid+"/versions/1/publish", map[string]interface{}{"workspace_id": a}, 200)
	graph := map[string]interface{}{"name": "跨空间引用", "owner_id": uid, "steward_id": uid, "model": map[string]interface{}{"layer": "logical", "fields": []map[string]interface{}{{"name": "id", "logical_type": "integer", "primary_key": true}}, "relations": []map[string]interface{}{{"name": "customer", "target": workflow.GovernanceRef{ID: mid, Version: 1}, "fields": []string{"id"}, "target_fields": []string{"id"}, "cardinality": "many_to_one"}}}}
	post("models", map[string]interface{}{"workspace_id": b, "definition": graph}, 403)
	post("models/"+mid+"/versions", map[string]interface{}{"base_version": 1, "definition": model}, 409)
	post("models/"+mid+"/versions", map[string]interface{}{"workspace_id": b, "base_version": 1, "definition": model}, 403)
	post("models/"+mid+"/versions", map[string]interface{}{"workspace_id": a, "base_version": 1, "definition": model}, 201)
	for _, v := range []struct {
		id       string
		contains bool
	}{{a, true}, {b, false}} {
		w := workflowRequest(router, "GET", "/api/v1/governance/models?workspace_id="+v.id, nil, cookie, "", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), mid) != v.contains {
			t.Fatalf("list scope: %d %s", w.Code, w.Body.String())
		}
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/models/"+mid+"/versions/1", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), a) {
		t.Fatal("old deep link must resolve actual space")
	}
	legacyID := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	w = workflowRequest(router, "GET", "/api/v1/governance/models", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), legacyID) || strings.Contains(w.Body.String(), mid) {
		t.Fatal("unscoped list must stay in legacy space")
	}
	var before workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=1", mid).Take(&before).Error; e != nil {
		t.Fatal(e)
	}
	post("workspaces/"+a+"/archive", map[string]interface{}{"base_version": 1}, 200)
	post("models/"+mid+"/versions", map[string]interface{}{"workspace_id": a, "base_version": 2, "definition": model}, 409)
	var after workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=1", mid).Take(&after).Error; e != nil || string(before.Definition) != string(after.Definition) {
		t.Fatal("archive changed version evidence")
	}
	_, otherName := businessUser(t, service, "space_other_", "other-"+uid, []string{"governor"})
	other, _ := login(t, router, otherName)
	if w := workflowRequest(router, "GET", "/api/v1/governance/workspaces/"+a, nil, other, "", ""); w.Code != 403 {
		t.Fatalf("cross department read: %d", w.Code)
	}
	if w := workflowRequest(router, "GET", "/api/v1/governance/models?workspace_id="+b, nil, other, "", ""); w.Code != 403 {
		t.Fatalf("forged scope: %d", w.Code)
	}
	for _, path := range []string{"operations?workspace_id=", "metadata?workspace_id="} {
		if w := workflowRequest(router, "GET", "/api/v1/governance/"+path+b, nil, other, "", ""); w.Code != 403 {
			t.Fatalf("forged scope reached %s: %d", path, w.Code)
		}
	}

}

func TestGovernanceWorkspaceExecutionScope(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "space_exec_", "spaceexec-"+fmt.Sprint(time.Now().UnixNano()), []string{"admin"})
	cookie, actor := login(t, router, name)
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("spaceexec-%s-%d", uid, serial))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var v map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	idOf := func(v map[string]json.RawMessage) string {
		var id string
		if e := json.Unmarshal(v["id"], &id); e != nil || id == "" {
			t.Fatal("id")
		}
		return id
	}
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}}
	resourceID := "space-source-" + uid
	if e := db.Create(&workflow.ImportRow{BatchID: resourceID, RowNumber: 1, Data: json.RawMessage(`{"id":1,"name":" Alice "}`)}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: resourceID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: "客户来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: resourceID, Fields: fields})})}).Error; e != nil {
		t.Fatal(e)
	}
	resource := workflow.Resource{Type: "table_resource", ID: resourceID, Version: 1}
	var spaces, flows, runs, models []string
	for _, label := range []string{"客户整理", "商品整理"} {
		space := idOf(post("workspaces", map[string]interface{}{"name": label}, 201))
		spaces = append(spaces, space)
		for _, item := range []struct {
			path  string
			count int
		}{
			{"metadata?workspace_id=" + space, 0},
			{"workspaces/" + space + "/source-candidates", 1},
		} {
			w := workflowRequest(router, "GET", "/api/v1/governance/"+item.path, nil, cookie, "", "")
			var page struct {
				Items []workflow.MetadataResource `json:"items"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != item.count {
				t.Fatalf("source scope %s=%d %s", item.path, w.Code, w.Body.String())
			}
		}
		blocked := workflowRequest(router, "GET", "/api/v1/governance/metadata/"+resourceID+"/observations?workspace_id="+space, nil, cookie, "", "")
		if blocked.Code != 409 {
			t.Fatalf("unassociated metadata=%d", blocked.Code)
		}
		post("workspaces/"+space+"/sources", map[string]interface{}{"workspace_id": space, "resource": resource}, 200)
		model := map[string]interface{}{"name": label + "模型", "owner_id": uid, "steward_id": uid, "model": map[string]interface{}{"layer": "logical", "fields": fields}}
		mid := idOf(post("models", map[string]interface{}{"workspace_id": space, "definition": model}, 201))
		models = append(models, mid)
		post("models/"+mid+"/versions/1/publish", map[string]interface{}{"workspace_id": space}, 200)
		nodes := []map[string]interface{}{{"id": "source", "kind": "source", "inputs": []interface{}{}, "source": map[string]interface{}{"resource": resource, "fields": []string{"id", "name"}}}, {"id": "clean", "kind": "transform", "inputs": []map[string]string{{"node": "source"}}, "columns": []map[string]string{{"source": "id", "target": "id", "rule": "copy"}, {"source": "name", "target": "name", "rule": "trim"}}}, {"id": "output", "kind": "output", "inputs": []map[string]string{{"node": "clean"}}, "model": workflow.GovernanceRef{ID: mid, Version: 1}}}
		def := map[string]interface{}{"name": label, "owner_id": uid, "steward_id": uid, "dataflow": map[string]interface{}{"nodes": nodes}}
		flow := idOf(post("dataflows", map[string]interface{}{"workspace_id": space, "definition": def}, 201))
		flows = append(flows, flow)
		post("dataflows/"+flow+"/versions/1/publish", map[string]interface{}{"workspace_id": space}, 200)
		post("dataflows/"+flow+"/versions/1/run", map[string]interface{}{}, 409)
		run := idOf(post("dataflows/"+flow+"/versions/1/run", map[string]interface{}{"workspace_id": space}, 201))
		runs = append(runs, run)
		waitWorkspaceRun(t, router, cookie, space, run)
	}
	for i, space := range spaces {
		w := workflowRequest(router, "GET", "/api/v1/governance/runs?workspace_id="+space, nil, cookie, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), runs[i]) || strings.Contains(w.Body.String(), runs[1-i]) {
			t.Fatal("run list mixed spaces: " + w.Body.String())
		}
		w = workflowRequest(router, "GET", "/api/v1/datasets?workspace_id="+space, nil, cookie, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), runs[i]) || strings.Contains(w.Body.String(), runs[1-i]) {
			t.Fatal("dataset list mixed spaces: " + w.Body.String())
		}
		w = workflowRequest(router, "GET", "/api/v1/governance/workspaces/"+space+"/overview", nil, cookie, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), runs[i]) || strings.Contains(w.Body.String(), runs[1-i]) {
			t.Fatal("overview mixed spaces: " + w.Body.String())
		}
	}

	// New sources come from exchange resources; historical direct-result references remain executable.
	derived := workflow.Resource{Type: "governed_dataset", ID: runs[0], Version: 1}
	post("workspaces/"+spaces[1]+"/sources", map[string]interface{}{"workspace_id": spaces[1], "resource": derived}, 409)
	for _, action := range []string{"publish"} {
		w := workflowRequest(router, "POST", "/api/v1/datasets/"+runs[0]+"/"+action+"?workspace_id="+spaces[0], []byte(`{}`), cookie, actor.CSRFToken, "space-result-"+action+uid)
		if w.Code != 200 {
			t.Fatalf("result %s: %d %s", action, w.Code, w.Body.String())
		}
	}
	post("workspaces/"+spaces[1]+"/sources", map[string]interface{}{"workspace_id": spaces[1], "resource": derived}, 409)
	// Simulate a reference created before the exchange-only intake policy.
	if e := db.Create(&workflow.GovernanceWorkspaceSource{WorkspaceID: spaces[1], ResourceType: derived.Type, ResourceID: derived.ID, ContractVersion: 1, ActorID: uid, Active: true}).Error; e != nil {
		t.Fatal(e)
	}
	reuseDefinition := map[string]interface{}{"name": "复用已发布客户成果", "owner_id": uid, "steward_id": uid, "dataflow": map[string]interface{}{"nodes": []map[string]interface{}{
		{"id": "source", "kind": "source", "inputs": []interface{}{}, "source": map[string]interface{}{"resource": derived, "fields": []string{"id", "name"}}},
		{"id": "output", "kind": "output", "inputs": []map[string]string{{"node": "source"}}, "model": workflow.GovernanceRef{ID: models[1], Version: 1}},
	}}}
	reused := idOf(post("dataflows", map[string]interface{}{"workspace_id": spaces[1], "definition": reuseDefinition}, 201))
	post("dataflows/"+reused+"/versions/1/publish", map[string]interface{}{"workspace_id": spaces[1]}, 200)
	reusedRun := idOf(post("dataflows/"+reused+"/versions/1/run", map[string]interface{}{"workspace_id": spaces[1]}, 201))
	waitWorkspaceRun(t, router, cookie, spaces[1], reusedRun)
	w := workflowRequest(router, "POST", "/api/v1/datasets/"+runs[0]+"/withdraw?workspace_id="+spaces[0], []byte(`{}`), cookie, actor.CSRFToken, "space-result-withdraw-"+uid)
	if w.Code != 200 {
		t.Fatalf("withdraw result: %d %s", w.Code, w.Body.String())
	}
	post("dataflows/"+reused+"/versions/1/run", map[string]interface{}{"workspace_id": spaces[1]}, 409)
	post("dataflows/"+flows[0]+"/versions/1/run", map[string]interface{}{"workspace_id": spaces[1]}, 403)

	// A newer exchange version stays visible so metadata can explain drift.
	if e := db.Model(&workflow.Record{}).Where("kind='table_resource' AND id=?", resourceID).Update("version", 2).Error; e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"metadata?workspace_id=" + spaces[0], "workspaces/" + spaces[0] + "/source-candidates"} {
		w := workflowRequest(router, "GET", "/api/v1/governance/"+suffix, nil, cookie, "", "")
		var page struct {
			Items []workflow.MetadataResource `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ResourceType != "table_resource" || page.Items[0].SchemaVersion != 2 {
			t.Fatalf("exchange-only drift visibility %s=%d %s", suffix, w.Code, w.Body.String())
		}
	}
	if e := db.Model(&workflow.Record{}).Where("kind='table_resource' AND id=?", resourceID).Update("version", 1).Error; e != nil {
		t.Fatal(e)
	}

	// An actual workspace workflow propagates scope into its physical child and schedule.
	workflowDefinition := workflow.GovernanceDefinition{Name: "空间内工作流", OwnerID: uid, StewardID: uid, Workflow: &workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{{ID: "clean", Dataflow: workflow.GovernanceRef{ID: flows[1], Version: 1}}}}}
	wid := idOf(post("workflows", map[string]interface{}{"workspace_id": spaces[1], "definition": workflowDefinition}, 201))
	post("workflows/"+wid+"/versions/1/publish", map[string]interface{}{"workspace_id": spaces[1]}, 200)
	parent := idOf(post("workflows/"+wid+"/versions/1/run", map[string]interface{}{"workspace_id": spaces[1]}, 201))
	waitWorkspaceRun(t, router, cookie, spaces[1], parent)
	var tasks []workflow.GovernanceWorkflowTaskRun
	if e := db.Where("parent_run_id=?", parent).Find(&tasks).Error; e != nil || len(tasks) != 1 || tasks[0].ChildRunID == nil {
		t.Fatal("missing actual workspace child")
	}
	var child workflow.GovernanceRun
	if e := db.Where("id=?", *tasks[0].ChildRunID).Take(&child).Error; e != nil || child.WorkspaceID != spaces[1] {
		t.Fatal("workflow child lost workspace")
	}
	scheduled := idOf(post("schedules", map[string]interface{}{"workspace_id": spaces[1], "workflow": workflow.GovernanceRef{ID: wid, Version: 1}, "timezone": "UTC", "interval_seconds": 3600, "concurrency_policy": "forbid", "missed_policy": "skip", "next_run_at": time.Now().Add(time.Hour)}, 201))
	var schedule workflow.GovernanceSchedule
	if e := db.Where("id=?", scheduled).Take(&schedule).Error; e != nil || schedule.WorkspaceID != spaces[1] {
		t.Fatal("schedule lost workspace")
	}
	post("workspaces/"+spaces[0]+"/archive", map[string]interface{}{"base_version": 1}, 200)
	post("dataflows/"+flows[0]+"/versions/1/run", map[string]interface{}{"workspace_id": spaces[0]}, 409)
	run := idOf(post("dataflows/"+flows[1]+"/versions/1/run", map[string]interface{}{"workspace_id": spaces[1]}, 201))
	waitWorkspaceRun(t, router, cookie, spaces[1], run)
	var proof int64
	if e := db.Table("governance_outputs").Where("run_id=?", runs[0]).Count(&proof).Error; e != nil || proof != 1 {
		t.Fatal("archive removed output proof")
	}
}
func waitWorkspaceRun(t *testing.T, router http.Handler, cookie *http.Cookie, space, id string) {
	t.Helper()
	for deadline := time.Now().Add(25 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+id+"?workspace_id="+space, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var run workflow.GovernanceRun
		if e := json.Unmarshal(w.Body.Bytes(), &run); e != nil {
			t.Fatal(e)
		}
		if run.State == "succeeded" {
			if run.WorkspaceID != space {
				t.Fatal("run lost space")
			}
			return
		}
		if run.State == "failed" || run.State == "interrupted" {
			t.Fatal("run failed: " + w.Body.String())
		}
		if time.Now().After(deadline) {
			t.Fatal("run timed out")
		}
	}
}
