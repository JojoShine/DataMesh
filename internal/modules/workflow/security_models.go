package workflow

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
)

type SecurityControl struct {
	ID               string    `json:"id" gorm:"primaryKey"`
	Kind             string    `json:"kind"`
	OwnerID          string    `json:"owner_id"`
	DepartmentID     string    `json:"department_id"`
	Generation       int       `json:"generation"`
	LatestVersion    int       `json:"latest_version"`
	PublishedVersion int       `json:"published_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (SecurityControl) TableName() string { return "security_controls" }

type SecurityControlVersion struct {
	CreatedBy  string          `json:"created_by"`
	ControlID  string          `json:"control_id" gorm:"primaryKey"`
	Version    int             `json:"version" gorm:"primaryKey"`
	State      string          `json:"state"`
	Definition json.RawMessage `json:"definition" gorm:"type:jsonb"`
	ReviewerID string          `json:"reviewer_id"`
	Reason     string          `json:"reason"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (SecurityControlVersion) TableName() string { return "security_control_versions" }

type SecurityDefinition struct {
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	Resource      *Resource           `json:"resource"`
	Field         string              `json:"field"`
	Level         string              `json:"level"`
	Actions       []string            `json:"actions"`
	Department    string              `json:"department"`
	Roles         []string            `json:"roles"`
	Deny          bool                `json:"deny"`
	MaskFields    []string            `json:"mask_fields"`
	RequireProof  bool                `json:"require_proof"`
	Visible       bool                `json:"visible"`
	Invisible     bool                `json:"invisible"`
	RowFilters    []SecurityRowFilter `json:"row_filters"`
	FileMark      bool                `json:"file_mark"`
	Template      *GovernanceRef      `json:"template"`
	Text          string              `json:"text"`
	Pattern       string              `json:"pattern"`
	Threshold     int                 `json:"threshold"`
	WindowMinutes int                 `json:"window_minutes"`
}
type SecurityScan struct {
	ID         string          `json:"id" gorm:"primaryKey"`
	ActorID    string          `json:"actor_id"`
	Resource   json.RawMessage `json:"resource" gorm:"type:jsonb"`
	Candidates json.RawMessage `json:"candidates" gorm:"type:jsonb"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (SecurityScan) TableName() string { return "security_scans" }

type SecurityIncident struct {
	ID         string          `json:"id" gorm:"primaryKey"`
	Resource   json.RawMessage `json:"resource" gorm:"type:jsonb"`
	ResourceID string          `json:"resource_id"`
	Title      string          `json:"title"`
	Reason     string          `json:"reason"`
	State      string          `json:"state"`
	OwnerID    string          `json:"owner_id"`
	FrozenBy   string          `json:"frozen_by"`
	ReviewedBy string          `json:"reviewed_by"`
	Generation int             `json:"generation"`
	Evidence   json.RawMessage `json:"evidence" gorm:"type:jsonb"`
	DedupeKey  *string         `json:"-"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

func (SecurityIncident) TableName() string { return "security_incidents" }

type SecurityDeliveryProof struct {
	Manifest     json.RawMessage `json:"manifest" gorm:"type:jsonb"`
	ID           string          `json:"id" gorm:"primaryKey"`
	ActorID      string          `json:"actor_id"`
	DepartmentID string          `json:"department_id"`
	Resource     json.RawMessage `json:"resource" gorm:"type:jsonb"`
	Action       string          `json:"action"`
	GrantID      string          `json:"grant_id"`
	Fields       json.RawMessage `json:"fields" gorm:"type:jsonb"`
	Controls     json.RawMessage `json:"controls" gorm:"type:jsonb"`
	Text         string          `json:"text"`
	SHA256       string          `json:"sha256"`
	KeyID        string          `json:"key_id"`
	Signature    string          `json:"signature"`
	State        string          `json:"state"`
	SizeBytes    int64           `json:"size_bytes"`
	RunID        string          `json:"run_id"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (SecurityDeliveryProof) TableName() string { return "security_delivery_proofs" }

var securityLevels = []string{"public", "internal", "sensitive", "restricted"}
var securityActions = []string{"query", "download", "invoke", "etl"}
var securityVariables = []string{"recipient", "department", "resource", "version", "time", "delivery_id"}

func securityKind(kind string) bool {
	return slices.Contains([]string{"classification", "policy", "watermark", "recognition", "risk_rule"}, kind)
}
func validateSecurityDefinition(kind string, d SecurityDefinition) error {
	if !securityKind(kind) || !valid(d.Name, 200) || len(d.Description) > 2000 {
		return invalid
	}
	if d.Resource != nil && (!valid(d.Resource.ID, 128) || d.Resource.Version < 1 || !slices.Contains([]string{"raw_object", "governed_dataset", "table_resource", "file_resource", "api_resource"}, d.Resource.Type)) {
		return invalid
	}
	if len(d.Field) > 128 || len(d.Department) > 128 || len(d.MaskFields) > 100 || len(d.Roles) > 6 || len(d.Actions) > 4 {
		return invalid
	}
	if d.Level != "" && !slices.Contains(securityLevels, d.Level) {
		return invalid
	}
	for _, action := range d.Actions {
		if !slices.Contains(securityActions, action) {
			return invalid
		}
	}
	for _, r := range d.Roles {
		if !slices.Contains([]string{"admin", "auditor", "governor", "provider", "consumer", "operator"}, r) {
			return invalid
		}
	}
	for _, f := range d.MaskFields {
		if !valid(f, 128) {
			return invalid
		}
	}
	if len(d.RowFilters) > 0 && (kind != "policy" || d.Resource == nil) {
		return problem(400, "SECURITY_FILTER_RESOURCE_REQUIRED", "行过滤须绑定具体资源版本")
	}
	if e := securityValidateRowFilters(d.RowFilters, nil); e != nil {
		return e
	}
	if !validGovernanceRef(d.Template) {
		return invalid
	}
	switch kind {
	case "classification":
		if d.Resource == nil || d.Level == "" {
			return invalid
		}
	case "policy":
		if (d.Resource == nil && d.Level == "") || len(d.Actions) == 0 || (!d.Deny && len(d.MaskFields) == 0 && !d.RequireProof && !d.Visible && !d.FileMark && !d.Invisible && len(d.RowFilters) == 0) {
			return invalid
		}
		if len(d.MaskFields) > 0 && d.Resource == nil {
			return problem(400, "SECURITY_MASK_RESOURCE_REQUIRED", "额外脱敏字段须绑定具体资源版本")
		}
		if d.Template == nil && (d.Visible || d.FileMark || d.Invisible) {
			return invalid
		}
	case "watermark":
		if !valid(d.Text, 400) || strings.ContainsAny(d.Text, "\r\n\x00") {
			return invalid
		}
		residual := d.Text
		for _, v := range securityVariables {
			residual = strings.ReplaceAll(residual, "{"+v+"}", "")
		}
		if strings.ContainsAny(residual, "{}<>") {
			return invalid
		}
	case "recognition":
		if d.Level == "" || !valid(d.Pattern, 200) {
			return invalid
		}
		if _, e := regexp.Compile(d.Pattern); e != nil {
			return invalid
		}
	case "risk_rule":
		if d.Resource == nil || d.Threshold < 1 || d.Threshold > 100000 || d.WindowMinutes < 1 || d.WindowMinutes > 1440 {
			return invalid
		}
	}
	return nil
}

func securityNormalizeDefinition(d SecurityDefinition) SecurityDefinition {
	if d.RowFilters == nil {
		d.RowFilters = []SecurityRowFilter{}
	}
	if d.Actions == nil {
		d.Actions = []string{}
	}
	if d.Roles == nil {
		d.Roles = []string{}
	}
	if d.MaskFields == nil {
		d.MaskFields = []string{}
	}
	return d
}
