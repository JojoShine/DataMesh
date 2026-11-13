package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Shared accounts must never replay another visitor's mutation response.
func sandboxMutationScope(c *gin.Context) {
	key := c.GetHeader("Idempotency-Key")
	if valid(key, 128) {
		sum := sha256.Sum256([]byte(key))
		c.Request.Header.Set("Idempotency-Key", sandboxOperationPrefix(principal(c))+hex.EncodeToString(sum[:24]))
	}
	c.Next()
}

// The job/file and replay response share one platform operation transaction.
func (s *Server) sandboxReplay(action string, fn func(*gorm.DB, identity.Principal, string, string, []byte) (int, json.RawMessage, string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		s.mutate(action, func(tx *gorm.DB, p identity.Principal, body []byte) (int, json.RawMessage, string, error) {
			return fn(tx, p, c.Param("id"), c.Param("table_id"), body)
		})(c)
	}
}

func sandboxOperationPrefix(p identity.Principal) string {
	sum := sha256.Sum256([]byte(p.DepartmentID + "\x00" + sandboxVisitorScope(p)))
	return "sandbox:" + hex.EncodeToString(sum[:]) + ":"
}
