package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGuideWebRoutes(t *testing.T) {
	directory := t.TempDir()
	index := "<!doctype html><title>Guide application</title>"
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "static"), 0700); err != nil {
		t.Fatal(err)
	}
	markdown := "# 共享交换操作指南\n"
	if err := os.WriteFile(filepath.Join(directory, "static", "exchange.md"), []byte(markdown), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_DIR", directory)
	router := gin.New()
	registerWeb(router)
	for _, path := range []string{"/", "/guides", "/guides/exchange", "/guides/governance", "/guides/assets", "/guides/security"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.String() != index {
				t.Fatalf("guide deep link: status=%d body=%q", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'self'") {
				t.Fatal("guide page must retain the existing content security policy")
			}
		})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/exchange.md", nil))
	if response.Code != http.StatusOK || response.Body.String() != markdown {
		t.Fatal("Markdown download must serve the original document")
	}
	for _, path := range []string{"/guides/unknown", "/api/v1/unknown", "/static/missing.md"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("unknown path %s must stay 404, got %d", path, response.Code)
		}
	}
}
