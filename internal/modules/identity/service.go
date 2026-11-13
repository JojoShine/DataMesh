package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrCredentials = errors.New("invalid credentials")
	ErrInput       = errors.New("invalid input")
	usernameRE     = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,63}$`)
)

const SessionLifetime = 8 * time.Hour
const SessionIdle = 30 * time.Minute

type Service struct {
	fullExperienceAccounts []configuredTestAccount
	repo                   *Repository
	csrfKey                []byte
	dummyHash              []byte
}

func NewService(repo *Repository, csrfKey []byte) (*Service, error) {
	if len(csrfKey) < 32 {
		return nil, errors.New("CSRF key must contain at least 32 bytes")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("invalid-account-placeholder"), 12)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, csrfKey: append([]byte(nil), csrfKey...), dummyHash: dummy}, nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func ValidPlatform(platform string) bool {
	return platform == "exchange" || platform == "governance" || platform == "assets" || platform == "security"
}

func (s *Service) CreateUser(ctx context.Context, input NewUser) (string, error) {
	if input.PublicTest && (len(input.Roles) != 1 || input.Roles[0] != "consumer" || len(input.Platforms) == 0 || len(input.Platforms) != 2 || !slices.Contains(input.Platforms, "exchange") || !slices.Contains(input.Platforms, "assets")) {
		return "", ErrInput
	}
	if len(input.DisplayName) > 200 || !usernameRE.MatchString(input.Username) || len(input.Password) < 12 || len(input.Password) > 72 || strings.TrimSpace(input.DepartmentID) == "" || len(input.DepartmentID) > 128 {
		return "", ErrInput
	}
	seen := make(map[string]bool)
	for _, role := range input.Roles {
		if !ValidRole(role) || seen[role] {
			return "", ErrInput
		}
		seen[role] = true
	}
	for _, platform := range input.Platforms {
		if !ValidPlatform(platform) || seen[platform] {
			return "", ErrInput
		}
		seen[platform] = true
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		return "", err
	}
	id, err := randomToken()
	if err != nil {
		return "", err
	}
	user := User{PublicTest: input.PublicTest, ID: id, Username: input.Username, DisplayName: strings.TrimSpace(input.DisplayName), Version: 1, PasswordHash: string(hash), DepartmentID: input.DepartmentID, Active: true}
	return id, s.repo.CreateUser(ctx, user, input.Platforms, input.Roles)
}

func ValidRole(role string) bool {
	return role == "admin" || role == "operator" || role == "governor" || role == "provider" || role == "consumer" || role == "auditor"
}

func (s *Service) Login(ctx context.Context, username, password, previous, traceID string) (Principal, string, error) {
	if !usernameRE.MatchString(username) || len(password) < 12 || len(password) > 72 {
		if err := s.repo.AuditFailure(ctx, traceID); err != nil {
			return Principal{}, "", err
		}
		return Principal{}, "", ErrCredentials
	}
	user, err := s.repo.FindUser(ctx, username)
	if err != nil && !IsMissing(err) {
		return Principal{}, "", err
	}
	hash := s.dummyHash
	if err == nil {
		hash = []byte(user.PasswordHash)
	}
	match := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !match || user.ID == "" || !user.Active {
		if err := s.repo.AuditFailure(ctx, traceID); err != nil {
			return Principal{}, "", err
		}
		return Principal{}, "", ErrCredentials
	}
	if !user.PublicTest {
		for _, account := range s.fullExperienceAccounts {
			if account.Mode == "full" && account.Username == user.Username {
				if err := s.repo.AuditFailure(ctx, traceID); err != nil {
					return Principal{}, "", err
				}
				return Principal{}, "", ErrCredentials
			}
		}
	}
	token, err := randomToken()
	if err != nil {
		return Principal{}, "", err
	}
	previousHash := ""
	if previous != "" {
		previousHash = tokenHash(previous)
	}
	now := time.Now().UTC()
	if err := s.repo.CreateSession(ctx, Session{TokenHash: tokenHash(token), UserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(SessionLifetime), LastSeenAt: now}, previousHash, traceID); err != nil {
		return Principal{}, "", err
	}
	principal, err := s.Authenticate(ctx, token)
	return principal, token, err
}

func (s *Service) CSRF(token string) string {
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return Principal{}, ErrCredentials
	}
	principal, err := s.repo.Principal(ctx, tokenHash(token), SessionIdle)
	if IsMissing(err) {
		return Principal{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, err
	}
	principal, err = s.projectFullExperience(ctx, principal)
	if err != nil {
		return Principal{}, err
	}
	principal.CSRFToken = s.CSRF(token)
	return principal, nil
}

func (s *Service) Logout(ctx context.Context, token, userID, traceID string) error {
	return s.repo.Logout(ctx, tokenHash(token), userID, traceID)
}

func (s *Service) DisableUser(ctx context.Context, username string) error {
	if !usernameRE.MatchString(username) {
		return ErrInput
	}
	return s.repo.DisableUser(ctx, username)
}
