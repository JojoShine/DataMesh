package workflow

import (
	"encoding/json"
	"math"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
)

type AssetProduct struct {
	ID            string    `json:"id" gorm:"primaryKey"`
	DepartmentID  string    `json:"department_id"`
	OwnerID       string    `json:"owner_id"`
	State         string    `json:"state"`
	Version       int       `json:"version"`
	DraftID       string    `json:"draft_id"`
	PublishedID   string    `json:"published_id"`
	ReplacementID string    `json:"replacement_id"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (AssetProduct) TableName() string { return "asset_products" }

type AssetProductContract struct {
	FreshnessSeconds int      `json:"freshness_seconds"`
	MaxFailureRate   *float64 `json:"max_failure_rate"`
}
type AssetProductDefinition struct {
	Name              string               `json:"name"`
	Description       string               `json:"description"`
	Domain            *GovernanceRef       `json:"domain"`
	Terms             []GovernanceRef      `json:"terms"`
	Resources         []Resource           `json:"resources"`
	Contract          AssetProductContract `json:"contract"`
	UsageRestrictions string               `json:"usage_restrictions"`
}
type AssetProductMember struct {
	Resource Resource     `json:"resource"`
	Name     string       `json:"name"`
	Fields   []assetField `json:"fields"`
}
type AssetProductSemantic struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type AssetProductSemantics struct {
	Domain *AssetProductSemantic  `json:"domain"`
	Terms  []AssetProductSemantic `json:"terms"`
}
type AssetProductRevision struct {
	ID         string `gorm:"primaryKey"`
	ProductID  string
	State      string
	Definition json.RawMessage `gorm:"type:jsonb"`
	Members    json.RawMessage `gorm:"type:jsonb"`
	Semantics  json.RawMessage `gorm:"type:jsonb"`
	ReviewerID string
	ReviewNote string
	ValidUntil *time.Time
	CreatedAt  time.Time
}

func (AssetProductRevision) TableName() string { return "asset_product_revisions" }

type AssetProductCheck struct {
	ID         string          `json:"id" gorm:"primaryKey"`
	ProductID  string          `json:"product_id"`
	RevisionID string          `json:"revision_id"`
	ActorID    string          `json:"-"`
	State      string          `json:"state"`
	Items      json.RawMessage `json:"items" gorm:"type:jsonb"`
	CheckedAt  time.Time       `json:"checked_at"`
}

func (AssetProductCheck) TableName() string { return "asset_product_checks" }

type assetContractCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
type assetContractResult struct {
	Resource Resource             `json:"resource"`
	Name     string               `json:"name"`
	State    string               `json:"state"`
	Checks   []assetContractCheck `json:"checks"`
}

func assetProductManager(p identity.Principal) bool { return role(p, "provider", "governor") }
func assetProductManage(p identity.Principal, v AssetProduct) bool {
	return admin(p) || (assetProductManager(p) && v.OwnerID == p.UserID && v.DepartmentID == p.DepartmentID)
}
func assetProductReview(p identity.Principal, v AssetProduct) bool {
	return v.OwnerID != p.UserID && role(p, "governor") && (admin(p) || v.DepartmentID == p.DepartmentID)
}
func validateAssetProduct(v AssetProductDefinition) error {
	if !valid(v.Name, 200) || !valid(v.Description, 4000) || len(v.UsageRestrictions) > 4000 || len(v.Resources) == 0 || len(v.Resources) > 10 || len(v.Terms) > 20 || !validGovernanceRef(v.Domain) {
		return invalid
	}
	if v.Contract.FreshnessSeconds < 0 || v.Contract.FreshnessSeconds > 31536000 {
		return invalid
	}
	if rate := v.Contract.MaxFailureRate; rate != nil && (math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0 || *rate > 1) {
		return invalid
	}
	seen := map[string]bool{}
	for _, r := range v.Resources {
		key := resourceKey(r)
		if !valid(r.ID, 128) || r.Version < 1 || !slices.Contains([]string{"raw_object", "governed_dataset", "table_resource", "file_resource", "api_resource"}, r.Type) || seen[key] {
			return invalid
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, r := range v.Terms {
		if !valid(r.ID, 64) || r.Version < 1 || seen[r.ID] {
			return invalid
		}
		seen[r.ID] = true
	}
	return nil
}
func assetCheckState(checks []assetContractCheck) string {
	state := "passed"
	for _, c := range checks {
		if c.Status == "violated" {
			return "violated"
		}
		if c.Status == "unknown" {
			state = "unknown"
		}
	}
	return state
}
func assetContractEvaluate(member AssetProductMember, contract AssetProductContract, e *assetEvidence, now time.Time) []assetContractCheck {
	if e == nil || e.Notice != "" {
		return []assetContractCheck{{"sharing", "violated", "共享已暂停、版本变化或资源不可用，请核对原目录。"}}
	}
	checks := []assetContractCheck{{"sharing", "passed", "所引用的共享版本当前可核验。"}}
	schemaOK := true
	for _, pinned := range member.Fields {
		found := false
		for _, current := range e.Fields {
			if pinned.Name == current.Name && pinned.LogicalType == current.LogicalType && pinned.Sensitive == current.Sensitive {
				found = true
				break
			}
		}
		schemaOK = schemaOK && found
	}
	if schemaOK {
		checks = append(checks, assetContractCheck{"schema", "passed", "审核时固定的共享字段、类型和保护标识保持兼容。"})
	} else {
		checks = append(checks, assetContractCheck{"schema", "violated", "共享字段、类型或保护标识与产品审核快照不兼容。"})
	}
	if contract.FreshnessSeconds > 0 {
		c := assetContractCheck{"freshness", "unknown", "未提供可靠生成时间，无法核对成果生成时效；这不是源数据截止时间。"}
		if e.GeneratedAt != nil && !e.GeneratedAt.After(now) {
			c.Status = "passed"
			c.Message = "成果生成时间满足约定；不代表源数据实时性。"
			if now.Sub(*e.GeneratedAt) > time.Duration(contract.FreshnessSeconds)*time.Second {
				c.Status = "violated"
				c.Message = "当前成果生成时间已超过约定间隔。"
			}
		}
		checks = append(checks, c)
	}
	if contract.MaxFailureRate != nil {
		c := assetContractCheck{"failure_rate", "unknown", "缺少实际输出及运行隔离记录，无法计算隔离比例。"}
		if e.OutputRows != nil && e.RejectedRows != nil && *e.OutputRows >= 0 && *e.RejectedRows >= 0 && float64(*e.OutputRows)+float64(*e.RejectedRows) > 0 {
			rate := float64(*e.RejectedRows) / (float64(*e.OutputRows) + float64(*e.RejectedRows))
			c.Status = "passed"
			c.Message = "当次运行隔离比例未超过约定，仅覆盖该次运行。"
			if rate > *contract.MaxFailureRate {
				c.Status = "violated"
				c.Message = "当次运行隔离比例超过约定。"
			}
		}
		checks = append(checks, c)
	}
	return checks
}
