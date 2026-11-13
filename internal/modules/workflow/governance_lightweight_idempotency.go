package workflow

import (
	"crypto/sha256"
	"datamesh/internal/modules/identity"
	"encoding/hex"
	"github.com/gin-gonic/gin"
)

func lightweightOperationPrefix(p identity.Principal) string {
	sum := sha256.Sum256([]byte(p.DepartmentID + "\x00" + sandboxVisitorScope(p)))
	return "lightweight:" + hex.EncodeToString(sum[:]) + ":"
}
func lightweightMutationScope(c *gin.Context) {
	p := principal(c)
	if p.FullExperience {
		if p.CSRFToken == "" {
			c.AbortWithStatus(403)
			return
		}
		key := c.GetHeader("Idempotency-Key")
		if valid(key, 128) {
			sum := sha256.Sum256([]byte(key))
			c.Request.Header.Set("Idempotency-Key", lightweightOperationPrefix(p)+hex.EncodeToString(sum[:20]))
		}
	}
	c.Next()
}
