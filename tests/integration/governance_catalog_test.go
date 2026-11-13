package integration

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceCatalogLifecycle(t *testing.T) {
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "gov_", "governance-test", []string{"governor"})
	_, otherName := businessUser(t, service, "govother_", "other-department", []string{"governor"})
	_, consumerName := businessUser(t, service, "govconsumer_", "governance-test", []string{"consumer"})
	cookie, actor := login(t, router, ownerName)
	other, otherActor := login(t, router, otherName)
	consumer, _ := login(t, router, consumerName)
	sequence := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		sequence++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("gov-", sequence))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	idOf := func(v map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if err := json.Unmarshal(v["id"], &id); err != nil || id == "" {
			t.Fatal("missing definition id")
		}
		return id
	}
	domain := workflow.GovernanceDefinition{Name: "客户主题域", Description: "客户数据责任", OwnerID: ownerID, StewardID: ownerID}
	domainID := idOf(post("domains", map[string]interface{}{"definition": domain, "change_note": "首次建立"}, 201))
	post("domains/"+domainID+"/versions/1/publish", map[string]interface{}{}, 200)
	standard := workflow.GovernanceDefinition{Name: "客户名称", OwnerID: ownerID, StewardID: ownerID, Domain: &workflow.GovernanceRef{ID: domainID, Version: 1}, Standard: &workflow.GovernanceStandard{Code: "customer.name", LogicalType: "string", Length: 100, Nullable: true, Sensitive: true}}
	standardID := idOf(post("standards", map[string]interface{}{"definition": standard, "change_note": "名称标准"}, 201))
	post("standards/"+standardID+"/versions/1/publish", map[string]interface{}{}, 200)
	duplicateID := idOf(post("standards", map[string]interface{}{"definition": standard}, 201))
	post("standards/"+duplicateID+"/versions/1/publish", map[string]interface{}{}, 409)

	model := workflow.GovernanceDefinition{Name: "客户物理模型", OwnerID: ownerID, StewardID: ownerID, Domain: &workflow.GovernanceRef{ID: domainID, Version: 1}, Model: &workflow.GovernanceModel{Layer: "physical", Schema: "public", Table: "customers", Fields: []workflow.GovernanceField{{Name: "customer_id", LogicalType: "integer", PrimaryKey: true}, {Name: "customer_name", LogicalType: "string", Length: 100, Nullable: true, Sensitive: true, Standard: &workflow.GovernanceRef{ID: standardID, Version: 1}}}}}
	parent := workflow.GovernanceDefinition{Name: "父级客户", OwnerID: ownerID, StewardID: ownerID, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}}}}
	parentID := idOf(post("models", map[string]interface{}{"definition": parent}, 201))
	post("models/"+parentID+"/versions/1/publish", map[string]interface{}{}, 200)
	model.Model.Relations = []workflow.GovernanceRelation{{Name: "parent_customer", Target: workflow.GovernanceRef{ID: parentID, Version: 1}, Fields: []string{"customer_name"}, TargetFields: []string{"id"}, Cardinality: "many_to_one"}}
	post("models", map[string]interface{}{"definition": model}, 400)
	model.Model.Relations[0].Fields = []string{"customer_id"}
	modelID := idOf(post("models", map[string]interface{}{"definition": model, "change_note": "首次建立"}, 201))
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	post("models/"+parentID+"/versions/1/retire", map[string]interface{}{}, 409)
	// Publishing a reference pins version one even when a new draft exists.
	post("standards/"+standardID+"/versions", map[string]interface{}{"base_version": 1, "definition": standard, "change_note": "说明修订"}, 201)
	post("standards/"+standardID+"/versions/1/retire", map[string]interface{}{}, 409)
	w := workflowRequest(router, "GET", "/api/v1/governance/standards?state=published", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), standardID) || strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("published reference selection: %d %s", w.Code, w.Body.String())
	}

	model.Model.Fields[1].Sensitive = false
	post("models/"+modelID+"/versions", map[string]interface{}{"base_version": 1, "definition": model, "change_note": "非法降低敏感性"}, 400)
	model.Model.Fields[1].Sensitive = true
	model.Model.Fields = model.Model.Fields[:1]
	post("models/"+modelID+"/versions", map[string]interface{}{"base_version": 1, "definition": model, "change_note": "移除名称"}, 201)
	post("models/"+modelID+"/versions/2/publish", map[string]interface{}{}, 409)
	post("models/"+modelID+"/versions/2/publish", map[string]interface{}{"migration_note": "下游先迁移至仅客户编号输出，再启用本版本"}, 200)
	// The original definition remains intact.
	for _, path := range []string{"models/" + modelID + "/versions/1", "models/" + modelID + "/diff?from=1&to=2", "models/" + modelID + "/versions/1/ddl?dialect=postgres", "models/" + modelID + "/versions", "members"} {
		w := workflowRequest(router, "GET", "/api/v1/governance/"+path, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("GET %s=%d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(path, "/versions/1") && !strings.Contains(path, "ddl") && !strings.Contains(w.Body.String(), "customer_name") {
			t.Fatal("historical version overwritten")
		}
	}
	if w := workflowRequest(router, "GET", "/api/v1/governance/models/"+modelID+"/versions/1", nil, other, "", ""); w.Code != 403 {
		t.Fatalf("cross department read=%d", w.Code)
	}
	if w := workflowRequest(router, "GET", "/api/v1/governance/models", nil, consumer, "", ""); w.Code != 403 {
		t.Fatalf("consumer=%d", w.Code)
	}
	if w := workflowRequest(router, "POST", "/api/v1/governance/models/"+modelID+"/versions/2/retire", []byte("{}"), other, otherActor.CSRFToken, "other-retire"); w.Code != 403 {
		t.Fatalf("cross department write=%d", w.Code)
	}

	inactiveID, _ := businessUser(t, service, "inactive_steward_", "governance-test", []string{"governor"})
	if err := db.Table("users").Where("id=?", inactiveID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	model.StewardID = inactiveID
	post("models", map[string]interface{}{"definition": model}, 400)
	model.StewardID = ownerID
	model.OwnerID = "missing-owner"
	post("models", map[string]interface{}{"definition": model}, 400)
	model.OwnerID = ownerID
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- workflowRequest(router, "POST", "/api/v1/governance/models/"+modelID+"/versions", jsonBody(t, map[string]interface{}{"base_version": 2, "definition": model, "change_note": "并发版本"}), cookie, actor.CSRFToken, fmt.Sprint("concurrent-gov-", i))
		}(i)
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for result := range results {
		counts[result.Code]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent versions=%v", counts)
	}
	var audits int64
	if err := db.Table("audit_records").Where("actor_id=? AND action LIKE 'governance_%'", ownerID).Count(&audits).Error; err != nil || audits < 8 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
}
