package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
)

func TestPublicTestAccounts(t *testing.T) {
	db, service, fullRouter := fixture(t, false)
	id, name := businessUser(t, service, "experience_", "experience-dept", []string{"consumer"})
	// Business fixtures grant all platforms; public experiences must be restricted explicitly.
	if e := db.Where("user_id=? AND platform NOT IN ('exchange','assets')", id).Delete(&identity.PlatformAccess{}).Error; e != nil {
		t.Fatal(e)
	}
	if e := service.RestrictPublicTestUser(t.Context(), name); e != nil {
		t.Fatal(e)
	}
	folder := t.TempDir()
	path := filepath.Join(folder, "accounts.json")
	raw := jsonBody(t, map[string]interface{}{"items": []map[string]interface{}{{"username": name, "password": password, "label": "资产与目录体验", "description": "浏览和申请", "version": 2}}})
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	handler := identity.NewHandler(service, origin)
	if e := handler.ConfigurePublicTestAccounts(path); e != nil {
		t.Fatal(e)
	}
	router := gin.New()
	router.GET("/auth/test-accounts", handler.PublicTestAccounts)
	visible := func(want int) {
		t.Helper()
		response := request(router, "GET", "/auth/test-accounts", "", nil, "", "")
		if response.Code != 200 {
			t.Fatalf("test account status %d", response.Code)
		}
		var out struct {
			Items []identity.PublicTestAccount `json:"items"`
		}
		if e := json.Unmarshal(response.Body.Bytes(), &out); e != nil || len(out.Items) != want {
			t.Fatalf("unexpected public account count")
		}
	}
	visible(1)
	cookie, principal := login(t, fullRouter, name)
	if !principal.PublicTest {
		t.Fatal("test identity lacks persistent boundary")
	}
	for _, endpoint := range []string{"/api/v1/sources", "/api/v1/assets/grants", "/api/v1/governance/runs", "/api/v1/platforms/assets/access"} {
		response := request(fullRouter, "GET", endpoint, "", cookie, "", origin)
		if response.Code != 403 || !strings.Contains(response.Body.String(), "EXPERIENCE_READ_ONLY") {
			t.Fatalf("protected read allowed: %s", endpoint)
		}
	}
	response := request(fullRouter, "POST", "/api/v1/applications", `{}`, cookie, principal.CSRFToken, origin)
	if response.Code != 403 || !strings.Contains(response.Body.String(), "EXPERIENCE_READ_ONLY") {
		t.Fatal("public identity could reach mutation")
	}
	if response := request(fullRouter, "GET", "/api/v1/me", "", cookie, "", origin); response.Code != 200 {
		t.Fatal("safe identity view blocked")
	}

	if e := db.Create(&identity.UserRole{UserID: id, Role: "admin"}).Error; e != nil {
		t.Fatal(e)
	}
	visible(0)
	response = request(fullRouter, "POST", "/api/v1/applications", `{}`, cookie, principal.CSRFToken, origin)
	if response.Code != 403 || !strings.Contains(response.Body.String(), "EXPERIENCE_READ_ONLY") {
		t.Fatal("elevated role bypassed permanent experience boundary")
	}
	if e := db.Where("user_id=? AND role='admin'", id).Delete(&identity.UserRole{}).Error; e != nil {
		t.Fatal(e)
	}
	limited := false
	for i := 0; i < 65; i++ {
		if request(fullRouter, "GET", "/api/v1/me", "", cookie, "", origin).Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("public identity has no request quota")
	}
	if e := db.Model(&identity.User{}).Where("id=?", id).Update("version", 3).Error; e != nil {
		t.Fatal(e)
	}
	visible(0)
	if e := db.Model(&identity.User{}).Where("id=?", id).Updates(map[string]interface{}{"version": 2, "active": false}).Error; e != nil {
		t.Fatal(e)
	}
	visible(0)
	empty := identity.NewHandler(service, origin)
	if e := empty.ConfigurePublicTestAccounts(""); e != nil {
		t.Fatal(e)
	}
	router = gin.New()
	router.GET("/auth/test-accounts", empty.PublicTestAccounts)
	visible(0)
}
