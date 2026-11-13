package workflow

import (
	"database/sql"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type governanceDashboardState struct {
	State string `json:"state"`
	Count int64  `json:"count"`
}
type governanceDashboardDefinition struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Count int64  `json:"count"`
}
type governanceDashboardResource struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}
type governanceDashboardOutput struct {
	State        string `json:"state"`
	Quality      string `json:"quality"`
	Completeness string `json:"completeness"`
	Count        int64  `json:"count"`
}
type governanceDashboardRunDay struct {
	Day       string `json:"day"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
	Other     int64  `json:"other"`
}
type governanceDashboardQualityDay struct {
	Day      string `json:"day"`
	Checked  int64  `json:"checked"`
	Passed   int64  `json:"passed"`
	Rejected int64  `json:"rejected"`
	Runs     int64  `json:"runs"`
}
type governanceDashboardLightweight struct {
	Units    int64                      `json:"units"`
	Files    int64                      `json:"files"`
	Runs     []governanceDashboardState `json:"runs"`
	Checked  int64                      `json:"checked"`
	Passed   int64                      `json:"passed"`
	Rejected int64                      `json:"rejected"`
}
type governanceDashboardReport struct {
	Capabilities    governanceDashboardCapabilities `json:"capabilities"`
	FlowModes       []governanceDashboardResource   `json:"flow_modes"`
	Streams         []governanceDashboardState      `json:"streams"`
	ScheduleTargets []governanceDashboardDefinition `json:"schedule_targets"`
	QualityRuns     int64                           `json:"quality_runs"`
	Lightweight     governanceDashboardLightweight  `json:"lightweight"`

	Days               int                             `json:"days"`
	Timezone           string                          `json:"timezone"`
	Scope              string                          `json:"scope"`
	StartedAt          time.Time                       `json:"started_at"`
	ObservedAt         time.Time                       `json:"observed_at"`
	Definitions        []governanceDashboardDefinition `json:"definitions"`
	Resources          []governanceDashboardResource   `json:"resources"`
	MetadataCollection []governanceDashboardState      `json:"metadata_collection"`
	MetadataPending    int64                           `json:"metadata_pending"`
	Observations       int64                           `json:"observations"`
	Rules              []governanceDashboardState      `json:"rules"`
	Outputs            []governanceDashboardOutput     `json:"outputs"`
	Runs               []governanceDashboardDefinition `json:"runs"`
	RunTrend           []governanceDashboardRunDay     `json:"run_trend"`
	QualityTrend       []governanceDashboardQualityDay `json:"quality_trend"`
	Schedules          []governanceDashboardState      `json:"schedules"`
	Issues             []governanceDashboardState      `json:"issues"`
	OverdueIssues      int64                           `json:"overdue_issues"`
	Alerts             []governanceDashboardState      `json:"alerts"`
	AlertRules         int64                           `json:"alert_rules"`
	RetentionPolicies  int64                           `json:"retention_policies"`
	PurgedSnapshots    int64                           `json:"purged_snapshots"`
	LifecycleOutputs   int64                           `json:"lifecycle_outputs"`
	Migrations         []governanceDashboardState      `json:"migrations"`
	ModelDeployments   []governanceDashboardState      `json:"model_deployments"`
	LineageRuns        int64                           `json:"lineage_runs"`
}

func (s *Server) governanceDashboard(c *gin.Context) {
	days, err := intQuery(c, "days", 30, 90)
	if err != nil {
		s.fail(c, err)
		return
	}
	zone := c.DefaultQuery("timezone", "UTC")
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" || zone == "" {
		s.fail(c, invalid)
		return
	}
	p := principal(c)
	now := time.Now().UTC()
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1-days)
	out := governanceDashboardReport{
		FlowModes: []governanceDashboardResource{}, Streams: []governanceDashboardState{}, ScheduleTargets: []governanceDashboardDefinition{}, Lightweight: governanceDashboardLightweight{Runs: []governanceDashboardState{}},
		Days: days, Timezone: zone, Scope: "visible", StartedAt: start.UTC(), ObservedAt: now,
		Definitions: []governanceDashboardDefinition{}, Resources: []governanceDashboardResource{}, Rules: []governanceDashboardState{},
		Outputs: []governanceDashboardOutput{}, Runs: []governanceDashboardDefinition{}, RunTrend: []governanceDashboardRunDay{}, QualityTrend: []governanceDashboardQualityDay{},
		Schedules: []governanceDashboardState{}, Issues: []governanceDashboardState{}, Alerts: []governanceDashboardState{}, Migrations: []governanceDashboardState{}, ModelDeployments: []governanceDashboardState{},
	}
	if admin(p) {
		out.Scope = "all"
	}
	// One read-only snapshot keeps totals, distributions and trend series comparable.
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		defs := tx.Table("governance_definitions d").Joins("JOIN governance_versions v ON v.definition_id=d.id AND v.version=d.latest_version")
		if !admin(p) {
			defs = tx.Table("governance_definitions d").Joins("JOIN governance_versions v ON v.definition_id=d.id AND v.version=CASE WHEN d.kind='domain' AND (SELECT lv.definition->>'domain_scope' FROM governance_versions lv WHERE lv.definition_id=d.id AND lv.version=d.latest_version)='enterprise' THEN (SELECT MAX(pv.version) FROM governance_versions pv WHERE pv.definition_id=d.id AND pv.state='published') ELSE d.latest_version END").Where("d.department_id=? OR (d.kind='domain' AND v.definition->>'domain_scope'='enterprise' AND v.state='published')", p.DepartmentID)
		}
		// Shared business definitions remain available in every workspace; private
		// models and processing definitions are counted only in the current space.
		if id := workspaceScope(c.Request.Context()).ID; id != "" {
			defs = defs.Where("d.workspace_id=? OR (d.workspace_id IS NULL AND d.kind IN ?)", id, []string{"domain", "standard", "term"})
		}
		if e := governanceDashboardCapabilitiesRead(tx, p, defs, &out.Capabilities); e != nil {
			return e
		}
		if e := defs.Session(&gorm.Session{}).Select("d.kind,v.state,count(*) AS count").Group("d.kind,v.state").Scan(&out.Definitions).Error; e != nil {
			return e
		}
		if e := defs.Session(&gorm.Session{}).Where("d.kind='dataflow'").Select("v.execution_mode AS kind,count(*) AS count").Group("v.execution_mode").Scan(&out.FlowModes).Error; e != nil {
			return e
		}
		streams := workspaceQuery(tx.Model(&GovernanceStream{}), "workspace_id")
		if !admin(p) {
			streams = streams.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
		}
		if e := streams.Select("state,count(*) AS count").Group("state").Scan(&out.Streams).Error; e != nil {
			return e
		}
		// Private CSV units stay owner-only even on an administrator's dashboard.
		privateObjects := []governanceDashboardResource{}
		if e := tx.Model(&Record{}).Where("kind IN ? AND owner_id=? AND department_id=?", []string{lightweightUnitKind, lightweightUploadKind}, p.UserID, p.DepartmentID).Select("kind,count(*) AS count").Group("kind").Scan(&privateObjects).Error; e != nil {
			return e
		}
		for _, item := range privateObjects {
			if item.Kind == lightweightUnitKind {
				out.Lightweight.Units = item.Count
			} else {
				out.Lightweight.Files = item.Count
			}
		}
		privateRuns := tx.Model(&GovernanceRun{}).Where("workspace_id IS NULL AND kind='dataflow' AND actor_id=? AND department_id=? AND created_at>=? AND created_at<=?", p.UserID, p.DepartmentID, start, now)
		if e := privateRuns.Select("state,count(*) AS count").Group("state").Scan(&out.Lightweight.Runs).Error; e != nil {
			return e
		}
		var privateQuality struct{ Checked, Passed, Rejected int64 }
		if e := tx.Table("governance_run_events e").Joins("JOIN governance_runs r ON r.id=e.run_id").Where("r.workspace_id IS NULL AND r.kind='dataflow' AND r.actor_id=? AND r.department_id=? AND e.code='QUALITY_CHECKED' AND e.created_at>=? AND e.created_at<=?", p.UserID, p.DepartmentID, start, now).Select("COALESCE(sum(e.input_count),0) AS checked,COALESCE(sum(e.output_count),0) AS passed,COALESCE(sum(e.input_count-e.output_count),0) AS rejected").Scan(&privateQuality).Error; e != nil {
			return e
		}
		out.Lightweight.Checked, out.Lightweight.Passed, out.Lightweight.Rejected = privateQuality.Checked, privateQuality.Passed, privateQuality.Rejected
		resources := tx.Model(&Record{}).Where("kind IN ?", []string{"object", "dataset", "table_resource", "file_resource", "api_resource"})
		if !admin(p) {
			resources = resources.Where("department_id=? AND EXISTS (SELECT 1 FROM business_records a WHERE a.kind='assignment' AND a.state='active' AND a.body->>'user_id'=? AND a.body->>'capability'='govern_resource' AND a.department_id=business_records.department_id AND (a.body->>'resource_id'='*' OR a.body->>'resource_id'=business_records.id) AND (a.body->>'expires_at')::timestamptz>now())", p.DepartmentID, p.UserID)
		}
		if id := workspaceScope(c.Request.Context()).ID; id != "" {
			resources = resources.Where("kind IN ?", []string{"table_resource", "file_resource", "api_resource"}).Where("EXISTS(SELECT 1 FROM governance_workspace_sources ws WHERE ws.workspace_id=? AND ws.resource_id=business_records.id AND ws.resource_type=business_records.kind AND ws.active)", id)
		}
		if e := resources.Session(&gorm.Session{}).Select("kind,count(*) AS count").Group("kind").Scan(&out.Resources).Error; e != nil {
			return e
		}
		if e := tx.Model(&MetadataObservation{}).Where("object_id IN (?)", resources.Session(&gorm.Session{}).Select("id")).Count(&out.Observations).Error; e != nil {
			return e
		}
		out.MetadataCollection = []governanceDashboardState{}
		if e := tx.Model(&MetadataCollectionPolicy{}).Where("object_id IN (?)", resources.Session(&gorm.Session{}).Select("id")).Select("CASE WHEN requested THEN 'queued' WHEN failures>0 THEN 'failed' WHEN NOT enabled THEN 'paused' WHEN last_success_at IS NULL THEN 'pending' WHEN last_success_at < now() - interval_minutes * INTERVAL '90 seconds' THEN 'overdue' ELSE 'healthy' END AS state,count(*) AS count").Group("state").Scan(&out.MetadataCollection).Error; e != nil {
			return e
		}
		if e := tx.Model(&MetadataChangeReview{}).Where("object_id IN (?) AND state IN ('pending','deferred')", resources.Session(&gorm.Session{}).Select("id")).Count(&out.MetadataPending).Error; e != nil {
			return e
		}
		rules := tx.Model(&Record{}).Where("kind='rule'")
		if !admin(p) {
			rules = rules.Where("department_id=?", p.DepartmentID)
		}
		if e := rules.Select("state,count(*) AS count").Group("state").Scan(&out.Rules).Error; e != nil {
			return e
		}
		outputs := workspaceQuery(tx.Model(&Record{}).Where("kind='dataset'"), workspaceDatasetColumn)
		if !admin(p) {
			outputs = outputs.Where("state='published' OR department_id=?", p.DepartmentID)
		}
		if e := outputs.Select("state,COALESCE(body->>'quality_status','not_checked') AS quality,COALESCE(body->>'completeness_status','pending') AS completeness,count(*) AS count").Group("state,quality,completeness").Scan(&out.Outputs).Error; e != nil {
			return e
		}
		runs := workspaceQuery(tx.Model(&GovernanceRun{}), "workspace_id").Where("workspace_id IS NOT NULL AND created_at>=? AND created_at<=? AND kind<>'dataflow_preview'", start, now)
		if !admin(p) {
			runs = runs.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
		}
		if e := runs.Session(&gorm.Session{}).Select("kind,state,count(*) AS count").Group("kind,state").Scan(&out.Runs).Error; e != nil {
			return e
		}
		if e := runs.Session(&gorm.Session{}).Select("to_char(created_at AT TIME ZONE ?,'YYYY-MM-DD') AS day,count(*) FILTER(WHERE state='succeeded') AS succeeded,count(*) FILTER(WHERE state IN ('failed','interrupted')) AS failed,count(*) FILTER(WHERE state NOT IN ('succeeded','failed','interrupted')) AS other", zone).Group("day").Order("day").Scan(&out.RunTrend).Error; e != nil {
			return e
		}
		quality := tx.Table("governance_run_events e").Joins("JOIN governance_runs r ON r.id=e.run_id").Where("r.workspace_id IS NOT NULL AND e.code='QUALITY_CHECKED' AND e.created_at>=? AND e.created_at<=?", start, now)
		quality = workspaceQuery(quality, "r.workspace_id")
		if !admin(p) {
			quality = quality.Where("r.department_id=? AND r.actor_id=?", p.DepartmentID, p.UserID)
		}
		if e := quality.Session(&gorm.Session{}).Distinct("r.id").Count(&out.QualityRuns).Error; e != nil {
			return e
		}
		if e := quality.Select("to_char(e.created_at AT TIME ZONE ?,'YYYY-MM-DD') AS day,sum(e.input_count) AS checked,sum(e.output_count) AS passed,sum(e.input_count-e.output_count) AS rejected,count(DISTINCT r.id) AS runs", zone).Group("day").Order("day").Scan(&out.QualityTrend).Error; e != nil {
			return e
		}
		for _, item := range []struct {
			table  string
			target *[]governanceDashboardState
		}{
			{"governance_schedules", &out.Schedules}, {"governance_migration_plans", &out.Migrations}, {"governance_model_deployments", &out.ModelDeployments},
		} {
			q := tx.Table(item.table)
			column := map[string]string{"governance_schedules": "workspace_id", "governance_migration_plans": "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_migration_plans.source_id)", "governance_model_deployments": "(SELECT d.workspace_id FROM governance_definitions d WHERE d.id=governance_model_deployments.model_id)", "governance_alert_rules": "workspace_id", "governance_retention_policies": "(SELECT d.workspace_id FROM governance_definitions d WHERE d.id=governance_retention_policies.flow_id)"}[item.table]
			if column != "" {
				q = workspaceQuery(q, column)
			}
			if !admin(p) {
				q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
			}
			if item.table == "governance_schedules" {
				if e := q.Session(&gorm.Session{}).Select("target_kind AS kind,state,count(*) AS count").Group("target_kind,state").Scan(&out.ScheduleTargets).Error; e != nil {
					return e
				}
			}
			if e := q.Select("state,count(*) AS count").Group("state").Scan(item.target).Error; e != nil {
				return e
			}
		}
		issues := workspaceQuery(tx.Model(&GovernanceQualityIssue{}), "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_quality_issues.run_id)").Where("(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_quality_issues.run_id) IS NOT NULL")
		if !admin(p) {
			issues = issues.Where("department_id=? AND (actor_id=? OR assignee_id=?)", p.DepartmentID, p.UserID, p.UserID)
		}
		if e := issues.Session(&gorm.Session{}).Select("state,count(*) AS count").Group("state").Scan(&out.Issues).Error; e != nil {
			return e
		}
		if e := issues.Session(&gorm.Session{}).Where("state NOT IN ('closed','superseded') AND due_at<?", now).Count(&out.OverdueIssues).Error; e != nil {
			return e
		}
		alerts := workspaceQuery(tx.Model(&GovernanceAlert{}), "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_alerts.run_id)").Where("(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_alerts.run_id) IS NOT NULL")
		if !admin(p) {
			alerts = alerts.Where("EXISTS(SELECT 1 FROM governance_alert_rules r WHERE r.id=governance_alerts.rule_id AND r.actor_id=? AND r.department_id=?)", p.UserID, p.DepartmentID)
		}
		if e := alerts.Select("state,count(*) AS count").Group("state").Scan(&out.Alerts).Error; e != nil {
			return e
		}
		for _, item := range []struct {
			table  string
			target *int64
		}{
			{"governance_alert_rules", &out.AlertRules}, {"governance_retention_policies", &out.RetentionPolicies},
		} {
			q := tx.Table(item.table)
			column := map[string]string{"governance_schedules": "workspace_id", "governance_migration_plans": "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_migration_plans.source_id)", "governance_model_deployments": "(SELECT d.workspace_id FROM governance_definitions d WHERE d.id=governance_model_deployments.model_id)", "governance_alert_rules": "workspace_id", "governance_retention_policies": "(SELECT d.workspace_id FROM governance_definitions d WHERE d.id=governance_retention_policies.flow_id)"}[item.table]
			if column != "" {
				q = workspaceQuery(q, column)
			}
			if !admin(p) {
				q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
			}
			if e := q.Count(item.target).Error; e != nil {
				return e
			}
		}
		purges := tx.Table("governance_snapshot_purges p").Joins("JOIN governance_runs r ON r.id=p.run_id")
		purges = workspaceQuery(purges, "r.workspace_id").Where("r.workspace_id IS NOT NULL")
		if !admin(p) {
			purges = purges.Where("r.department_id=? AND r.actor_id=?", p.DepartmentID, p.UserID)
		}
		if e := purges.Count(&out.PurgedSnapshots).Error; e != nil {
			return e
		}
		lifecycle := workspaceQuery(tx.Model(&Record{}).Where("kind='dataset' AND body->>'governance_run_id'<>''"), workspaceDatasetColumn)
		if !admin(p) {
			lifecycle = lifecycle.Where("owner_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		if e := lifecycle.Count(&out.LifecycleOutputs).Error; e != nil {
			return e
		}
		lineage := workspaceQuery(tx.Model(&GovernanceRun{}), "workspace_id").Where("workspace_id IS NOT NULL AND kind='dataflow' AND state='succeeded'")
		if !admin(p) {
			lineage = lineage.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
		}
		return lineage.Count(&out.LineageRuns).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, out)
}
