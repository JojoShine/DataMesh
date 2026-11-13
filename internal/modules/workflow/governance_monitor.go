package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"time"
)

type GovernanceMonitorPolicy struct {
	Parameters            map[string]string `json:"parameters,omitempty"`
	Resource              Resource          `json:"resource"`
	Fields                []string          `json:"fields"`
	TimeField             string            `json:"time_field"`
	FreshnessSeconds      int               `json:"freshness_seconds"`
	MaxNullRate           float64           `json:"max_null_rate"`
	MaxDuplicateRate      float64           `json:"max_duplicate_rate"`
	MaxVolumeChange       float64           `json:"max_volume_change"`
	MaxDistributionChange float64           `json:"max_distribution_change"`
}
type GovernanceMonitor struct {
	ID                string          `json:"id" gorm:"primaryKey"`
	WorkspaceID       string          `json:"workspace_id"`
	DepartmentID      string          `json:"department_id"`
	ActorID           string          `json:"actor_id"`
	Name              string          `json:"name"`
	Policy            json.RawMessage `json:"policy" gorm:"type:jsonb"`
	Enabled           bool            `json:"enabled"`
	IntervalMinutes   int             `json:"interval_minutes"`
	Version           int             `json:"version"`
	NextAt            time.Time       `json:"next_at"`
	PendingRunID      *string         `json:"pending_run_id"`
	BaselineID        *string         `json:"baseline_id"`
	LastObservationID *string         `json:"last_observation_id"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

func (GovernanceMonitor) TableName() string { return "governance_monitors" }

type GovernanceMonitorObservation struct {
	ResolutionState     string          `json:"resolution_state" gorm:"default:open"`
	ResolutionNote      string          `json:"resolution_note"`
	RetestObservationID *string         `json:"retest_observation_id"`
	Version             int             `json:"version" gorm:"default:1"`
	ID                  string          `json:"id" gorm:"primaryKey"`
	MonitorID           string          `json:"monitor_id"`
	RunID               *string         `json:"run_id"`
	PolicyVersion       int             `json:"policy_version"`
	Policy              json.RawMessage `json:"policy" gorm:"type:jsonb"`
	State               string          `json:"state"`
	Report              json.RawMessage `json:"report" gorm:"type:jsonb"`
	ErrorCode           string          `json:"error_code"`
	CreatedAt           time.Time       `json:"created_at"`
	FinishedAt          *time.Time      `json:"finished_at"`
}

func (GovernanceMonitorObservation) TableName() string { return "governance_monitor_observations" }
func validateMonitorPolicy(v GovernanceMonitorPolicy) error {
	if !valid(v.Resource.ID, 128) || v.Resource.Version < 1 || !slices.Contains([]string{"raw_object", "table_resource", "file_resource", "api_resource", "governed_dataset"}, v.Resource.Type) || len(v.Fields) < 1 || len(v.Fields) > 100 {
		return invalid
	}
	seen := map[string]bool{}
	for _, field := range v.Fields {
		if !valid(field, 128) || seen[field] {
			return invalid
		}
		seen[field] = true
	}
	if v.FreshnessSeconds < 0 || v.FreshnessSeconds > 366*86400 || v.FreshnessSeconds > 0 && (v.TimeField == "" || !seen[v.TimeField]) || v.TimeField != "" && !seen[v.TimeField] {
		return invalid
	}
	for _, rate := range []float64{v.MaxNullRate, v.MaxDuplicateRate, v.MaxDistributionChange} {
		if rate < 0 || rate > 1 {
			return invalid
		}
	}
	if v.MaxVolumeChange < 0 || v.MaxVolumeChange > 1000 {
		return invalid
	}
	return nil
}
func monitorAccess(tx *gorm.DB, p identity.Principal, id string, lock bool) (GovernanceMonitor, error) {
	var row GovernanceMonitor
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	e := q.Take(&row).Error
	if e != nil {
		return row, e
	}
	if !admin(p) && (row.ActorID != p.UserID || row.DepartmentID != p.DepartmentID) {
		return row, denied
	}
	if e = governanceWorkspaceMatch(tx.Statement.Context, row.WorkspaceID); e != nil {
		return row, e
	}
	if workspaceScope(tx.Statement.Context).Mutation {
		_, e = governanceWorkspaceWrite(tx, p, row.WorkspaceID)
	} else {
		_, e = governanceWorkspaceRead(tx, p, row.WorkspaceID, false)
	}
	return row, e
}
func (s *Server) monitorQueue(tx *gorm.DB, p identity.Principal, row *GovernanceMonitor) (GovernanceMonitorObservation, error) {
	var out GovernanceMonitorObservation
	if row.PendingRunID != nil {
		return out, problem(409, "GOVERNANCE_MONITOR_BUSY", "上次监测尚未结束，请等待完成")
	}
	cfg, e := decode[GovernanceMonitorPolicy](row.Policy)
	if e != nil {
		return out, e
	}
	scoped := workspaceContext(tx, row.WorkspaceID, false)
	_, _, runID, e := s.enqueueGovernanceProfile(scoped, p, governanceProfileInput{Resource: cfg.Resource, Fields: cfg.Fields, Mode: "full", Parameters: cfg.Parameters}, nil)
	if e != nil {
		return out, e
	}
	id, e := newID()
	if e != nil {
		return out, e
	}
	if cfg.TimeField != "" {
		var profile GovernanceRun
		if e = tx.Where("id=?", runID).Take(&profile).Error; e != nil {
			return out, e
		}
		definition, e := decode[governanceProfileDefinition](profile.Definition)
		if e != nil {
			return out, e
		}
		definition.BusinessTimeField = cfg.TimeField
		if e = tx.Model(&profile).Update("definition", encode(definition)).Error; e != nil {
			return out, e
		}
	}
	out = GovernanceMonitorObservation{ID: id, MonitorID: row.ID, RunID: &runID, PolicyVersion: row.Version, Policy: row.Policy, State: "queued", Report: json.RawMessage(`{}`)}
	if e = tx.Create(&out).Error; e != nil {
		return out, e
	}
	row.PendingRunID = &runID
	row.NextAt = time.Now().UTC().Add(time.Duration(row.IntervalMinutes) * time.Minute)
	return out, tx.Save(row).Error
}
func (s *Server) registerGovernanceMonitoring(api, write *gin.RouterGroup) {
	api.GET("/governance/monitors", s.require("governance", "governor"), func(c *gin.Context) {
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 15, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		q := workspaceQuery(s.db.WithContext(c.Request.Context()).Model(&GovernanceMonitor{}), "workspace_id")
		p := principal(c)
		if !admin(p) {
			q = q.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
		}
		var total int64
		rows := []GovernanceMonitor{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("updated_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	api.GET("/governance/monitors/:id/observations", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		row, e := monitorAccess(tx, principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 15, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		q := tx.Model(&GovernanceMonitorObservation{}).Where("monitor_id=?", row.ID)
		var total int64
		rows := []GovernanceMonitorObservation{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		for _, observation := range rows {
			policy, e := decode[GovernanceMonitorPolicy](observation.Policy)
			if e != nil {
				s.fail(c, e)
				return
			}
			if _, e = s.governanceInputBasisParameters(tx, principal(c), policy.Resource, policy.Fields, false, policy.Parameters); e != nil {
				s.fail(c, e)
				return
			}
		}
		c.JSON(200, gin.H{"monitor": row, "items": rows, "total": total, "page": page, "page_size": size})
	})
	write.POST("/governance/monitors/:id/resolve", s.require("governance", "governor"), s.mutatePath("monitor_resolve", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			ObservationID   string `json:"observation_id"`
			ExpectedVersion int    `json:"expected_version"`
			RetestID        string `json:"retest_id"`
			Note            string `json:"note"`
		}](b)
		if e != nil || !valid(input.Note, 4000) {
			return 0, nil, id, invalid
		}
		monitor, e := monitorAccess(tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		var original, retest GovernanceMonitorObservation
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND monitor_id=?", input.ObservationID, id).Take(&original).Error; e != nil {
			return 0, nil, id, e
		}
		var linked int64
		if e = tx.Model(&GovernanceQualityIssue{}).Where("observation_id=?", original.ID).Count(&linked).Error; e != nil {
			return 0, nil, id, e
		}
		if linked > 0 {
			return 0, nil, id, problem(409, "MONITOR_UNIFIED_ISSUE_REQUIRED", "此异常已登记统一整改，请在问题整改中复测并关闭")
		}
		if original.Version != input.ExpectedVersion || original.ResolutionState == "closed" || original.State != "alert" {
			return 0, nil, id, conflict
		}
		if e = tx.Where("id=? AND monitor_id=?", input.RetestID, id).Take(&retest).Error; e != nil {
			return 0, nil, id, e
		}
		if original.PolicyVersion != retest.PolicyVersion || retest.State != "passed" || !retest.CreatedAt.After(original.CreatedAt) || fingerprint(retest.Policy) != fingerprint(original.Policy) || fingerprint(monitor.Policy) != fingerprint(original.Policy) {
			return 0, nil, id, problem(409, "MONITOR_RETEST_REQUIRED", "需要相同配置与固定基线下的较新正常监测记录")
		}
		policy, e := decode[GovernanceMonitorPolicy](monitor.Policy)
		if e != nil {
			return 0, nil, id, e
		}
		if _, e = s.governanceInputBasisParameters(tx, p, policy.Resource, policy.Fields, false, policy.Parameters); e != nil {
			return 0, nil, id, e
		}
		original.ResolutionState = "closed"
		original.ResolutionNote = input.Note
		original.RetestObservationID = &retest.ID
		original.Version++
		e = tx.Save(&original).Error
		return 200, encode(original), id, e
	}))
	for _, path := range []string{"/governance/monitors", "/governance/monitors/:id"} {
		write.POST(path, s.require("governance", "governor"), s.mutatePath("monitor_save", s.monitorSave))
	}

	write.POST("/governance/monitors/:id/run", s.require("governance", "governor"), s.mutatePath("monitor_run", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[VersionAction](b)
		if e != nil {
			return 0, nil, id, e
		}
		row, e := monitorAccess(tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if row.Version != input.ExpectedVersion {
			return 0, nil, id, conflict
		}
		out, e := s.monitorQueue(tx, p, &row)
		return 201, encode(out), id, e
	}))
	write.POST("/governance/monitors/:id/baseline", s.require("governance", "governor"), s.mutatePath("monitor_baseline", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			ExpectedVersion int    `json:"expected_version"`
			ObservationID   string `json:"observation_id"`
		}](b)
		if e != nil {
			return 0, nil, id, e
		}
		row, e := monitorAccess(tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if row.Version != input.ExpectedVersion || row.PendingRunID != nil {
			return 0, nil, id, conflict
		}
		var out GovernanceMonitorObservation
		if e = tx.Where("id=? AND monitor_id=? AND policy=?::jsonb AND state IN ('passed','alert')", input.ObservationID, id, string(row.Policy)).Take(&out).Error; e != nil {
			return 0, nil, id, e
		}
		row.BaselineID = &out.ID
		row.Version++
		e = tx.Save(&row).Error
		return 200, encode(row), id, e
	}))
}

func (s *Server) monitorSave(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		Name            string                  `json:"name"`
		ExpectedVersion int                     `json:"expected_version"`
		Policy          GovernanceMonitorPolicy `json:"policy"`
		Enabled         bool                    `json:"enabled"`
		IntervalMinutes int                     `json:"interval_minutes"`
	}](b)
	if e != nil {
		return 0, nil, "", e
	}
	if !valid(input.Name, 200) || input.IntervalMinutes < 15 || input.IntervalMinutes > 43200 {
		return 0, nil, "", invalid
	}
	if e = validateMonitorPolicy(input.Policy); e != nil {
		return 0, nil, "", e
	}
	row := GovernanceMonitor{DepartmentID: p.DepartmentID, ActorID: p.UserID, Version: 1, NextAt: time.Now().UTC()}
	status := 201
	if id != "" {
		row, e = monitorAccess(tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if row.Version != input.ExpectedVersion || row.PendingRunID != nil {
			return 0, nil, id, conflict
		}
		row.Version++
		row.BaselineID = nil
		status = 200
	} else {
		if input.ExpectedVersion != 0 {
			return 0, nil, "", conflict
		}
		space, e := governanceWorkspaceCreateScope(tx, p, p.DepartmentID)
		if e != nil {
			return 0, nil, "", e
		}
		row.WorkspaceID = space.ID
		row.ID, e = newID()
		if e != nil {
			return 0, nil, "", e
		}
	}
	scoped := workspaceContext(tx, row.WorkspaceID, false)
	if e = governanceWorkspaceReference(scoped, p, row.WorkspaceID, input.Policy.Resource); e != nil {
		return 0, nil, id, e
	}
	if _, e = s.governanceInputBasisParameters(scoped, p, input.Policy.Resource, input.Policy.Fields, false, input.Policy.Parameters); e != nil {
		return 0, nil, id, e
	}
	row.Name = input.Name
	row.Policy = encode(input.Policy)
	row.Enabled = input.Enabled
	row.IntervalMinutes = input.IntervalMinutes
	row.NextAt = time.Now().UTC()
	if status == 201 {
		e = tx.Create(&row).Error
	} else {
		e = tx.Save(&row).Error
	}
	return status, encode(row), row.ID, e
}
