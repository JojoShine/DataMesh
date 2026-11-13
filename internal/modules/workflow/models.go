package workflow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Record keeps immutable definitions together with their owning department. Runtime facts are refreshed separately.
type Record struct {
	Kind         string `gorm:"primaryKey"`
	ID           string `gorm:"primaryKey"`
	DepartmentID string
	OwnerID      string
	ParentID     string
	State        string
	Version      int
	Body         json.RawMessage `gorm:"type:jsonb"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Record) TableName() string { return "business_records" }

type Operation struct {
	ID             string          `json:"operation_id"`
	ActorID        string          `json:"-"`
	IdempotencyKey string          `json:"-"`
	RequestHash    string          `json:"-"`
	Status         string          `json:"status"`
	ResourceID     string          `json:"resource_id"`
	ResponseStatus int             `json:"-"`
	Response       json.RawMessage `json:"result" gorm:"type:jsonb"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func (Operation) TableName() string { return "business_operations" }
func newID() (string, error) {
	v := make([]byte, 16)
	if _, e := rand.Read(v); e != nil {
		return "", e
	}
	return hex.EncodeToString(v), nil
}
func encode[T any](v T) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func decode[T any](v json.RawMessage) (T, error) {
	var out T
	e := json.Unmarshal(v, &out)
	return out, e
}
func get(tx *gorm.DB, kind, id string, lock bool) (Record, error) {
	var row Record
	q := tx.Where("kind=? AND id=?", kind, id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	e := q.Take(&row).Error
	return row, e
}
func save(tx *gorm.DB, row *Record) error { return tx.Save(row).Error }

type Field struct {
	Name        string `json:"name"`
	LogicalType string `json:"logical_type"`
	Nullable    bool   `json:"nullable"`
	PrimaryKey  bool   `json:"primary_key"`
	Sensitive   bool   `json:"sensitive"`
	Precision   *int   `json:"precision"`
	Scale       *int   `json:"scale"`
}
type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type SourceInput struct {
	AuthDatabase string     `json:"auth_database,omitempty"`
	Scheme       string     `json:"scheme,omitempty"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"`
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	Database     string     `json:"database"`
	OwnerID      string     `json:"owner_id"`
	Credentials  Credential `json:"credentials"`
}

// SourceSecret is never serialized into an HTTP response.
type SourceSecret struct {
	AuthDatabase     string `json:"auth_database,omitempty"`
	Scheme           string `json:"scheme,omitempty"`
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Database         string `json:"database"`
	Ciphertext       string `json:"ciphertext"`
	ConnectionStatus string `json:"connection_status"`
	CDCStatus        string `json:"cdc_status"`
}
type Object struct {
	ObjectID      string  `json:"object_id"`
	SourceID      string  `json:"source_id"`
	QualifiedName string  `json:"qualified_name"`
	SchemaVersion int     `json:"schema_version"`
	Fields        []Field `json:"fields"`
}
type TaskInput struct {
	SourceID      string  `json:"source_id"`
	ObjectID      string  `json:"object_id"`
	Mode          string  `json:"mode"`
	DatasetID     *string `json:"dataset_id"`
	SchemaVersion int     `json:"schema_version"`
	RuleVersion   *int    `json:"rule_version"`
}
type TaskData struct {
	TaskInput
	Capture   *CDCBinding `json:"capture_binding,omitempty"`
	RunID     *string     `json:"run_id"`
	Binding   string      `json:"binding"`
	ArchiveID string      `json:"archive_id"`
	ErrorCode string      `json:"error_code"`
}
type Progress struct {
	Status        string          `json:"status"`
	LastSuccessAt *time.Time      `json:"last_success_at"`
	Position      json.RawMessage `json:"position"`
	ErrorCode     *string         `json:"error_code"`
}

func progress(s string) Progress { return Progress{Status: s, Position: json.RawMessage("null")} }

type Check struct {
	Field      string                     `json:"field"`
	Kind       string                     `json:"kind"`
	Blocking   bool                       `json:"blocking"`
	Parameters map[string]json.RawMessage `json:"parameters"`
}
type RuleInput struct {
	Name          string  `json:"name"`
	ObjectID      string  `json:"object_id"`
	SchemaVersion int     `json:"schema_version"`
	Checks        []Check `json:"checks"`
}
type RuleData struct {
	Definition RuleInput `json:"definition"`
	PreviousID string    `json:"previous_id"`
}
type DatasetInput struct {
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	SourceObjectID    string  `json:"source_object_id"`
	OwnerID           string  `json:"owner_id"`
	Fields            []Field `json:"fields"`
	UsageRestrictions string  `json:"usage_restrictions"`
}
type DatasetData struct {
	DataLayer          string                 `json:"data_layer,omitempty"`
	Storage            *GovernanceStorage     `json:"storage,omitempty"`
	Governance         *GovernanceProvenance  `json:"governance,omitempty"`
	GovernanceRunID    string                 `json:"governance_run_id,omitempty"`
	ResultCheckID      string                 `json:"result_check_id,omitempty"`
	ResultBinding      *GovernedResultBinding `json:"result_binding,omitempty"`
	Definition         DatasetInput           `json:"definition"`
	RuleID             string                 `json:"rule_id"`
	RuleVersion        int                    `json:"rule_version"`
	QualityStatus      string                 `json:"quality_status"`
	CompletenessStatus string                 `json:"completeness_status"`
	LatestWatermark    json.RawMessage        `json:"latest_watermark"`
	Quality            json.RawMessage        `json:"quality"`
}
type GovernanceTreatment struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type GovernanceProvenance struct {
	SingleSource          bool                  `json:"single_source"`
	Source                *Resource             `json:"source,omitempty"`
	SourceName            string                `json:"source_name,omitempty"`
	ProviderDepartmentID  string                `json:"provider_department_id,omitempty"`
	OriginalDirectoryID   string                `json:"original_directory_id,omitempty"`
	OriginalDirectoryName string                `json:"original_directory_name,omitempty"`
	Steps                 []GovernanceTreatment `json:"steps"`
}
type Resource struct {
	Type    string `json:"resource_type"`
	ID      string `json:"resource_id"`
	Version int    `json:"contract_version"`
}
type GrantRequest struct {
	Materials    *ApplicationMaterials `json:"materials,omitempty"`
	Resource     Resource              `json:"resource"`
	DepartmentID string                `json:"department_id"`
	Purpose      string                `json:"purpose"`
	Fields       []string              `json:"fields"`
	ExpiresAt    time.Time             `json:"expires_at"`
}
type ApplicationData struct {
	Snapshot       *SharingSnapshot   `json:"sharing_snapshot,omitempty"`
	Supplements    []SupplementRecord `json:"supplements,omitempty"`
	ApplicantID    string             `json:"applicant_id"`
	Request        GrantRequest       `json:"request"`
	DecisionReason *string            `json:"decision_reason"`
}
type GrantData struct {
	Snapshot      *SharingSnapshot `json:"sharing_snapshot,omitempty"`
	ApplicationID string           `json:"application_id"`
	GranteeUserID string           `json:"grantee_user_id"`
	Scope         GrantRequest     `json:"scope"`
	ApprovedBy    string           `json:"approved_by"`
	ApprovedAt    time.Time        `json:"approved_at"`
}
type ArchiveData struct {
	ManifestVersion int                 `json:"manifest_version,omitempty"`
	Boundary        CDCSnapshotBoundary `json:"boundary,omitzero"`
	Chunks          []ArchiveChunk      `json:"chunks,omitempty"`
	ObjectID        string              `json:"object_id"`
	Key             string              `json:"key"`
	SHA256          string              `json:"sha256"`
	Topic           string              `json:"topic"`
	Start           int64               `json:"start_offset"`
	End             int64               `json:"end_offset_exclusive"`
	Count           int                 `json:"event_count"`
	RunID           string              `json:"run_id"`
}
