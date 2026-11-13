package workflow

import (
	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestLightweightReplayNamespace(t *testing.T) {
	derive := func(p identity.Principal, key string) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", nil)
		c.Request.Header.Set("Idempotency-Key", key)
		c.Set("principal", p)
		lightweightMutationScope(c)
		return c.GetHeader("Idempotency-Key")
	}
	p := identity.Principal{FullExperience: true, CSRFToken: "first"}
	key := "known-client-key"
	a := derive(p, key)
	if a == key || len(a) > 128 || a != derive(p, key) {
		t.Fatal("unstable namespace")
	}
	p.CSRFToken = "second"
	if a == derive(p, key) {
		t.Fatal("cross-session replay key collision")
	}
	p.FullExperience = false
	if derive(p, key) != key {
		t.Fatal("ordinary key changed")
	}
}
