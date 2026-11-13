package identity

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// This opt-in guard budgets authenticated work in the isolated instance. It does
// not replace current permissions, CSRF, review, or physical job limits.
type isolatedGuard struct {
	mu      sync.Mutex
	buckets map[string]*isolatedBucket
}

type isolatedBucket struct {
	start, lastSeen, forbiddenStart            time.Time
	requests, mutations, forbidden, concurrent int
	cooldownUntil                              time.Time
	cooldownReason                             string
	cooldownAll                                bool
}

func newIsolatedGuard() *isolatedGuard {
	return &isolatedGuard{buckets: make(map[string]*isolatedBucket)}
}

func (h *Handler) ConfigureIsolatedExperienceGuard(setting string) error {
	switch setting {
	case "", "false":
		h.isolatedGuard = nil
		return nil
	case "true":
		if h.cookieName != "datamesh_experience_session" || len(h.publicTestAccounts) > 0 {
			return fmt.Errorf("isolated experience guard requires isolated sessions and no public test accounts")
		}
		h.isolatedGuard = newIsolatedGuard()
		return nil
	default:
		return fmt.Errorf("ISOLATED_EXPERIENCE_GUARD must be true or false")
	}
}

func (g *isolatedGuard) begin(userID string, mutation bool, now time.Time) (func(int, time.Time), time.Duration, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, bucket := range g.buckets {
		if bucket.concurrent == 0 && now.Sub(bucket.lastSeen) >= 2*time.Minute && !bucket.cooldownUntil.After(now) {
			delete(g.buckets, key)
		}
	}
	b := g.buckets[userID]
	if b == nil {
		if len(g.buckets) >= 1024 {
			return nil, 30 * time.Second, "principal_capacity"
		}
		b = &isolatedBucket{start: now}
		g.buckets[userID] = b
	}
	b.lastSeen = now
	if !b.cooldownUntil.IsZero() && !b.cooldownUntil.After(now) {
		b.start = now
		b.requests, b.mutations, b.forbidden = 0, 0, 0
		b.forbiddenStart = time.Time{}
		b.cooldownUntil = time.Time{}
		b.cooldownReason = ""
		b.cooldownAll = false
	}
	if b.cooldownUntil.After(now) && (b.cooldownAll || mutation) {
		return nil, b.cooldownUntil.Sub(now), b.cooldownReason
	}
	if now.Sub(b.start) >= time.Minute {
		b.start = now
		b.requests, b.mutations = 0, 0
	}
	reason := ""
	switch {
	case b.requests >= 600:
		reason = "request_budget"
	case mutation && b.mutations >= 240:
		reason = "mutation_budget"
	case mutation && b.concurrent >= 8:
		reason = "concurrent_mutations"
	}
	if reason != "" {
		b.cooldownUntil = now.Add(30 * time.Second)
		b.cooldownReason = reason
		b.cooldownAll = reason == "request_budget"
		return nil, 30 * time.Second, reason
	}
	b.requests++
	if mutation {
		b.mutations++
		b.concurrent++
	}
	return func(status int, finished time.Time) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if mutation {
			b.concurrent--
			if finished.After(b.lastSeen) {
				b.lastSeen = finished
			}
			// Read-side permission differences, expired sessions and validation errors
			// are ordinary UI behavior; only repeated forbidden writes trigger cooldown.
			if status == http.StatusForbidden {
				if b.forbiddenStart.IsZero() || finished.Sub(b.forbiddenStart) >= time.Minute {
					b.forbiddenStart = finished
					b.forbidden = 0
				}
				b.forbidden++
				if b.forbidden >= 20 && !b.cooldownUntil.After(finished) {
					b.cooldownUntil = finished.Add(30 * time.Second)
					b.cooldownReason = "forbidden_mutations"
					b.cooldownAll = false
				}
			}
		}
	}, 0, ""
}

func (h *Handler) isolatedExperienceRequest(c *gin.Context, p Principal) {
	guard := h.isolatedGuard
	if p.FullExperience {
		guard = h.fullExperienceGuard
	}
	if guard == nil || p.PublicTest {
		c.Next()
		return
	}
	mutation := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions
	release, retry, reason := guard.begin(p.UserID, mutation, time.Now())
	if reason != "" {
		seconds := int(math.Ceil(retry.Seconds()))
		c.Header("Retry-After", fmt.Sprint(seconds))
		slog.Warn("isolated experience request blocked", "actor_id", p.UserID, "route", c.FullPath(), "method", c.Request.Method, "reason", reason, "retry_after", seconds, "trace_id", c.GetString("trace_id"))
		Error(c, http.StatusTooManyRequests, "ISOLATED_EXPERIENCE_COOLDOWN", "操作过于频繁，请稍后重试；现有权限不受影响")
		return
	}
	defer func() { release(c.Writer.Status(), time.Now()) }()
	c.Next()
}
