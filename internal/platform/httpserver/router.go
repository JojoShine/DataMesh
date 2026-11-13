package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"datamesh/migrations"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func New(db *gorm.DB, service *identity.Service, origin string) (*gin.Engine, error) {
	r, _, e := NewWithLifecycle(context.Background(), db, service, origin)
	return r, e
}
func NewWithLifecycle(ctx context.Context, db *gorm.DB, service *identity.Service, origin string) (*gin.Engine, func(), error) {
	r, business, e := newRouter(db, service, origin)
	if e != nil {
		return nil, nil, e
	}
	stopSandbox := business.StartDataSandboxWorker(ctx)
	stopETL := business.StartETLWorkers(ctx)
	stopCDC := business.StartCDCWorkers(ctx)
	stopSnapshot := business.StartCDCSnapshotWorker(ctx)
	stopRetention := business.StartResourceRetentionWorker(ctx)
	stopGoverned := business.StartGovernedWorkers(ctx)
	stopGovernance := business.StartGovernanceWorkers(ctx)
	stopOrchestrator := business.StartGovernanceOrchestrator(ctx)
	stopModels := business.StartGovernanceModelWorker(ctx)
	stopMaintenance := business.StartGovernanceMaintenanceWorker(ctx)
	stopReconciliation := business.StartETLReconciliationWorker(ctx)
	return r, func() {
		stopSandbox()
		stopMaintenance()
		stopModels()
		stopReconciliation()
		stopOrchestrator()
		stopGovernance()
		stopGoverned()
		stopRetention()
		stopSnapshot()
		stopCDC()
		stopETL()
	}, nil
}
func newRouter(db *gorm.DB, service *identity.Service, origin string) (*gin.Engine, *workflow.Server, error) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	if err := configureProxies(r, os.Getenv("TRUSTED_PROXY_IPS")); err != nil {
		return nil, nil, err
	}
	r.Use(func(c *gin.Context) {
		trace := make([]byte, 16)
		if _, err := rand.Read(trace); err != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		traceID := hex.EncodeToString(trace)
		c.Set("trace_id", traceID)
		c.Header("X-Trace-ID", traceID)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		timeout := 10 * time.Second
		if c.Request.URL.Path == "/internal/governance/stream-script" {
			timeout = 45 * time.Second
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "trace_id", traceID)
				identity.Error(c, http.StatusInternalServerError, "INTERNAL", "请求处理失败")
			}
		}()
		c.Next()
	})
	r.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "alive"}) })
	r.GET("/health/ready", func(c *gin.Context) {
		sqlDB, err := db.DB()
		if err != nil || sqlDB.PingContext(c.Request.Context()) != nil {
			identity.Error(c, http.StatusServiceUnavailable, "NOT_READY", "依赖尚未就绪")
			return
		}
		var tables int64
		if err := db.WithContext(c.Request.Context()).Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('users', 'sessions', 'user_platform_access', 'audit_records','user_roles','business_records','business_operations','governance_definitions','governance_versions','metadata_observations','governance_runs','governance_run_events','governance_flow_rows','governance_outputs','governance_workflow_tasks','governance_schedules','governance_schedule_triggers','governance_quality_issues','governance_quality_issue_events','security_controls','security_control_versions','security_scans','security_incidents','security_delivery_proofs','organization_units','directory_code_counters','directory_code_reservations','identity_change_records','platform_initializations')").Scan(&tables).Error; err != nil || tables != 29 {
			identity.Error(c, http.StatusServiceUnavailable, "SCHEMA_NOT_READY", "数据库迁移尚未完成")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	r.GET("/healthz", func(c *gin.Context) {
		version, err := migrations.Version()
		var state struct {
			Version uint
			Dirty   bool
		}
		result := db.WithContext(c.Request.Context()).Raw("SELECT version,dirty FROM schema_migrations").Scan(&state)
		if err != nil || result.Error != nil || result.RowsAffected != 1 || state.Dirty || state.Version != version {
			identity.Error(c, http.StatusServiceUnavailable, "SCHEMA_NOT_READY", "数据库迁移尚未完成")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	h := identity.NewHandler(service, origin)
	if err := h.ConfigureSessionCookiePath(os.Getenv("BASE_PATH")); err != nil {
		return nil, nil, err
	}
	if err := h.ConfigureSessionCookie(os.Getenv("SESSION_COOKIE_NAME")); err != nil {
		return nil, nil, err
	}
	if err := h.ConfigurePublicTestAccounts(os.Getenv("PUBLIC_TEST_ACCOUNTS_FILE")); err != nil {
		return nil, nil, err
	}
	if err := h.ConfigureIsolatedExperienceGuard(os.Getenv("ISOLATED_EXPERIENCE_GUARD")); err != nil {
		return nil, nil, err
	}
	r.Use(func(c *gin.Context) {
		if c.Request.URL.Path == "/api/public/assets" || strings.HasPrefix(c.Request.URL.Path, "/api/public/assets/") {
			h.PublicExperienceRateLimit(c)
		} else {
			c.Next()
		}
	})
	r.GET("/auth/test-accounts", h.PublicExperienceRateLimit, h.PublicTestAccounts)
	r.POST("/auth/login", h.Login)
	r.POST("/auth/logout", h.Authenticate, h.CSRF, h.Logout)
	r.GET("/api/v1/me", h.Authenticate, h.Me)
	r.GET("/api/v1/platforms/:platform/access", h.Authenticate, h.PlatformAccess)
	business, err := workflow.New(db)
	if err != nil {
		return nil, nil, err
	}
	business.Register(r, h)
	business.RegisterDelivery(r, h)
	registerWeb(r)
	r.NoRoute(func(c *gin.Context) { identity.Error(c, http.StatusNotFound, "NOT_FOUND", "接口尚未提供") })
	return r, business, nil
}
