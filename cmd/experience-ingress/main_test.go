package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIngressFixedUpstreamAndCookieIsolation(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me" || r.Host != "localhost:19443" {
			t.Errorf("route/host changed")
		}
		if _, err := r.Cookie("datamesh_session"); err == nil {
			t.Error("main cookie reached experience backend")
		}
		if cookie, err := r.Cookie(experienceCookie); err != nil || cookie.Value != "own" {
			t.Error("experience cookie missing")
		}
		if r.Header.Get("Forwarded") != "" || strings.Contains(r.Header.Get("X-Forwarded-For"), "forged") {
			t.Error("spoofed proxy identity forwarded")
		}
		if r.Header.Get("X-CSRF-Token") != "token" {
			t.Error("CSRF token lost")
		}
		http.SetCookie(w, &http.Cookie{Name: "datamesh_session", Value: "bad"})
		http.SetCookie(w, &http.Cookie{Name: experienceCookie, Value: "new", Domain: "example.com"})
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	handler := ingressHandler(target, http.DefaultTransport)
	request := httptest.NewRequest("GET", "/api/v1/me", nil)
	request.Host = "localhost:19443"
	request.Header.Set("Cookie", "datamesh_session=main; datamesh_experience_session=own")
	request.Header.Set("Forwarded", "forged")
	request.Header.Set("X-Forwarded-For", "forged")
	request.Header.Set("X-CSRF-Token", "token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != `{"ok":true}` {
		t.Fatal("upstream payload changed")
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != experienceCookie || cookies[0].Domain != "" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("response cookie isolation failed")
	}
}
func TestIngressRejectsProxyMethodsAndAbsoluteURLs(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	handler := ingressHandler(target, http.DefaultTransport)
	for _, method := range []string{"CONNECT", "TRACE"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/", nil))
		if response.Code != 405 {
			t.Fatal("proxy method accepted")
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://external.invalid/", nil))
	if response.Code != 400 {
		t.Fatal("absolute proxy URL accepted")
	}
}
func TestIngressDoesNotTrustUnpinnedTLS(t *testing.T) {
	if _, err := ingressTransport([]byte("invalid")); err == nil {
		t.Fatal("invalid certificate accepted")
	}
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	// No system/unrelated CA fallback: the supplied trust roots must authenticate the backend.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	response := httptest.NewRecorder()
	ingressHandler(target, transport).ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 502 || strings.Contains(response.Body.String(), "x509") {
		t.Fatal("untrusted upstream was accepted or leaked diagnostics")
	}
}

func TestIngressDoesNotExposePrivateStreamBridge(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	for _, path := range []string{"/internal/governance/stream-script", "/internal/other", "/internal//governance/stream-script"} {
		response := httptest.NewRecorder()
		ingressHandler(target, http.DefaultTransport).ServeHTTP(response, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		if response.Code != 404 {
			t.Fatalf("private route forwarded: %s %d", path, response.Code)
		}
	}
}
