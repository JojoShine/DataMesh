package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/platform/database"
	"datamesh/internal/platform/httpserver"

	"gorm.io/gorm"
)

const origin = "https://localhost:18443"
const password = "integration-password-1234"

func fixture(t *testing.T, etlWorkers ...bool) (*gorm.DB, *identity.Service, http.Handler) {
	t.Helper()
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Fatal("TEST_DSN is required; real PostgreSQL integration cannot be skipped")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(parsed.Path, "/datamesh_test_") {
		t.Fatal("Integration tests require an isolated datamesh_test_ database")
	}
	appURL, err := url.Parse(os.Getenv("TEST_APP_DSN"))
	if err != nil || appURL.Path != parsed.Path {
		t.Fatal("TEST_APP_DSN must target the same isolated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal("integration PostgreSQL unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("database unavailable")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	service, err := identity.NewService(identity.NewRepository(db), bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	router, stopWorkers, err := httpserver.NewWithLifecycle(context.Background(), db, service, origin)
	if err != nil {
		t.Fatal(err)
	}
	if len(etlWorkers) > 0 && !etlWorkers[0] {
		stopWorkers()
	}
	t.Cleanup(stopWorkers)
	return db, service, router
}

func newUser(t *testing.T, service *identity.Service, platforms []string) (string, string) {
	t.Helper()
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	username := "test_" + hex.EncodeToString(random)
	id, err := service.CreateUser(context.Background(), identity.NewUser{Username: username, Password: password, DepartmentID: "test-department", Platforms: platforms})
	if err != nil {
		t.Fatal("create integration account failed")
	}
	return id, username
}

func request(router http.Handler, method, path, body string, cookie *http.Cookie, csrf, requestOrigin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://localhost:18443"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if requestOrigin != "" {
		req.Header.Set("Origin", requestOrigin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func login(t *testing.T, router http.Handler, username string) (*http.Cookie, identity.Principal) {
	t.Helper()
	data, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	response := request(router, "POST", "/auth/login", string(data), nil, "", origin)
	if response.Code != http.StatusOK {
		t.Fatalf("login status %d", response.Code)
	}
	var principal identity.Principal
	if err := json.Unmarshal(response.Body.Bytes(), &principal); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("session Cookie protections missing")
	}
	if strings.Contains(response.Body.String(), password) || strings.Contains(response.Body.String(), cookies[0].Value) {
		t.Fatal("response leaked credentials")
	}
	return cookies[0], principal
}

func TestSharedIdentityRealPostgreSQL(t *testing.T) {
	db, service, router := fixture(t)
	all := []string{"exchange", "governance", "assets", "security"}
	id, username := newUser(t, service, all)
	cookie, principal := login(t, router, username)
	t.Run("one_session_all_four_platforms", func(t *testing.T) {
		for _, platform := range all {
			response := request(router, "GET", "/api/v1/platforms/"+platform+"/access", "", cookie, "", "")
			if response.Code != http.StatusOK {
				t.Fatalf("%s: %d", platform, response.Code)
			}
		}
	})
	t.Run("stored_session_is_hashed", func(t *testing.T) {
		var session identity.Session
		if err := db.Where("user_id = ?", id).Take(&session).Error; err != nil {
			t.Fatal(err)
		}
		if len(session.TokenHash) != 64 || session.TokenHash == cookie.Value {
			t.Fatal("session secret persisted incorrectly")
		}
	})
	t.Run("permission_revocation_applies_next_request", func(t *testing.T) {
		if err := db.Where("user_id = ? AND platform = ?", id, "security").Delete(&identity.PlatformAccess{}).Error; err != nil {
			t.Fatal(err)
		}
		if got := request(router, "GET", "/api/v1/platforms/security/access", "", cookie, "", "").Code; got != http.StatusForbidden {
			t.Fatalf("revoked platform: %d", got)
		}
	})
	t.Run("csrf_and_origin_enforced", func(t *testing.T) {
		for _, headers := range [][2]string{{"", origin}, {principal.CSRFToken, "https://untrusted.example"}, {"wrong", origin}} {
			if got := request(router, "POST", "/auth/logout", "", cookie, headers[0], headers[1]).Code; got != http.StatusForbidden {
				t.Fatalf("CSRF check: %d", got)
			}
		}
	})
	t.Run("disable_invalidates_all_platform_requests", func(t *testing.T) {
		disabledID, disabledUser := newUser(t, service, all)
		disabledCookie, _ := login(t, router, disabledUser)
		if err := service.DisableUser(context.Background(), disabledUser); err != nil {
			t.Fatal(err)
		}
		for _, platform := range all {
			if got := request(router, "GET", "/api/v1/platforms/"+platform+"/access", "", disabledCookie, "", "").Code; got != http.StatusUnauthorized {
				t.Fatalf("disabled platform: %d", got)
			}
		}
		if err := db.Model(&identity.User{}).Where("id = ?", disabledID).Update("active", true).Error; err != nil {
			t.Fatal(err)
		}
		if got := request(router, "GET", "/api/v1/me", "", disabledCookie, "", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("disabled sessions must stay invalid after re-enable: %d", got)
		}
	})
	t.Run("idle_expiry_is_enforced", func(t *testing.T) {
		if err := db.Model(&identity.Session{}).Where("user_id = ?", id).Update("last_seen_at", time.Now().Add(-31*time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if got := request(router, "GET", "/api/v1/me", "", cookie, "", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("expired session: %d", got)
		}
	})
	t.Run("logout_invalidates_shared_session", func(t *testing.T) {
		fresh, current := login(t, router, username)
		if got := request(router, "POST", "/auth/logout", "", fresh, current.CSRFToken, origin).Code; got != http.StatusNoContent {
			t.Fatalf("logout: %d", got)
		}
		for _, platform := range all {
			if got := request(router, "GET", "/api/v1/platforms/"+platform+"/access", "", fresh, "", "").Code; got != http.StatusUnauthorized {
				t.Fatalf("logged out: %d", got)
			}
		}
	})
	t.Run("absolute_expiry_is_enforced", func(t *testing.T) {
		fresh, _ := login(t, router, username)
		if err := db.Model(&identity.Session{}).Where("user_id = ?", id).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if got := request(router, "GET", "/api/v1/me", "", fresh, "", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("absolute expiry: %d", got)
		}
	})
	t.Run("login_rotates_previous_session", func(t *testing.T) {
		old, _ := login(t, router, username)
		data, err := json.Marshal(map[string]string{"username": username, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		response := request(router, "POST", "/auth/login", string(data), old, "", origin)
		if response.Code != http.StatusOK {
			t.Fatalf("rotation: %d", response.Code)
		}
		if got := request(router, "GET", "/api/v1/me", "", old, "", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("old session after rotation: %d", got)
		}
	})
	t.Run("no_default_platform_privileges", func(t *testing.T) {
		_, restricted := newUser(t, service, nil)
		limited, _ := login(t, router, restricted)
		if got := request(router, "GET", "/api/v1/platforms/exchange/access", "", limited, "", "").Code; got != http.StatusForbidden {
			t.Fatalf("unassigned account: %d", got)
		}
	})
	t.Run("unknown_routes_never_fake_success", func(t *testing.T) {
		if got := request(router, "GET", "/api/v1/not-implemented", "", nil, "", "").Code; got != http.StatusNotFound {
			t.Fatalf("unimplemented endpoint: %d", got)
		}
	})
	t.Run("audit_records_immutable_to_application_role", func(t *testing.T) {
		app, err := database.Open(context.Background(), os.Getenv("TEST_APP_DSN"))
		if err != nil {
			t.Fatal("application role unavailable")
		}
		sqlApp, err := app.DB()
		if err != nil {
			t.Fatal(err)
		}
		defer sqlApp.Close()
		if err := app.Exec("UPDATE audit_records SET result = 'tampered' WHERE actor_id = ?", id).Error; err == nil {
			t.Fatal("application role can alter audit records")
		}
	})
}

func TestLoginBoundariesAndRateLimit(t *testing.T) {
	_, _, router := fixture(t)
	if got := request(router, "POST", "/auth/login", "{}", nil, "", "https://untrusted.example").Code; got != http.StatusForbidden {
		t.Fatalf("untrusted origin: %d", got)
	}
	for _, body := range []string{`{"username":1}`, `{"unknown":"value"}`, `{} {}`, strings.Repeat("x", 9000)} {
		response := request(router, "POST", "/auth/login", body, nil, "", origin)
		if response.Code != http.StatusBadRequest || response.Header().Get("X-Trace-ID") == "" {
			t.Fatalf("invalid input: %d", response.Code)
		}
	}
	_, _, rateRouter := fixture(t)
	for index := range 13 {
		response := request(rateRouter, "POST", "/auth/login", `{"username":"x","password":"bad"}`, nil, "", origin)
		expected := http.StatusUnauthorized
		if index == 12 {
			expected = http.StatusTooManyRequests
		}
		if response.Code != expected {
			t.Fatalf("rate limit request %d: %d", index, response.Code)
		}
	}
}

func TestConcurrentAccountUniqueness(t *testing.T) {
	_, service, _ := fixture(t)
	random := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		t.Fatal(err)
	}
	input := identity.NewUser{Username: "race_" + hex.EncodeToString(random), Password: password, DepartmentID: "concurrency"}
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := service.CreateUser(context.Background(), input)
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected one account, got %d", success)
	}
}

func TestDisableSerializesWithSessionIssuance(t *testing.T) {
	db, service, _ := fixture(t)
	id, username := newUser(t, service, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	const callback = "test:hold-session-insert"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sessions" {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(callback)
	repo := identity.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	issued := make(chan error, 1)
	go func() {
		now := time.Now().UTC()
		issued <- repo.CreateSession(ctx, identity.Session{TokenHash: strings.Repeat("a", 64), UserID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, "", "concurrent-disable")
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("session issuance did not reach the controlled boundary")
	}
	blocked, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	err := service.DisableUser(blocked, username)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("disable must wait for in-flight issuance, got %v", err)
	}
	unblock()
	if err := <-issued; err != nil {
		t.Fatal(err)
	}
	if err := service.DisableUser(ctx, username); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&identity.User{}).Where("id = ?", id).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&identity.Session{}).Where("user_id = ?", id).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("disabled sessions survived concurrent issuance: count=%d err=%v", count, err)
	}
}
