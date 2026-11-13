package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceEnterpriseDomainsAndCodeMeanings(t *testing.T) {
	db, service, router := fixture(t, false)
	adminID, adminName := businessUser(t, service, "enterprise_admin_", "enterprise-office", []string{"admin"})
	aID, aName := businessUser(t, service, "enterprise_a_", "enterprise-sales", []string{"governor"})
	bID, bName := businessUser(t, service, "enterprise_b_", "enterprise-service", []string{"governor"})
	_, peerName := businessUser(t, service, "enterprise_peer_", "enterprise-office", []string{"governor"})
	adminCookie, adminActor := login(t, router, adminName)
	adminCSRF := adminActor.CSRFToken
	aCookie, aActor := login(t, router, aName)
	aCSRF := aActor.CSRFToken
	bCookie, bActor := login(t, router, bName)
	bCSRF := bActor.CSRFToken
	peerCookie, peerActor := login(t, router, peerName)
	prefix := fmt.Sprintf("enterprise-%d", time.Now().UnixNano())
	sequence := 0
	post := func(cookie *http.Cookie, csrf, path string, body interface{}, status int) []byte {
		t.Helper()
		sequence++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, csrf, fmt.Sprintf("%s-%d", prefix, sequence))
		if w.Code != status {
			t.Fatalf("POST %s=%d want %d: %s", path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	get := func(cookie *http.Cookie, path string, status int) []byte {
		t.Helper()
		w := workflowRequest(router, "GET", "/api/v1/governance/"+path, nil, cookie, "", "")
		if w.Code != status {
			t.Fatalf("GET %s=%d want %d: %s", path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	idOf := func(body []byte) string {
		t.Helper()
		var v struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal(body, &v); e != nil || v.ID == "" {
			t.Fatal("missing definition id")
		}
		return v.ID
	}
	global := workflow.GovernanceDefinition{Name: prefix, Description: "统一客户边界，不合并来源或授予资源权限", OwnerID: adminID, StewardID: adminID, DomainScope: "enterprise"}
	write := func(v workflow.GovernanceDefinition, base int) map[string]interface{} {
		return map[string]interface{}{"definition": v, "base_version": base, "change_note": "business definition"}
	}
	post(aCookie, aCSRF, "domains", write(global, 0), 403)
	globalID := idOf(post(adminCookie, adminCSRF, "domains", write(global, 0), 201))
	get(aCookie, "domains/"+globalID+"/versions/1", 403)
	get(peerCookie, "domains/"+globalID+"/versions/1", 403)
	var list struct {
		Items []workflow.GovernanceDefinitionRecord `json:"items"`
		Total int                                   `json:"total"`
	}
	if e := json.Unmarshal(get(aCookie, "domains?search="+prefix, 200), &list); e != nil || list.Total != 0 {
		t.Fatal("enterprise draft listed")
	}
	post(adminCookie, adminCSRF, "domains/"+globalID+"/versions/1/publish", map[string]string{}, 200)
	get(aCookie, "domains/"+globalID+"/versions/1", 200)
	ref := workflow.GovernanceRef{ID: globalID, Version: 1}
	local := workflow.GovernanceDefinition{Name: prefix + "-sales", Description: "销售部负责联系资料", OwnerID: aID, StewardID: aID, DomainScope: "department", EnterpriseDomain: &ref}
	localID := idOf(post(aCookie, aCSRF, "domains", write(local, 0), 201))
	post(aCookie, aCSRF, "domains/"+localID+"/versions/1/publish", map[string]string{}, 200)
	post(aCookie, aCSRF, "domains", write(local, 0), 409)
	get(bCookie, "domains/"+localID+"/versions/1", 403)
	get(bCookie, "domains?enterprise_domain_id="+globalID+"&department_id=enterprise-sales", 403)
	if e := json.Unmarshal(get(bCookie, "domains?enterprise_domain_id="+globalID, 200), &list); e != nil || list.Total != 0 {
		t.Fatal("other department responsibility listed")
	}
	foreign := local
	foreign.Name = prefix + "-service"
	foreign.OwnerID = bID
	foreign.StewardID = bID
	foreign.Description = "售后部负责服务记录"
	foreignID := idOf(post(bCookie, bCSRF, "domains", write(foreign, 0), 201))
	post(bCookie, bCSRF, "domains/"+foreignID+"/versions/1/publish", map[string]string{}, 200)
	bad := foreign
	bad.OwnerID = aID
	post(bCookie, bCSRF, "domains/"+foreignID+"/versions", write(bad, 1), 400)
	post(aCookie, aCSRF, "domains/"+globalID+"/versions", write(global, 1), 403)
	post(aCookie, aCSRF, "domains/"+globalID+"/versions/1/retire", map[string]string{}, 403)
	post(peerCookie, peerActor.CSRFToken, "domains/"+globalID+"/versions", write(global, 1), 403)
	global.Description = "尚未发布的新口径"
	post(adminCookie, adminCSRF, "domains/"+globalID+"/versions", write(global, 1), 201)
	get(aCookie, "domains/"+globalID+"/versions/2", 403)
	get(peerCookie, "domains/"+globalID+"/versions/2", 403)
	get(aCookie, "domains/"+globalID+"/diff?from=1&to=2", 403)
	var versions struct {
		Items []struct {
			Version, LatestVersion int
			Definition             workflow.GovernanceDefinition
		} `json:"items"`
		Total int `json:"total"`
	}
	for _, cookie := range []*http.Cookie{aCookie, peerCookie} {
		if e := json.Unmarshal(get(cookie, "domains/"+globalID+"/versions", 200), &versions); e != nil || versions.Total != 1 || len(versions.Items) != 1 || versions.Items[0].Version != 1 {
			t.Fatal("history leaked enterprise draft")
		}
		var rows struct {
			Items []struct {
				Version       int `json:"version"`
				LatestVersion int `json:"latest_version"`
			} `json:"items"`
		}
		if e := json.Unmarshal(get(cookie, "domains?domain_scope=enterprise&search="+prefix, 200), &rows); e != nil || len(rows.Items) != 1 || rows.Items[0].Version != 1 || rows.Items[0].LatestVersion != 1 {
			t.Fatal("list did not pin visible published version")
		}
	}
	standard := workflow.GovernanceDefinition{Name: prefix + "-enable", Description: "通用启停标志，不绑定表", OwnerID: aID, StewardID: aID, Domain: &ref, Standard: &workflow.GovernanceStandard{Code: prefix + ".enable_flag", LogicalType: "integer", Values: []string{"0", "1"}, ValueMeanings: map[string]string{"0": "停用", "1": "启用"}}}
	standardID := idOf(post(aCookie, aCSRF, "standards", write(standard, 0), 201))
	post(aCookie, aCSRF, "standards/"+standardID+"/versions/1/publish", map[string]string{}, 200)
	get(bCookie, "standards/"+standardID+"/versions/1", 403)
	localRef := workflow.GovernanceRef{ID: foreignID, Version: 1}
	term := workflow.GovernanceDefinition{Name: prefix + "-term", Description: "不允许通过统一主题域读取其他部门定义", OwnerID: aID, StewardID: aID, Domain: &localRef}
	post(aCookie, aCSRF, "terms", write(term, 0), 403)
	for _, meanings := range []map[string]string{nil, {"0": "停用"}, {"0": "停用", "1": " "}, {"0": "停用", "2": "启用"}} {
		badStandard := standard
		copyStandard := *standard.Standard
		copyStandard.ValueMeanings = meanings
		badStandard.Standard = &copyStandard
		post(aCookie, aCSRF, "standards", write(badStandard, 0), 400)
	}
	legacy := standard
	legacy.Standard = &workflow.GovernanceStandard{Code: prefix + ".legacy", LogicalType: "integer", Values: []string{"0", "1"}}
	legacyID := prefix + "-legacy"
	if e := db.Create(&workflow.GovernanceDefinitionRecord{ID: legacyID, Kind: "standard", DepartmentID: "enterprise-sales", LatestVersion: 1}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&workflow.GovernanceVersion{DefinitionID: legacyID, Version: 1, State: "published", Definition: jsonBody(t, legacy), CreatedBy: aID}).Error; e != nil {
		t.Fatal(e)
	}
	get(aCookie, "standards/"+legacyID+"/versions/1", 200)
	legacy.Standard.ValueMeanings = map[string]string{"0": "停用", "1": "启用"}
	post(aCookie, aCSRF, "standards/"+legacyID+"/versions", write(legacy, 1), 201)
	post(aCookie, aCSRF, "standards/"+legacyID+"/versions/2/publish", map[string]string{}, 200)
	legacy.Standard.ValueMeanings["1"] = "已付款"
	post(aCookie, aCSRF, "standards/"+legacyID+"/versions", write(legacy, 2), 201)
	post(aCookie, aCSRF, "standards/"+legacyID+"/versions/3/publish", map[string]string{}, 409)
	for _, cookie := range []*http.Cookie{aCookie, bCookie, peerCookie} {
		var dashboard struct {
			Definitions []struct {
				Kind  string `json:"kind"`
				Count int    `json:"count"`
			} `json:"definitions"`
		}
		if e := json.Unmarshal(get(cookie, "dashboard?days=7", 200), &dashboard); e != nil {
			t.Fatal(e)
		}
		total := 0
		for _, row := range dashboard.Definitions {
			if row.Kind == "domain" {
				total += row.Count
			}
		}
		expected := 2
		if cookie == peerCookie {
			expected = 1
		}
		if total != expected {
			t.Fatalf("enterprise domain dashboard count=%d want %d", total, expected)
		}
	}

	var impact struct {
		Items []struct {
			DefinitionID string `json:"id"`
		} `json:"items"`
	}
	body := get(aCookie, "domains/"+globalID+"/impact?version=1", 200)
	if e := json.Unmarshal(body, &impact); e != nil {
		t.Fatal(e)
	}
	var raw []map[string]interface{}
	var parsed map[string]json.RawMessage
	if e := json.Unmarshal(body, &parsed); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(parsed["items"], &raw); e != nil {
		t.Fatal(e)
	}
	for _, row := range raw {
		if row["id"] == foreignID {
			t.Fatal("impact leaked another department responsibility")
		}
	}
	post(adminCookie, adminCSRF, "domains/"+globalID+"/versions/1/retire", map[string]string{}, 409)
}
