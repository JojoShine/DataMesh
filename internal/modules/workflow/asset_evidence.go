package workflow

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type assetField struct {
	Name         string `json:"name"`
	BusinessName string `json:"business_name"`
	Description  string `json:"description"`
	LogicalType  string `json:"logical_type"`
	Sensitive    bool   `json:"sensitive"`
}
type assetEvidence struct {
	Resource          Resource       `json:"resource"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Provider          string         `json:"provider"`
	Owner             string         `json:"owner"`
	UpdateFrequency   string         `json:"update_frequency"`
	UsageRestrictions string         `json:"usage_restrictions"`
	Fields            []assetField   `json:"fields"`
	Parameters        []APIParameter `json:"parameters"`
	DeliveryMode      string         `json:"delivery_mode"`
	GeneratedAt       *time.Time     `json:"generated_at"`
	ArchivedAt        *time.Time     `json:"archived_at"`
	RegisteredAt      *time.Time     `json:"registered_at"`
	DataCutoff        *time.Time     `json:"data_cutoff"`
	QualityChecks     int            `json:"quality_checks"`
	OutputValidated   bool           `json:"output_validated"`
	OutputRows        *int64         `json:"output_rows"`
	RejectedRows      *int64         `json:"rejected_rows"`
	SnapshotScope     string         `json:"snapshot_scope"`
	UpstreamState     string         `json:"upstream_state"`
	UpstreamAttemptAt *time.Time     `json:"upstream_attempt_at"`
	Notice            string         `json:"notice"`
}

// Only the reviewed field mapping crosses the consumer boundary. Internal SQL,
// source credentials, run definitions and record-level quality failures do not.
func (s *Server) assetDetail(c *gin.Context) {
	version, e := intQuery(c, "contract_version", 1, 100000000)
	if e != nil {
		s.fail(c, e)
		return
	}
	ref := Resource{Type: c.Query("resource_type"), ID: c.Query("resource_id"), Version: version}
	out, e := s.loadAssetEvidence(s.db.WithContext(c.Request.Context()), ref)
	if e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, out)
}
func (s *Server) loadAssetEvidence(tx *gorm.DB, ref Resource) (assetEvidence, error) {
	var out assetEvidence
	snap, e := resolveActiveSharing(tx, ref, false)
	if e != nil {
		return out, e
	}
	rev, e := get(tx, "directory_revision", snap.DirectoryRevisionID, false)
	if e != nil {
		return out, e
	}
	d, e := decode[DirectoryRevisionData](rev.Body)
	if e != nil {
		return out, e
	}
	mount, e := get(tx, "mount_revision", snap.MountRevisionID, false)
	if e != nil {
		return out, e
	}
	m, e := decode[MountRevisionData](mount.Body)
	if e != nil {
		return out, e
	}
	r, fields, e := resource(tx, ref, false)
	if e != nil {
		return out, e
	}
	out = assetEvidence{Resource: ref, Name: d.Definition.Name, Description: d.Definition.Description, Provider: d.Definition.ProviderName, UpdateFrequency: d.Definition.UpdateFrequency, UsageRestrictions: d.Definition.UsageRestrictions, Fields: []assetField{}, Parameters: []APIParameter{}, DeliveryMode: "snapshot"}
	if d.Definition.ResponsibleUserID != "" {
		if e = tx.Table("users").Select("username").Where("id=?", d.Definition.ResponsibleUserID).Scan(&out.Owner).Error; e != nil {
			return out, e
		}
	}
	for _, item := range d.Definition.Items {
		for _, f := range fields {
			if m.Mapping[item.Name] == f.Name {
				out.Fields = append(out.Fields, assetField{f.Name, item.BusinessName, item.Description, f.LogicalType, item.Sensitive || f.Sensitive})
			}
		}
	}
	var dataset DatasetData
	if ref.Type == "governed_dataset" {
		dataset, e = decode[DatasetData](r.Body)
	}
	if ref.Type == "file_resource" {
		out.RegisteredAt = &r.CreatedAt
	}
	if ref.Type == "table_resource" {
		out.DeliveryMode = "source_query"
	}
	if ref.Type == "api_resource" {
		var managed ManagedResource
		managed, e = decode[ManagedResource](r.Body)
		if e == nil {
			var config APIConfig
			config, e = decode[APIConfig](managed.Config)
			if e == nil {
				out.Parameters = config.Parameters
				out.DeliveryMode = "source_query"
				if config.DataService != nil {
					out.DeliveryMode = config.DataService.Mode
					_, dataset, e = s.serviceSource(tx.Statement.Context, tx, config.DataService, "")
					var p *Problem
					if errors.As(e, &p) || errors.Is(e, gorm.ErrRecordNotFound) {
						out.Notice = "当前接口来源暂不可交付，请联系提供部门确认。"
						e = nil
						dataset = DatasetData{}
					}
				}
			}
		}
	}
	if e == nil && dataset.GovernanceRunID != "" {
		e = assetRunEvidence(tx, dataset, &out)
	}
	if e != nil {
		return out, e
	}
	return out, nil
}
func assetRunEvidence(tx *gorm.DB, dataset DatasetData, out *assetEvidence) error {
	var run GovernanceRun
	if e := tx.Where("id=?", dataset.GovernanceRunID).Take(&run).Error; e != nil {
		return e
	}
	def, e := decode[governanceFlowExecution](run.Definition)
	if e != nil {
		return e
	}
	report, e := decode[governanceFlowReport](run.Result)
	if e != nil {
		return e
	}
	if run.State == "succeeded" {
		out.GeneratedAt = run.FinishedAt
		out.OutputRows = &report.OutputRows
		out.RejectedRows = &report.RejectedRows
		out.OutputValidated = !report.Unmodeled && (report.Model.ID != "" || report.GeneratedModel != nil)
		out.SnapshotScope = report.SnapshotScope
		for _, pin := range def.Pinned {
			if pin.Kind == "dataflow" && pin.Ref == def.Flow && pin.Definition.Dataflow != nil {
				for _, n := range pin.Definition.Dataflow.Nodes {
					if n.Kind == "quality" {
						out.QualityChecks++
					}
				}
			}
		}
	}
	// A more recent processing attempt is not proof that this shared snapshot changed.
	var latest GovernanceRun
	e = tx.Where("kind='dataflow' AND workspace_id IS NOT DISTINCT FROM NULLIF(?, '') AND department_id=? AND definition->'flow'->>'id'=?", run.WorkspaceID, run.DepartmentID, def.Flow.ID).Order("created_at DESC,id DESC").Take(&latest).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	if e == nil {
		out.UpstreamState = latest.State
		out.UpstreamAttemptAt = &latest.CreatedAt
	}
	var export GovernanceExport
	e = tx.Where("run_id=? AND state='succeeded'", run.ID).Order("created_at DESC,id DESC").Take(&export).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	if e == nil {
		manifest, err := decode[governanceExportManifest](export.Manifest)
		if err != nil {
			return err
		}
		out.ArchivedAt = &manifest.CreatedAt
	}
	return nil
}
