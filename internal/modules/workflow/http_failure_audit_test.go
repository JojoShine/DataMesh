package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestExpiredRequestStillRequiresFailureAudit(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "record-denial", true: "audit-unavailable"}[unavailable], func(t *testing.T) {
			db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=unused dbname=unused"}), &gorm.Config{DisableAutomaticPing: true, DryRun: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			called := false
			err = db.Callback().Create().Before("gorm:create").Register("verify_failure_audit", func(tx *gorm.DB) {
				called = true
				if tx.Statement.Context.Err() != nil {
					t.Error("expired request prevented mandatory denial audit")
				}
				deadline, ok := tx.Statement.Context.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
					t.Error("failure audit must have an independent bounded budget")
				}
				record := tx.Statement.Dest.(*identity.AuditRecord)
				if record.Result != "denied" || !strings.HasPrefix(record.Action, "GET ") {
					t.Error("detached context used for more than a denial audit")
				}
				if unavailable {
					tx.AddError(errors.New("audit storage unavailable"))
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest("GET", "/protected", nil).WithContext(ctx)
			c.Set("principal", identity.Principal{UserID: "actor"})
			(&Server{db: db}).fail(c, denied)
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCode := 403, "FORBIDDEN"
			if unavailable {
				wantStatus, wantCode = 503, "AUDIT_UNAVAILABLE"
			}
			if !called || response.Code != wantStatus || body.Code != wantCode {
				t.Fatalf("mandatory failure audit status=%d code=%s called=%t", response.Code, body.Code, called)
			}
		})
	}
}
