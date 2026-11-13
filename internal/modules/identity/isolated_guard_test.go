package identity

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestIsolatedGuardConfiguration(t *testing.T) {
	for _, name := range []string{"", CookieName} {
		h := NewHandler(nil, "https://example.test")
		if err := h.ConfigureSessionCookie(name); err != nil {
			t.Fatal(err)
		}
		if err := h.ConfigureIsolatedExperienceGuard("true"); err == nil {
			t.Fatal("enabled on main instance")
		}
	}
	h := NewHandler(nil, "https://example.test")
	if err := h.ConfigureSessionCookie("datamesh_experience_session"); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfigureIsolatedExperienceGuard("typo"); err == nil {
		t.Fatal("invalid setting accepted")
	}
	if err := h.ConfigureIsolatedExperienceGuard("true"); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedGuardNormalRolesAndFailures(t *testing.T) {
	g := newIsolatedGuard()
	now := time.Now()
	for _, role := range []string{"provider", "directory_reviewer", "operator", "governor", "consumer", "auditor", "admin"} {
		for i := 0; i < 100; i++ {
			release, _, reason := g.begin(role, true, now)
			if reason != "" {
				t.Fatalf("normal %s request blocked: %s", role, reason)
			}
			release(http.StatusOK, now)
		}
	}
	for _, status := range []int{400, 401, 404, 409, 422} {
		for i := 0; i < 25; i++ {
			release, _, reason := g.begin(fmt.Sprint(status), true, now)
			if reason != "" {
				t.Fatalf("ordinary failure classified as abuse: %d %s", status, reason)
			}
			release(status, now)
		}
	}
	for i := 0; i < 100; i++ {
		release, _, reason := g.begin("read-forbidden", false, now)
		if reason != "" {
			t.Fatal(reason)
		}
		release(403, now)
	}
}

func TestIsolatedGuardBudgetsCooldownAndReset(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		mutations bool
		count     int
		status    int
		reason    string
	}{
		{"requests", false, 600, 200, "request_budget"},
		{"mutations", true, 240, 200, "mutation_budget"},
		{"denials", true, 20, 403, "forbidden_mutations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newIsolatedGuard()
			for i := 0; i < tc.count; i++ {
				release, _, reason := g.begin("user", tc.mutations, now)
				if reason != "" {
					t.Fatalf("request %d: %s", i, reason)
				}
				release(tc.status, now)
			}
			_, retry, reason := g.begin("user", tc.mutations, now)
			if reason != tc.reason || retry != 30*time.Second {
				t.Fatalf("wrong cooldown: %s %s", reason, retry)
			}
			if tc.mutations {
				release, _, reason := g.begin("user", false, now)
				if reason != "" {
					t.Fatal("mutation cooldown blocked reads")
				}
				release(200, now)
			}
			release, _, reason := g.begin("user", tc.mutations, now.Add(31*time.Second))
			if reason != "" {
				t.Fatalf("cooldown did not restore access: %s", reason)
			}
			release(200, now)
		})
	}
}

func TestIsolatedGuardConcurrentAndBounded(t *testing.T) {
	g := newIsolatedGuard()
	now := time.Now()
	releases := []func(int, time.Time){}
	for i := 0; i < 8; i++ {
		release, _, reason := g.begin("busy", true, now)
		if reason != "" {
			t.Fatal(reason)
		}
		releases = append(releases, release)
	}
	if _, _, reason := g.begin("busy", true, now); reason != "concurrent_mutations" {
		t.Fatal(reason)
	}
	for _, release := range releases {
		release(200, now)
	}
	release, _, reason := g.begin("busy", true, now.Add(31*time.Second))
	if reason != "" {
		t.Fatal(reason)
	}
	release(200, now)
	for i := 0; i < 1023; i++ {
		release, _, reason := g.begin(fmt.Sprint(i), false, now)
		if reason != "" {
			t.Fatal(reason)
		}
		release(200, now)
	}
	if _, _, reason := g.begin("overflow", false, now); reason != "principal_capacity" {
		t.Fatal(reason)
	}
	if len(g.buckets) != 1024 {
		t.Fatal("unbounded buckets")
	}
	release, _, reason = g.begin("new", false, now.Add(3*time.Minute))
	if reason != "" {
		t.Fatal(reason)
	}
	release(200, now)
	if len(g.buckets) != 1 {
		t.Fatal("idle buckets not evicted")
	}
}

func TestIsolatedGuardBlocksBeforeUnsafeWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(nil, "https://example.test")
	h.isolatedGuard = newIsolatedGuard()
	router := gin.New()
	executed := 0
	router.POST("/mutate", func(c *gin.Context) {
		h.isolatedExperienceRequest(c, Principal{UserID: "user"})
	}, func(c *gin.Context) { executed++; c.Status(403) })
	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/mutate", nil))
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/mutate", nil))
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || executed != 20 {
		t.Fatalf("unsafe operation ran: status=%d executed=%d", w.Code, executed)
	}
}

