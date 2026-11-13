package identity

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const CookieName = "datamesh_session"

type limiterEntry struct {
	start time.Time
	count int
}

type Handler struct {
	fullExperienceGuard *isolatedGuard
	isolatedGuard       *isolatedGuard
	experienceMu        sync.Mutex
	experienceSessions  map[string]experienceSession
	testRequests        experienceLimits
	publicRequests      experienceLimits
	testLogins          experienceLimits
	publicTestAccounts  []configuredTestAccount
	cookieName          string
	cookiePath          string
	service             *Service
	origin              string
	mu                  sync.Mutex
	entries             map[string]limiterEntry
}

func NewHandler(service *Service, origin string) *Handler {
	return &Handler{service: service, origin: origin, cookieName: CookieName, cookiePath: "/", entries: make(map[string]limiterEntry)}
}

func Error(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"code": code, "message": message, "trace_id": c.GetString("trace_id")})
}

func (h *Handler) allowLogin(ip string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for key, entry := range h.entries {
		if now.Sub(entry.start) >= time.Minute {
			delete(h.entries, key)
		}
	}
	entry, exists := h.entries[ip]
	if !exists {
		if len(h.entries) >= 1024 {
			return false
		}
		entry.start = now
	}
	entry.count++
	h.entries[ip] = entry
	return entry.count <= 12
}

func (h *Handler) Login(c *gin.Context) {
	if c.GetHeader("Origin") != h.origin {
		Error(c, http.StatusForbidden, "ORIGIN_DENIED", "请求来源不受信任")
		return
	}
	if !h.allowLogin(c.ClientIP()) {
		c.Header("Retry-After", "60")
		Error(c, http.StatusTooManyRequests, "RATE_LIMITED", "登录请求过于频繁")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		Error(c, http.StatusUnsupportedMediaType, "INVALID_CONTENT_TYPE", "仅接受 JSON")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		Error(c, http.StatusBadRequest, "INVALID_INPUT", "登录参数不合法")
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		Error(c, http.StatusBadRequest, "INVALID_INPUT", "请求只能包含一个 JSON 对象")
		return
	}
	previous, _ := c.Cookie(h.cookieName)
	user, lookupErr := h.service.repo.FindUser(c.Request.Context(), input.Username)
	if lookupErr != nil && !IsMissing(lookupErr) {
		Error(c, 503, "IDENTITY_UNAVAILABLE", "暂时无法登录")
		return
	}
	if user.PublicTest && !h.testLogins.allow(user.ID, 10) {
		c.Header("Retry-After", "60")
		Error(c, 429, "EXPERIENCE_RATE_LIMITED", "体验登录过于频繁，请稍后再试")
		return
	}
	principal, token, err := h.service.Login(c.Request.Context(), input.Username, input.Password, previous, c.GetString("trace_id"))
	input.Password = ""
	if errors.Is(err, ErrCredentials) {
		Error(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "账号或密码错误")
		return
	}
	if err != nil {
		Error(c, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "暂时无法登录")
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: h.cookieName, Value: token, Path: h.cookiePath, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(SessionLifetime.Seconds())})
	h.rememberExperience(token, principal)
	c.JSON(http.StatusOK, principal)
}

func (h *Handler) Authenticate(c *gin.Context) {
	token, err := c.Cookie(h.cookieName)
	if err != nil {
		Error(c, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
		return
	}
	if !h.precheckExperience(c, token) {
		return
	}
	principal, err := h.service.Authenticate(c.Request.Context(), token)
	if errors.Is(err, ErrCredentials) {
		Error(c, http.StatusUnauthorized, "UNAUTHENTICATED", "登录状态已失效")
		return
	}
	if err != nil {
		Error(c, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "暂时无法校验身份")
		return
	}
	h.rememberExperience(token, principal)
	if !h.publicTestBoundary(c, principal) {
		return
	}
	c.Set("principal", principal)
	c.Set("session_token", token)
	h.isolatedExperienceRequest(c, principal)
}

func (h *Handler) CSRF(c *gin.Context) {
	token := c.GetString("session_token")
	if c.GetHeader("Origin") != h.origin || !hmac.Equal([]byte(c.GetHeader("X-CSRF-Token")), []byte(h.service.CSRF(token))) {
		Error(c, http.StatusForbidden, "CSRF_DENIED", "请求校验失败")
		return
	}
	c.Next()
}

func principalFrom(c *gin.Context) Principal {
	value, _ := c.Get("principal")
	principal, _ := value.(Principal)
	return principal
}

func (h *Handler) Me(c *gin.Context) { c.JSON(http.StatusOK, principalFrom(c)) }

func (h *Handler) PlatformAccess(c *gin.Context) {
	platform := c.Param("platform")
	if !ValidPlatform(platform) {
		Error(c, http.StatusNotFound, "NOT_FOUND", "平台不存在")
		return
	}
	if !slices.Contains(principalFrom(c).Platforms, platform) {
		Error(c, http.StatusForbidden, "FORBIDDEN", "无权访问该平台")
		return
	}
	c.JSON(http.StatusOK, gin.H{"platform": platform, "allowed": true})
}

func (h *Handler) Logout(c *gin.Context) {
	principal := principalFrom(c)
	if err := h.service.Logout(c.Request.Context(), c.GetString("session_token"), principal.UserID, c.GetString("trace_id")); err != nil {
		Error(c, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "暂时无法退出")
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: h.cookieName, Value: "", Path: h.cookiePath, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	c.Status(http.StatusNoContent)
}
