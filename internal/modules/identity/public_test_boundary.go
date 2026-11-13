package identity

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type experienceWindow struct {
	Start time.Time
	Count int
}
type experienceLimits struct {
	mu      sync.Mutex
	buckets map[string]experienceWindow
}

type experienceSession struct {
	UserID    string
	ExpiresAt time.Time
}

func (h *Handler) rememberExperience(token string, p Principal) {
	if !p.PublicTest {
		return
	}
	h.experienceMu.Lock()
	defer h.experienceMu.Unlock()
	if h.experienceSessions == nil {
		h.experienceSessions = map[string]experienceSession{}
	}
	now := time.Now()
	for key, value := range h.experienceSessions {
		if !value.ExpiresAt.After(now) {
			delete(h.experienceSessions, key)
		}
	}
	if len(h.experienceSessions) < 1024 {
		h.experienceSessions[tokenHash(token)] = experienceSession{UserID: p.UserID, ExpiresAt: now.Add(SessionLifetime)}
	}
}
func (h *Handler) precheckExperience(c *gin.Context, token string) bool {
	h.experienceMu.Lock()
	session, exists := h.experienceSessions[tokenHash(token)]
	h.experienceMu.Unlock()
	if !exists || !session.ExpiresAt.After(time.Now()) {
		return true
	}
	// This cache only throttles; authentication and current permissions still come from the database.
	if !h.testRequests.allow(session.UserID, 60) {
		c.Header("Retry-After", "60")
		Error(c, 429, "EXPERIENCE_RATE_LIMITED", "体验访问过于频繁，请稍后再试")
		return false
	}
	c.Set("experience_rate_checked", true)
	return true
}

func (l *experienceLimits) allow(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.buckets == nil {
		l.buckets = map[string]experienceWindow{}
	}
	for k, v := range l.buckets {
		if now.Sub(v.Start) >= time.Minute {
			delete(l.buckets, k)
		}
	}
	entry, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= 1024 {
			return false
		}
		entry.Start = now
	}
	entry.Count++
	l.buckets[key] = entry
	return entry.Count <= limit
}

func (h *Handler) publicTestBoundary(c *gin.Context, p Principal) bool {
	if !p.PublicTest {
		return true
	}
	if !c.GetBool("experience_rate_checked") && !h.testRequests.allow(p.UserID, 60) {
		c.Header("Retry-After", "60")
		Error(c, 429, "EXPERIENCE_RATE_LIMITED", "体验访问过于频繁，请稍后再试")
		return false
	}
	// Closed allowlist: new APIs and even elevated roles never expand a public identity.
	allowed := c.Request.Method == http.MethodGet && c.FullPath() == "/api/v1/me" || c.Request.Method == http.MethodPost && c.FullPath() == "/auth/logout"
	if !allowed {
		Error(c, 403, "EXPERIENCE_READ_ONLY", "公开测试账号仅可浏览首页公开资产，禁止业务写入、源数据读取及任务执行")
		return false
	}
	return true
}

func (h *Handler) PublicExperienceRateLimit(c *gin.Context) {
	if !h.publicRequests.allow(c.ClientIP(), 60) {
		c.Header("Retry-After", "60")
		Error(c, 429, "PUBLIC_RATE_LIMITED", "访问过于频繁，请稍后再试")
		return
	}
	c.Next()
}

func (s *Service) RestrictPublicTestUser(ctx context.Context, username string) error {
	return s.repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("username=?", username).Take(&user).Error; e != nil {
			return e
		}
		if user.PublicTest {
			return nil
		}
		var roles []string
		if e := tx.Model(&UserRole{}).Where("user_id=?", user.ID).Pluck("role", &roles).Error; e != nil {
			return e
		}
		if len(roles) != 1 || roles[0] != "consumer" {
			return fmt.Errorf("only consumer identities can become public test identities")
		}
		if e := tx.Model(&user).Updates(map[string]interface{}{"public_test": true, "version": user.Version + 1}).Error; e != nil {
			return e
		}
		if e := tx.Where("user_id=?", user.ID).Delete(&Session{}).Error; e != nil {
			return e
		}
		return tx.Create(&AuditRecord{Action: "restrict_public_test_user", ObjectID: user.ID, Result: "success", TraceID: user.ID}).Error
	})
}