func TestIsolatedGuardPreservesCSRFAndBlocksWork(t *testing.T) {
	h := NewHandler(&Service{csrfKey: make([]byte, 32)}, "https://example.test")
	h.isolatedGuard = newIsolatedGuard()
	router := gin.New()
	executed := 0
	router.POST("/write", func(c *gin.Context) {
		c.Set("session_token", "session")
		user := c.GetHeader("X-Test-User")
		h.isolatedExperienceRequest(c, Principal{UserID: user})
	}, h.CSRF, func(c *gin.Context) { executed++; c.Status(200) })
	request := func(user string, valid bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/write", nil)
		r.Header.Set("X-Test-User", user)
		if valid {
			r.Header.Set("Origin", h.origin)
			r.Header.Set("X-CSRF-Token", h.service.CSRF("session"))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 20; i++ {
		if w := request("abusive", false); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if w := request("abusive", true); w.Code != 429 {
		t.Fatal(w.Code)
	}
	if executed != 0 {
		t.Fatal("unsafe work executed")
	}
	if w := request("normal", true); w.Code != 200 || executed != 1 {
		t.Fatal("normal operation blocked")
	}
}

func TestIsolatedGuardDisabledAndPublicBoundary(t *testing.T) {
	h := NewHandler(nil, "https://example.test")
	for _, setting := range []string{"", "false"} {
		if err := h.ConfigureIsolatedExperienceGuard(setting); err != nil || h.isolatedGuard != nil {
			t.Fatal("default changed")
		}
	}
	h.isolatedGuard = newIsolatedGuard()
	router := gin.New()
	executed := false
	router.POST("/write", func(c *gin.Context) {
		p := Principal{UserID: "public", PublicTest: true, Roles: []string{"admin"}}
		if !h.publicTestBoundary(c, p) {
			return
		}
		h.isolatedExperienceRequest(c, p)
	}, func(c *gin.Context) { executed = true })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/write", nil))
	if w.Code != 403 || executed {
		t.Fatal("public_test boundary expanded")
	}
}

func TestIsolatedGuardForbiddenCooldownStartsAtCompletion(t *testing.T) {
	g := newIsolatedGuard()
	now := time.Now()
	for i := 0; i < 19; i++ {
		release, _, reason := g.begin("user", true, now)
		if reason != "" {
			t.Fatal(reason)
		}
		release(403, now)
	}
	release, _, reason := g.begin("user", true, now)
	if reason != "" {
		t.Fatal(reason)
	}
	finished := now.Add(40 * time.Second)
	release(403, finished)
	if _, retry, reason := g.begin("user", true, finished); reason != "forbidden_mutations" || retry != 30*time.Second {
		t.Fatalf("cooldown did not start at completion: %s %s", reason, retry)
	}
	release, _, reason = g.begin("user", true, finished.Add(31*time.Second))
	if reason != "" {
		t.Fatal("completion cooldown did not recover")
	}
	release(200, finished.Add(31*time.Second))
}

func TestIsolatedGuardExpiredForbiddenWindowAtCompletion(t *testing.T) {
	g := newIsolatedGuard()
	now := time.Now()
	for i := 0; i < 19; i++ {
		release, _, reason := g.begin("user", true, now)
		if reason != "" {
			t.Fatal(reason)
		}
		release(403, now)
	}
	release, _, reason := g.begin("user", true, now)
	if reason != "" {
		t.Fatal(reason)
	}
	finished := now.Add(70 * time.Second)
	release(403, finished)
	if g.buckets["user"].forbidden != 1 {
		t.Fatal("old denials spilled into completion window")
	}
	release, _, reason = g.begin("user", true, finished)
	if reason != "" {
		t.Fatal("stale denials triggered cooldown")
	}
	release(200, finished)
}

func TestFullExperienceMainGuardPreservesCSRFAndQuota(t *testing.T) {
	h := NewHandler(&Service{csrfKey: make([]byte, 32)}, "https://example.test")
	h.fullExperienceGuard = newIsolatedGuard()
	if h.cookieName != CookieName || h.isolatedGuard != nil {
		t.Fatal("not a main instance")
	}
	router := gin.New()
	executed := 0
	router.POST("/write", func(c *gin.Context) {
		c.Set("session_token", "session")
		p := Principal{UserID: c.GetHeader("X-Test-User"), FullExperience: true}
		if !h.publicTestBoundary(c, p) {
			return
		}
		h.isolatedExperienceRequest(c, p)
	}, h.CSRF, func(c *gin.Context) { executed++; c.Status(200) })
	request := func(user string, valid bool) int {
		r := httptest.NewRequest("POST", "/write", nil)
		r.Header.Set("X-Test-User", user)
		if valid {
			r.Header.Set("Origin", h.origin)
			r.Header.Set("X-CSRF-Token", h.service.CSRF("session"))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 20; i++ {
		if code := request("abuse", false); code != 403 {
			t.Fatal(code)
		}
	}
	if code := request("abuse", true); code != 429 || executed != 0 {
		t.Fatal("forbidden writes failed to trigger guard")
	}
	for i := 0; i < 240; i++ {
		if code := request("normal", true); code != 200 {
			t.Fatalf("valid write %d: %d", i, code)
		}
	}
	if code := request("normal", true); code != 429 || executed != 240 {
		t.Fatal("full experience write budget missing")
	}
}

func TestFullExperienceSkipsReadonlyCache(t *testing.T) {
	h := NewHandler(nil, "https://example.test")
	h.rememberExperience("full-token", Principal{UserID: "full", FullExperience: true})
	if len(h.experienceSessions) != 0 {
		t.Fatal("full session entered readonly rate cache")
	}
	h.rememberExperience("readonly-token", Principal{UserID: "readonly", PublicTest: true})
	if len(h.experienceSessions) != 1 {
		t.Fatal("legacy readonly rate cache missing")
	}
}
