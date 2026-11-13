package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTrustedProxyBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ config, remote, want string }{{"", "172.18.0.1:1234", "172.18.0.1"}, {`["172.18.0.1"]`, "172.18.0.1:1234", "198.51.100.9"}, {`["172.18.0.1"]`, "192.0.2.9:1234", "192.0.2.9"}} {
		router := gin.New()
		if err := configureProxies(router, test.config); err != nil {
			t.Fatal(err)
		}
		router.GET("/", func(c *gin.Context) { c.String(200, c.ClientIP()) })
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.9")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Body.String() != test.want {
			t.Fatalf("proxy boundary: %s", response.Body.String())
		}
	}
	for _, raw := range []string{`["0.0.0.0/0"]`, `["0.0.0.0"]`, `["::"]`, `["*"]`, `[]`, `null`, `["proxy.local"]`} {
		if configureProxies(gin.New(), raw) == nil {
			t.Fatalf("accepted broad or invalid proxy %s", raw)
		}
	}
}
