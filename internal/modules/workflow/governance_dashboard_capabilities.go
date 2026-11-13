package workflow

import (
	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

type governanceDashboardCapabilities struct {
	Monitors          int64 `json:"monitors"`
	OpenObservations  int64 `json:"open_observations"`
	Audits            int64 `json:"audits"`
	AuditViolations   int64 `json:"audit_violations"`
	MasterConflicts   int64 `json:"master_conflicts"`
	ConfirmedEntities int64 `json:"confirmed_entities"`
	Metrics           int64 `json:"metrics"`
	Scenarios         int64 `json:"scenarios"`
	PendingReleases   int64 `json:"pending_releases"`
}

func governanceDashboardCapabilitiesRead(tx *gorm.DB, p identity.Principal, defs *gorm.DB, out *governanceDashboardCapabilities) error {
	monitors := workspaceQuery(tx.Model(&GovernanceMonitor{}), "workspace_id")
	if !admin(p) {
		monitors = monitors.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
	}
	if e := monitors.Count(&out.Monitors).Error; e != nil {
		return e
	}
	observations := workspaceQuery(tx.Table("governance_monitor_observations o").Joins("JOIN governance_monitors m ON m.id=o.monitor_id"), "m.workspace_id").Where("o.state='alert' AND o.resolution_state<>'closed'")
	if !admin(p) {
		observations = observations.Where("m.actor_id=? AND m.department_id=?", p.UserID, p.DepartmentID)
	}
	if e := observations.Count(&out.OpenObservations).Error; e != nil {
		return e
	}
	audits := workspaceQuery(tx.Model(&GovernanceRun{}), "workspace_id").Where("kind='profile' AND state='succeeded' AND result->'standard_audit' IS NOT NULL")
	if !admin(p) {
		audits = audits.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
	}
	if e := audits.Session(&gorm.Session{}).Count(&out.Audits).Error; e != nil {
		return e
	}
	if e := audits.Select("COALESCE(sum((result->'standard_audit'->>'rejected_rows')::bigint),0)").Scan(&out.AuditViolations).Error; e != nil {
		return e
	}
	conflicts := workspaceQuery(tx.Table("governance_master_candidates c").Joins("JOIN governance_runs r ON r.id=c.run_id"), "r.workspace_id").Where("c.conflicts<>'[]'::jsonb AND NOT EXISTS(SELECT 1 FROM governance_master_revisions v WHERE v.candidate_id=c.id)")
	if !admin(p) {
		conflicts = conflicts.Where("r.actor_id=? AND r.department_id=?", p.UserID, p.DepartmentID)
	}
	if e := conflicts.Count(&out.MasterConflicts).Error; e != nil {
		return e
	}
	entities := workspaceQuery(tx.Table("governance_master_entities e").Joins("JOIN governance_master_revisions v ON v.entity_id=e.entity_id AND v.version=e.latest_version").Joins("JOIN governance_master_candidates c ON c.id=v.candidate_id").Joins("JOIN governance_runs r ON r.id=c.run_id"), "e.workspace_id")
	if !admin(p) {
		entities = entities.Where("r.actor_id=? AND r.department_id=?", p.UserID, p.DepartmentID)
	}
	if e := entities.Count(&out.ConfirmedEntities).Error; e != nil {
		return e
	}
	if e := defs.Session(&gorm.Session{}).Where("d.kind='dataflow' AND v.definition->'metric' IS NOT NULL").Count(&out.Metrics).Error; e != nil {
		return e
	}
	if e := defs.Session(&gorm.Session{}).Where("d.kind='scenario'").Count(&out.Scenarios).Error; e != nil {
		return e
	}
	releases := workspaceQuery(tx.Model(&Record{}), "body->>'workspace_id'").Where("kind='governance_release' AND state='submitted'")
	if !admin(p) {
		releases = releases.Where("department_id=?", p.DepartmentID)
	}
	return releases.Count(&out.PendingReleases).Error
}
