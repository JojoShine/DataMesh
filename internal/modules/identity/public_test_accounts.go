package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

type PublicTestAccount struct {
	Mode        experienceMode `json:"mode,omitempty"`
	Username    string         `json:"username"`
	Password    string         `json:"password"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
}

type experienceMode string

func (m *experienceMode) UnmarshalJSON(raw []byte) error {
	var value string
	if json.Unmarshal(raw, &value) != nil || (value != "readonly" && value != "full") {
		return fmt.Errorf("invalid public test account mode")
	}
	*m = experienceMode(value)
	return nil
}

func validExperienceAccess(mode experienceMode, roles, platforms []string) bool {
	if len(platforms) == 0 || slices.ContainsFunc(platforms, func(platform string) bool { return !ValidPlatform(platform) }) {
		return false
	}
	if mode == "full" {
		return len(roles) > 0 && !slices.ContainsFunc(roles, func(role string) bool { return !ValidRole(role) || role == "admin" })
	}
	return len(roles) == 1 && roles[0] == "consumer" && !slices.ContainsFunc(platforms, func(platform string) bool { return platform != "exchange" && platform != "assets" })
}

// Recheck the deliberate marker and version after loading current permissions.
// Unconfigured identities do not incur an extra DB query.
func (s *Service) projectFullExperience(ctx context.Context, p Principal) (Principal, error) {
	for _, account := range s.fullExperienceAccounts {
		if account.Mode != "full" || account.Username != p.Username {
			continue
		}
		// A published full identity must never become an ordinary private identity
		// merely because its persistent safety marker was removed.
		if !p.PublicTest {
			return Principal{}, ErrCredentials
		}
		if !validExperienceAccess("full", p.Roles, p.Platforms) {
			return p, nil
		}
		user, err := s.repo.FindUser(ctx, p.Username)
		if IsMissing(err) {
			return p, nil
		}
		if err != nil {
			return Principal{}, err
		}
		if !user.PublicTest {
			return Principal{}, ErrCredentials
		}
		if user.ID == p.UserID && user.PublicTest && user.Active && user.Version == account.Version {
			p.PublicTest = false
			p.FullExperience = true
		}
		return p, nil
	}
	return p, nil
}

type configuredTestAccount struct {
	PublicTestAccount
	Version int `json:"version"`
}

// Only credentials deliberately provisioned for public experience are eligible.
// Existing business and administrator credentials never enter this configuration.
func (h *Handler) ConfigurePublicTestAccounts(path string) error {
	if path == "" {
		return nil
	}
	file, e := os.Open(path)
	if e != nil {
		return fmt.Errorf("public test account configuration unavailable")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	var config struct {
		Items []configuredTestAccount `json:"items"`
	}
	if decoder.Decode(&config) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || len(config.Items) > 4 {
		return fmt.Errorf("invalid public test account configuration")
	}
	seen := map[string]bool{}
	for _, account := range config.Items {
		if !usernameRE.MatchString(account.Username) || seen[account.Username] || len(account.Password) < 12 || len(account.Password) > 72 || strings.TrimSpace(account.Label) == "" || len(account.Label) > 128 || len(account.Description) > 512 || account.Version < 1 {
			return fmt.Errorf("invalid public test account configuration")
		}
		seen[account.Username] = true
	}
	h.publicTestAccounts = config.Items
	if h.service != nil {
		h.service.fullExperienceAccounts = config.Items
	}
	h.fullExperienceGuard = nil
	if slices.ContainsFunc(config.Items, func(account configuredTestAccount) bool { return account.Mode == "full" }) {
		h.fullExperienceGuard = newIsolatedGuard()
	}
	return nil
}

func (h *Handler) PublicTestAccounts(c *gin.Context) {
	items := []PublicTestAccount{}
	for _, account := range h.publicTestAccounts {
		var user User
		result := h.service.repo.db.WithContext(c.Request.Context()).Where("username=? AND active=true AND version=?", account.Username, account.Version).Limit(1).Find(&user)
		if result.Error != nil {
			Error(c, 503, "IDENTITY_UNAVAILABLE", "暂时无法加载体验账号")
			return
		}
		if user.ID == "" || !user.PublicTest {
			continue
		}
		var roles, platforms []string
		db := h.service.repo.db.WithContext(c.Request.Context())
		if db.Model(&UserRole{}).Where("user_id=?", user.ID).Pluck("role", &roles).Error != nil || db.Model(&PlatformAccess{}).Where("user_id=?", user.ID).Pluck("platform", &platforms).Error != nil {
			Error(c, 503, "IDENTITY_UNAVAILABLE", "暂时无法加载体验账号")
			return
		}
		if !validExperienceAccess(account.Mode, roles, platforms) {
			continue
		}
		items = append(items, account.PublicTestAccount)
	}
	c.JSON(200, gin.H{"items": items})
}
