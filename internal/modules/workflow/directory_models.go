package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"regexp"
	"slices"
	"strings"
	"time"
)

type pathMutation func(*gorm.DB, identity.Principal, string, []byte) (int, json.RawMessage, string, error)

type DirectoryItem struct {
	Name         string `json:"name"`
	BusinessName string `json:"business_name"`
	LogicalType  string `json:"logical_type"`
	Length       *int   `json:"length"`
	Precision    *int   `json:"precision"`
	Scale        *int   `json:"scale"`
	Nullable     bool   `json:"nullable"`
	Sensitive    bool   `json:"sensitive"`
	Description  string `json:"description"`
}
type DirectoryDefinition struct {
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	Classification         string          `json:"classification"`
	ClassificationPath     string          `json:"classification_path"`
	ResourceCode           string          `json:"resource_code"`
	DictionaryID           string          `json:"dictionary_id"`
	ProviderName           string          `json:"provider_name"`
	ProviderCode           string          `json:"provider_code"`
	ResponsibleUserID      string          `json:"responsible_user_id"`
	UpdateFrequency        string          `json:"update_frequency"`
	Format                 string          `json:"format"`
	DataLayer              string          `json:"data_layer"`
	Items                  []DirectoryItem `json:"items"`
	SharingType            string          `json:"sharing_type"`
	SharingConditions      string          `json:"sharing_conditions"`
	NonSharingReason       string          `json:"non_sharing_reason"`
	LegalBasis             string          `json:"legal_basis"`
	SharingMethod          string          `json:"sharing_method"`
	UsageRestrictions      string          `json:"usage_restrictions"`
	SecurityClassification string          `json:"security_classification"`
	OpenToPublic           bool            `json:"open_to_public"`
	OpenConditions         string          `json:"open_conditions"`
	DepartmentApprovalRef  string          `json:"department_approval_ref"`
	AssessmentRef          string          `json:"assessment_ref"`
	RelatedResourceCodes   []string        `json:"related_resource_codes"`
}
type DirectoryData struct {
	DraftID      string `json:"draft_id"`
	PublishedID  string `json:"published_id"`
	ResourceCode string `json:"resource_code"`
}
type DirectoryRevisionData struct {
	Definition         DirectoryDefinition `json:"definition"`
	SubmittedBy        string              `json:"submitted_by"`
	DepartmentReviewer string              `json:"department_reviewer"`
	AuthorityReviewer  string              `json:"authority_reviewer"`
	Reason             string              `json:"reason"`
}
type MountData struct {
	DraftID           string `json:"draft_id"`
	ActiveID          string `json:"active_id"`
	ResourceKey       string `json:"resource_key"`
	GovernanceVariant bool   `json:"governance_variant,omitempty"`
	ServiceVariant    bool   `json:"service_variant,omitempty"`
}
type MountRevisionData struct {
	DirectoryRevisionID string            `json:"directory_revision_id"`
	Resource            Resource          `json:"resource"`
	Mapping             map[string]string `json:"mapping"`
	Validation          *MountValidation  `json:"validation"`
	SubmittedBy         string            `json:"submitted_by"`
	ApprovedBy          string            `json:"approved_by"`
	Reason              string            `json:"reason"`
}
type MountValidation struct {
	CheckedAt time.Time       `json:"checked_at"`
	Passed    bool            `json:"passed"`
	Checks    []MountCheck    `json:"checks"`
	Watermark json.RawMessage `json:"watermark"`
}
type MountCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}
type ReviewData struct {
	TargetKind  string `json:"target_kind"`
	TargetID    string `json:"target_id"`
	RevisionID  string `json:"revision_id"`
	Stage       string `json:"stage"`
	SubmittedBy string `json:"submitted_by"`
	ReviewerID  string `json:"reviewer_id"`
	Reason      string `json:"reason"`
}
type ResourceAssignment struct {
	UserID       string    `json:"user_id"`
	DepartmentID string    `json:"department_id"`
	ResourceID   string    `json:"resource_id"`
	Capability   string    `json:"capability"`
	ExpiresAt    time.Time `json:"expires_at"`
	Reason       string    `json:"reason"`
}
type ProcessingAuthorization struct {
	Resource     Resource  `json:"resource"`
	UserID       string    `json:"user_id"`
	Fields       []string  `json:"fields"`
	Purpose      string    `json:"purpose"`
	ExpiresAt    time.Time `json:"expires_at"`
	AllowDerived bool      `json:"allow_derived"`
	AllowSharing bool      `json:"allow_sharing"`
	SharingBasis string    `json:"sharing_basis"`
	ApprovalRef  string    `json:"approval_ref"`
}
type DirectoryDictionary struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Source          string   `json:"source"`
	Verified        bool     `json:"verified"`
	TestOnly        bool     `json:"test_only"`
	CodePattern     string   `json:"code_pattern"`
	CodeTemplate    string   `json:"code_template,omitempty"`
	SequenceWidth   int      `json:"sequence_width,omitempty"`
	Classifications []string `json:"classifications"`
	VerificationRef string   `json:"verification_ref"`
}
type SharingSnapshot struct {
	DirectoryID         string `json:"directory_id"`
	DirectoryRevisionID string `json:"directory_revision_id"`
	MountID             string `json:"mount_id"`
	MountRevisionID     string `json:"mount_revision_id"`
}
type VersionAction struct {
	ExpectedVersion int    `json:"expected_version"`
	Reason          string `json:"reason"`
}

