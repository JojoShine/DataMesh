package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"testing"
)

func TestGovernanceFlowLayouts(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "layout_owner_", "layout-dept", []string{"governor"})
	cookie, actor := login(t, router, name)
	id := "layout-" + uid
	body := jsonBody(t, map[string]interface{}{"name": "布局验证", "owner_id": uid, "steward_id": uid, "dataflow": map[string]interface{}{"nodes": []map[string]interface{}{{"id": "source", "kind": "source", "inputs": []interface{}{}}, {"id": "output", "kind": "output", "inputs": []map[string]string{{"node": "source"}}}}}})
	if e := db.Create(&workflow.GovernanceDefinitionRecord{ID: id, Kind: "dataflow", DepartmentID: actor.DepartmentID, LatestVersion: 1}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&workflow.GovernanceVersion{DefinitionID: id, Version: 1, State: "draft", Definition: body, CreatedBy: uid}).Error; e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/governance/dataflows/" + id + "/versions/1/layout"
	before := workflowRequest(router, "GET", path, nil, cookie, "", "")
	if before.Code != 200 {
		t.Fatal(before.Body.String())
	}
	serial := 0
	put := func(revision int, positions map[string]interface{}, want int) {
		t.Helper()
		serial++
		w := workflowRequest(router, "PUT", path, jsonBody(t, map[string]interface{}{"revision": revision, "positions": positions, "viewport": map[string]interface{}{"x": 0, "y": 0, "zoom": 1}}), cookie, actor.CSRFToken, fmt.Sprint("layout-", serial))
		if w.Code != want {
			t.Fatalf("PUT %d %s", w.Code, w.Body.String())
		}
	}
	positions := map[string]interface{}{"source": map[string]int{"x": 120, "y": 50}}
	put(0, positions, 200)
	put(0, positions, 409)
	put(1, map[string]interface{}{"unknown": map[string]int{"x": 0, "y": 0}}, 400)
	put(1, positions, 200)
	after := workflowRequest(router, "GET", path, nil, cookie, "", "")
	var saved struct {
		Revision int `json:"revision"`
	}
	if e := json.Unmarshal(after.Body.Bytes(), &saved); e != nil || saved.Revision != 2 {
		t.Fatal(after.Body.String())
	}
	var version workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=1", id).Take(&version).Error; e != nil {
		t.Fatal(e)
	}
	var original, next interface{}
	_ = json.Unmarshal(body, &original)
	_ = json.Unmarshal(version.Definition, &next)
	if string(jsonBody(t, original)) != string(jsonBody(t, next)) {
		t.Fatal("layout altered business definition")
	}
	_, other := businessUser(t, service, "layout_other_", "layout-dept", []string{"governor"})
	otherCookie, _ := login(t, router, other)
	otherView := workflowRequest(router, "GET", path, nil, otherCookie, "", "")
	if otherView.Code != 200 || !containsLayoutRevision(otherView.Body.Bytes(), 0) {
		t.Fatal("layout leaked across users")
	}
	_, foreign := businessUser(t, service, "layout_foreign_", "other-dept", []string{"governor"})
	foreignCookie, _ := login(t, router, foreign)
	denied := workflowRequest(router, "GET", path, nil, foreignCookie, "", "")
	if denied.Code != 403 {
		t.Fatalf("foreign read %d", denied.Code)
	}
}
func containsLayoutRevision(body []byte, revision int) bool {
	var row struct {
		Revision int `json:"revision"`
	}
	return json.Unmarshal(body, &row) == nil && row.Revision == revision
}
