package identity

import "time"

type User struct {
	PublicTest   bool `json:"public_test,omitempty"`
	ID           string
	Username     string
	DisplayName  string
	Version      int
	PasswordHash string `json:"-"`
	DepartmentID string
	Active       bool
	CreatedAt    time.Time
}

type Session struct {
	TokenHash  string `json:"-"`
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

type PlatformAccess struct {
	UserID   string
	Platform string
}

func (PlatformAccess) TableName() string { return "user_platform_access" }

type AuditRecord struct {
	ActorID  *string
	Action   string
	ObjectID string
	Result   string
	TraceID  string
}

type Principal struct {
	FullExperience bool     `json:"full_experience,omitempty" gorm:"-"`
	PublicTest     bool     `json:"public_test,omitempty"`
	UserID         string   `json:"user_id"`
	Username       string   `json:"username"`
	DisplayName    string   `json:"display_name"`
	DepartmentID   string   `json:"department_id"`
	Platforms      []string `json:"platforms" gorm:"-"`
	Roles          []string `json:"roles" gorm:"-"`
	CSRFToken      string   `json:"csrf_token" gorm:"-"`
}

type NewUser struct {
	PublicTest   bool     `json:"public_test,omitempty"`
	Username     string   `json:"username"`
	DisplayName  string   `json:"display_name,omitempty"`
	Password     string   `json:"password"`
	DepartmentID string   `json:"department_id"`
	Platforms    []string `json:"platforms"`
	Roles        []string `json:"roles"`
}

type UserRole struct {
	UserID string
	Role   string
}

func (UserRole) TableName() string { return "user_roles" }
