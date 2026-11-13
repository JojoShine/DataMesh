package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMountBasePath(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.URL.RequestURI())) })
	handler, err := MountBasePath("/datamesh", next)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path           string
		status         int
		body, redirect string
	}{
		{"/datamesh/api/v1/me?file=a%2Fb", 200, "/api/v1/me?file=a%2Fb", ""},
		{"/datamesh?file=a%2Fb", 308, "", "/datamesh/?file=a%2Fb"},
		{"/datameshx/api/v1/me", 404, "", ""},
		{"/api/v1/me", 404, "", ""},
		{"/internal/governance/stream-script", 200, "/internal/governance/stream-script", ""},
		{"/internal/other", 404, "", ""},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status || test.body != "" && response.Body.String() != test.body || response.Header().Get("Location") != test.redirect {
			t.Fatalf("%s: %d %q %q", test.path, response.Code, response.Body.String(), response.Header().Get("Location"))
		}
	}
	if _, err := MountBasePath("/../other", next); err == nil {
		t.Fatal("unsafe prefix accepted")
	}
	root, err := MountBasePath("", next)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	root.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if response.Body.String() != "/api/v1/me" {
		t.Fatal("root routing changed")
	}
}
