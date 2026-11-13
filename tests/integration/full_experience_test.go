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

func TestFullExperiencePublicIdentity(t *testing.T) {
	db, service, _ := fixture(t, false)
	id, name := businessUser(t, service, "full_exp_", "experience-dept", []string{"consumer"})
	if err := service.RestrictPublicTestUser(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&identity.UserRole{UserID: id, Role: "operator"}).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "accounts.json")
	raw := jsonBody(t, map[string]interface{}{"items": []map[string]interface{}{{"username": name, "password": password, "label": "Real experience", "version": 2, "mode": "full"}}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h := identity.NewHandler(service, origin)
	if err := h.ConfigurePublicTestAccounts(path); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/auth/login", h.Login)
	router.GET("/auth/test-accounts", h.PublicTestAccounts)
	protected := router.Group("/api/v1", h.Authenticate)
	protected.GET("/me", h.Me)
	protected.GET("/platforms/:platform/access", h.PlatformAccess)
	writes := 0
	protected.POST("/write", h.CSRF, func(c *gin.Context) { writes++; c.Status(204) })
	visible := func(want int) {
		t.Helper()
		response := request(router, "GET", "/auth/test-accounts", "", nil, "", "")
		var out struct {
			Items []identity.PublicTestAccount `json:"items"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil || len(out.Items) != want {
			t.Fatal("unexpected public credential visibility")
		}
	}
	visible(1)
	cookie, p := login(t, router, name)
	if p.PublicTest || !p.FullExperience {
		t.Fatal("login did not project full experience")
	}
	me := func(wantFull, wantPublic bool) {
		t.Helper()
		response := request(router, "GET", "/api/v1/me", "", cookie, "", origin)
		var out identity.Principal
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.FullExperience != wantFull || out.PublicTest != wantPublic {
			t.Fatal("current database permissions not reflected by Me")
		}
	}
	me(true, false)
	for _, platform := range []string{"exchange", "assets", "governance", "security"} {
		if response := request(router, "GET", "/api/v1/platforms/"+platform+"/access", "", cookie, "", origin); response.Code != 200 {
			t.Fatal("normal platform access blocked")
		}
	}
	if response := request(router, "POST", "/api/v1/write", "{}", cookie, "", origin); response.Code != 403 || !strings.Contains(response.Body.String(), "CSRF_DENIED") {
		t.Fatal("CSRF protection bypassed")
	}
	if response := request(router, "POST", "/api/v1/write", "{}", cookie, p.CSRFToken, origin); response.Code != 204 || writes != 1 {
		t.Fatal("valid write blocked")
	}
	// Role drift must immediately restore the legacy boundary, even for existing sessions.
	if err := db.Create(&identity.UserRole{UserID: id, Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	visible(0)
	me(false, true)
	if response := request(router, "POST", "/api/v1/write", "{}", cookie, p.CSRFToken, origin); response.Code != 403 || !strings.Contains(response.Body.String(), "EXPERIENCE_READ_ONLY") {
		t.Fatal("admin role bypassed public boundary")
	}
	if err := db.Where("user_id=? AND role='admin'", id).Delete(&identity.UserRole{}).Error; err != nil {
		t.Fatal(err)
	}
	me(true, false)
	if err := db.Model(&identity.User{}).Where("id=?", id).Update("version", 3).Error; err != nil {
		t.Fatal(err)
	}
	visible(0)
	me(false, true)
	if err := db.Model(&identity.User{}).Where("id=?", id).Updates(map[string]interface{}{"version": 2, "public_test": false}).Error; err != nil {
		t.Fatal(err)
	}
	visible(0)
	denied := func() {
		t.Helper()
		if response := request(router, "GET", "/api/v1/me", "", cookie, "", origin); response.Code != 401 {
			t.Fatal("configured public identity without marker remained authenticated")
		}
		if response := request(router, "POST", "/api/v1/write", "{}", cookie, p.CSRFToken, origin); response.Code != 401 {
			t.Fatal("configured public identity without marker could write")
		}
	}
	denied()
	var sessionsBefore int64
	if err := db.Model(&identity.Session{}).Where("user_id=?", id).Count(&sessionsBefore).Error; err != nil {
		t.Fatal(err)
	}
	loginBody := string(jsonBody(t, map[string]string{"username": name, "password": password}))
	if response := request(router, "POST", "/auth/login", loginBody, nil, "", origin); response.Code != 401 {
		t.Fatal("configured public identity without marker could log in")
	}
	var sessionsAfter int64
	if err := db.Model(&identity.Session{}).Where("user_id=?", id).Count(&sessionsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if sessionsAfter != sessionsBefore {
		t.Fatal("denied login issued a session")
	}
	if err := db.Create(&identity.UserRole{UserID: id, Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	visible(0)
	denied()
	_, privateName := businessUser(t, service, "private_admin_", "experience-dept", []string{"admin"})
	privateCookie, privatePrincipal := login(t, router, privateName)
	if privatePrincipal.PublicTest || privatePrincipal.FullExperience {
		t.Fatal("unconfigured private admin projected as public")
	}
	if response := request(router, "GET", "/api/v1/me", "", privateCookie, "", origin); response.Code != 200 {
		t.Fatal("unconfigured private admin authentication affected")
	}
	if err := db.Model(&identity.User{}).Where("id=?", id).Updates(map[string]interface{}{"public_test": true, "active": false}).Error; err != nil {
		t.Fatal(err)
	}
	visible(0)
	if response := request(router, "GET", "/api/v1/me", "", cookie, "", origin); response.Code != 401 {
		t.Fatal("disabled identity remained authenticated")
	}
}
