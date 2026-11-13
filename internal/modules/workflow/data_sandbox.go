package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"datamesh/internal/modules/identity"
)

const sandboxDatabase = "datamesh_sandbox"
const sandboxMaxTables = 8
const sandboxMaxBytes = 80 << 20
const sandboxMaxActive = 4

// The session scope is derived exclusively from the authenticated principal.
func sandboxVisitorScope(p identity.Principal) string {
	if !p.FullExperience {
		return ""
	}
	if p.CSRFToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(p.CSRFToken))
	return hex.EncodeToString(sum[:])
}

type DataSandbox struct {
	OwnerVersion int       `json:"-"`
	ID           string    `json:"id"`
	OwnerID      string    `json:"-"`
	DepartmentID string    `json:"-"`
	VisitorScope string    `json:"-"`
	Name         string    `json:"name"`
	State        string    `json:"state"`
	Version      int       `json:"version"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (DataSandbox) TableName() string { return "data_sandboxes" }

type DataSandboxTable struct {
	ID              string          `json:"id"`
	SandboxID       string          `json:"sandbox_id"`
	Name            string          `json:"name"`
	State           string          `json:"state"`
	Fields          json.RawMessage `json:"fields" gorm:"type:jsonb"`
	FieldLabels     json.RawMessage `json:"field_labels" gorm:"type:jsonb"`
	Rows            int64           `json:"rows"`
	Bytes           int64           `json:"bytes"`
	QueryID         *string         `json:"query_id,omitempty"`
	ErrorCode       *string         `json:"error_code,omitempty"`
	SourceObject    json.RawMessage `json:"-" gorm:"type:jsonb"`
	ResultObject    json.RawMessage `json:"-" gorm:"type:jsonb"`
	AttemptID       *string         `json:"-"`
	SandboxVersion  *int            `json:"-"`
	LeaseOwner      *string         `json:"-"`
	LeaseUntil      *time.Time      `json:"-"`
	CancelRequested bool            `json:"-"`
	LoadLabel       *string         `json:"-"`
	SourceDigest    *string         `json:"-"`
	PhysicalTable   string          `json:"-"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (DataSandboxTable) TableName() string { return "data_sandbox_tables" }

type DataSandboxQuery struct {
	ReservedBytes   int64           `json:"-"`
	ID              string          `json:"id"`
	SandboxID       string          `json:"sandbox_id"`
	SQL             string          `json:"sql"`
	Mode            string          `json:"mode"`
	TableNameValue  *string         `json:"table_name,omitempty" gorm:"column:table_name"`
	State           string          `json:"state"`
	Fields          json.RawMessage `json:"fields" gorm:"type:jsonb"`
	RowCount        int64           `json:"row_count"`
	Truncated       bool            `json:"truncated"`
	ResultTableID   *string         `json:"result_table_id,omitempty"`
	ErrorCode       *string         `json:"error_code,omitempty"`
	ResultObject    json.RawMessage `json:"-" gorm:"type:jsonb"`
	AttemptID       *string         `json:"-"`
	SandboxVersion  *int            `json:"-"`
	LeaseOwner      *string         `json:"-"`
	LeaseUntil      *time.Time      `json:"-"`
	CancelRequested bool            `json:"-"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (DataSandboxQuery) TableName() string { return "data_sandbox_queries" }