func resourceKey(r Resource) string { return fmt.Sprintf("%s:%s:%d", r.Type, r.ID, r.Version) }
func validateDirectory(v DirectoryDefinition, complete bool) error {
	if !valid(v.Name, 200) || len(v.Description) > 2000 || len(v.Items) > 100 {
		return invalid
	}
	if !slices.Contains([]string{"base", "theme", "department"}, v.Classification) || !slices.Contains([]string{"raw_object", "governed_dataset"}, v.DataLayer) || !slices.Contains([]string{"unconditional", "conditional", "not_shared"}, v.SharingType) {
		return invalid
	}
	if !slices.Contains([]string{"database", "api", "file"}, v.Format) {
		return invalid
	}
	seen := map[string]bool{}
	for _, f := range v.Items {
		if !valid(f.Name, 128) || !valid(f.BusinessName, 200) || !slices.Contains([]string{"integer", "string", "decimal", "boolean", "date", "datetime"}, f.LogicalType) || seen[f.Name] || len(f.Description) > 1000 {
			return invalid
		}
		seen[f.Name] = true
		if f.Length != nil && (*f.Length < 1 || *f.Length > 1000000) {
			return invalid
		}
		if f.Precision != nil && (*f.Precision < 1 || *f.Precision > 38) {
			return invalid
		}
		if f.Scale != nil && (*f.Scale < 0 || f.Precision == nil || *f.Scale > *f.Precision) {
			return invalid
		}
	}
	if !complete {
		return nil
	}
	for _, s := range []string{v.Description, v.ClassificationPath, v.ResourceCode, v.DictionaryID, v.ProviderName, v.ProviderCode, v.ResponsibleUserID, v.UpdateFrequency, v.UsageRestrictions, v.SecurityClassification, v.DepartmentApprovalRef, v.AssessmentRef} {
		if !valid(s, 2000) {
			return invalid
		}
	}
	if len(v.Items) == 0 && v.Format != "file" {
		return invalid
	}
	if v.SharingType == "conditional" && !valid(v.SharingConditions, 2000) {
		return problem(400, "SHARING_CONDITIONS_REQUIRED", "有条件共享必须明确范围和使用条件")
	}
	if v.SharingType == "not_shared" && (!valid(v.NonSharingReason, 2000) || !valid(v.LegalBasis, 2000)) {
		return problem(400, "NON_SHARING_BASIS_REQUIRED", "不予共享必须说明理由及法律依据")
	}
	if v.OpenToPublic && !valid(v.OpenConditions, 2000) {
		return invalid
	}
	method := map[string]string{"database": "query", "file": "download", "api": "invoke"}[v.Format]
	if v.SharingType != "not_shared" && v.SharingMethod != method {
		return problem(409, "DELIVERY_METHOD_UNAVAILABLE", "共享方式必须与资源载体匹配")
	}
	return nil
}
func validateDictionary(v DirectoryDictionary) error {
	if err := validateCodeTemplate(v.CodeTemplate, v.SequenceWidth); err != nil {
		return err
	}
	if !valid(v.Name, 200) || !valid(v.Version, 100) || !valid(v.Source, 1000) || !valid(v.VerificationRef, 1000) || !v.Verified || len(v.Classifications) == 0 || len(v.CodePattern) > 300 || !strings.HasPrefix(v.CodePattern, "^") || !strings.HasSuffix(v.CodePattern, "$") {
		return invalid
	}
	_, err := regexp.Compile(v.CodePattern)
	if err != nil {
		return invalid
	}
	return nil
}
