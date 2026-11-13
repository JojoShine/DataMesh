package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	_ "time/tzdata"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceSchedule struct {
	WorkspaceID       string          `json:"workspace_id"`
	Parameters        json.RawMessage `json:"parameters" gorm:"type:jsonb"`
	ID                string          `json:"id" gorm:"primaryKey"`
	DepartmentID      string          `json:"department_id"`
	ActorID           string          `json:"actor_id"`
	TargetKind        string          `json:"target_kind" gorm:"default:workflow"`
	Dataflow          *GovernanceRef  `json:"dataflow,omitempty" gorm:"-"`
	Workflow          GovernanceRef   `json:"workflow,omitzero" gorm:"-"`
	WorkflowID        string          `json:"-"`
	WorkflowVersion   int             `json:"-"`
	Timezone          string          `json:"timezone"`
	IntervalSeconds   int             `json:"interval_seconds"`
	Cron              string          `json:"cron"`
	ConcurrencyPolicy string          `json:"concurrency_policy"`
	MissedPolicy      string          `json:"missed_policy"`
	NextRunAt         time.Time       `json:"next_run_at"`
	State             string          `json:"state"`
	Version           int             `json:"version"`
	LastRunID         *string         `json:"last_run_id"`
	LastErrorCode     string          `json:"last_error_code"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type governanceScheduleInput struct {
	BaseVersion int `json:"base_version"`

	Parameters        map[string]json.RawMessage `json:"parameters"`
	Workflow          GovernanceRef              `json:"workflow"`
	Dataflow          *GovernanceRef             `json:"dataflow"`
	Timezone          string                     `json:"timezone"`
	IntervalSeconds   int                        `json:"interval_seconds"`
	Cron              string                     `json:"cron"`
	ConcurrencyPolicy string                     `json:"concurrency_policy"`
	MissedPolicy      string                     `json:"missed_policy"`
	NextRunAt         time.Time                  `json:"next_run_at"`
}

func (GovernanceSchedule) TableName() string { return "governance_schedules" }
func validateGovernanceSchedule(v GovernanceSchedule) error {
	_, _, targetErr := governanceScheduleTarget(v)
	if targetErr != nil || (v.Cron == "" && (v.IntervalSeconds < 60 || v.IntervalSeconds > 31536000)) || (v.Cron != "" && v.IntervalSeconds != 0) || v.ConcurrencyPolicy != "forbid" || (v.MissedPolicy != "latest" && v.MissedPolicy != "skip") || v.NextRunAt.IsZero() || v.Timezone == "Local" || (!strings.Contains(v.Timezone, "/") && v.Timezone != "UTC") {
		return invalid
	}
	if _, e := time.LoadLocation(v.Timezone); e != nil {
		return invalid
	}
	if v.Cron != "" {
		if _, e := governanceCronTime(v.Cron, v.Timezone, v.NextRunAt.Add(-time.Nanosecond), true); e != nil {
			return e
		}
	}
	return nil
}
func governanceScheduleDue(v GovernanceSchedule, now time.Time) (time.Time, time.Time, bool) {
	if now.Before(v.NextRunAt) {
		return time.Time{}, v.NextRunAt, false
	}
	if v.Cron != "" {
		fire, e := governanceCronTime(v.Cron, v.Timezone, now, false)
		if e != nil || fire.Before(v.NextRunAt) {
			return time.Time{}, v.NextRunAt, false
		}
		next, e := governanceCronTime(v.Cron, v.Timezone, now, true)
		if e != nil {
			return time.Time{}, v.NextRunAt, false
		}
		return fire, next, v.MissedPolicy != "skip" || fire.Equal(v.NextRunAt)
	}
	interval := time.Duration(v.IntervalSeconds) * time.Second
	if interval <= 0 {
		return time.Time{}, v.NextRunAt, false
	}
	missed := (now.Unix() - v.NextRunAt.Unix()) / int64(v.IntervalSeconds)
	fire := time.Unix(v.NextRunAt.Unix()+missed*int64(v.IntervalSeconds), int64(v.NextRunAt.Nanosecond())).UTC()
	if fire.After(now) {
		missed--
		fire = time.Unix(fire.Unix()-int64(v.IntervalSeconds), int64(fire.Nanosecond())).UTC()
	}
	return fire, fire.Add(interval), v.MissedPolicy != "skip" || missed == 0
}
func (s *Server) registerGovernanceSchedules(api, write *gin.RouterGroup) {
	api.GET("/governance/schedules/preview", s.require("governance", "governor"), s.governanceSchedulePreview)
	api.GET("/governance/schedules/:id/triggers", s.require("governance", "governor"), func(c *gin.Context) {
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		var schedule GovernanceSchedule
		if e := tx.Where("id=?", c.Param("id")).Take(&schedule).Error; e != nil {
			s.fail(c, e)
			return
		}
		if e := governanceWorkspaceMatch(c.Request.Context(), schedule.WorkspaceID); e != nil {
			s.fail(c, e)
			return
		}
		if !admin(p) && (schedule.ActorID != p.UserID || schedule.DepartmentID != p.DepartmentID) {
			s.fail(c, denied)
			return
		}
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 20, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		var total int64
		if e = tx.Table("governance_schedule_triggers").Where("schedule_id=?", schedule.ID).Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []struct {
			ScheduledAt time.Time `json:"scheduled_at"`
			RunID       *string   `json:"run_id"`
			Code        string    `json:"code"`
		}{}
		if e = tx.Table("governance_schedule_triggers").Where("schedule_id=?", schedule.ID).Order("scheduled_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	api.GET("/governance/schedules", s.require("governance", "governor"), func(c *gin.Context) {
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 20, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		p := principal(c)
		q := s.db.WithContext(c.Request.Context()).Model(&GovernanceSchedule{})
		var scopeErr error
		q, scopeErr = s.governanceWorkspaceFilter(c, q, "workspace_id", true)
		if scopeErr != nil {
			s.fail(c, scopeErr)
			return
		}
		if !admin(p) {
			q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
		}
		if target := c.Query("target_id"); target != "" {
			q = q.Where("workflow_id=?", target)
		}
		if kind := c.Query("target_kind"); kind != "" {
			if kind != "dataflow" && kind != "workflow" {
				s.fail(c, invalid)
				return
			}
			q = q.Where("target_kind=?", kind)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []GovernanceSchedule{}
		if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		for i := range rows {
			rows[i].setTargetReference()
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	write.POST("/governance/schedules", s.require("governance", "governor"), s.mutate("governance_create_schedule", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[governanceScheduleInput](b)
		if e != nil {
			return 0, nil, "", e
		}
		row := GovernanceSchedule{Parameters: encode(map[string]json.RawMessage{}), Dataflow: v.Dataflow, Workflow: v.Workflow, WorkflowID: v.Workflow.ID, WorkflowVersion: v.Workflow.Version, Timezone: v.Timezone, IntervalSeconds: v.IntervalSeconds, Cron: v.Cron, ConcurrencyPolicy: v.ConcurrencyPolicy, MissedPolicy: v.MissedPolicy, NextRunAt: v.NextRunAt.UTC(), ActorID: p.UserID, State: "active", Version: 1}
		if e = validateGovernanceSchedule(row); e != nil {
			return 0, nil, "", e
		}
		kind, ref, _ := governanceScheduleTarget(row)
		row.TargetKind, row.WorkflowID, row.WorkflowVersion = kind, ref.ID, ref.Version
		if row.Cron != "" {
			row.NextRunAt, e = governanceCronTime(row.Cron, row.Timezone, row.NextRunAt.Add(-time.Nanosecond), true)
			if e != nil {
				return 0, nil, "", e
			}
		}
		head, version, e := governanceRead(tx, p, kind, ref.ID, ref.Version)
		if e != nil {
			return 0, nil, "", e
		}
		if version.State != "published" {
			return 0, nil, "", conflict
		}
		def, e := decode[GovernanceDefinition](version.Definition)
		if e != nil {
			return 0, nil, "", e
		}
		if e = governanceValidateReferences(tx, p, head.DepartmentID, workspaceID(head.WorkspaceID), def); e != nil {
			return 0, nil, "", e
		}
		parameters, e := governanceScheduleParameters(tx, p, kind, version, def, v.Parameters)
		if e != nil {
			return 0, nil, "", e
		}

		space, scopeErr := governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID))
		if scopeErr != nil {
			return 0, nil, "", scopeErr
		}
		row.WorkspaceID = space.ID
		row.Parameters = encode(parameters)
		row.DepartmentID = head.DepartmentID
		if e = governanceLock(tx, head.DepartmentID); e != nil {
			return 0, nil, "", e
		}
		id, e := newID()
		if e != nil {
			return 0, nil, "", e
		}
		row.ID = id
		if e = tx.Create(&row).Error; e != nil {
			return 0, nil, id, e
		}
		return 201, encode(row), id, nil
	}))
	write.POST("/governance/schedules/:id/update", s.require("governance", "governor"), s.mutatePath("governance_update_schedule", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[governanceScheduleInput](b)
		if e != nil {
			return 0, nil, id, e
		}
		var row GovernanceSchedule
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; e != nil {
			return 0, nil, id, e
		}
		if !admin(p) && (row.ActorID != p.UserID || row.DepartmentID != p.DepartmentID) {
			return 0, nil, id, denied
		}
		if _, e = governanceWorkspaceWrite(tx, p, row.WorkspaceID); e != nil {
			return 0, nil, id, e
		}
		if v.BaseVersion != row.Version {
			return 0, nil, id, conflict
		}
		proposed := GovernanceSchedule{Workflow: v.Workflow, Dataflow: v.Dataflow, Timezone: v.Timezone, IntervalSeconds: v.IntervalSeconds, Cron: v.Cron, ConcurrencyPolicy: v.ConcurrencyPolicy, MissedPolicy: v.MissedPolicy, NextRunAt: v.NextRunAt.UTC()}
		if e = validateGovernanceSchedule(proposed); e != nil {
			return 0, nil, id, e
		}
		kind, ref, _ := governanceScheduleTarget(proposed)
		row.setTargetReference()
		if kind != row.TargetKind || ref.ID != row.WorkflowID {
			return 0, nil, id, invalid
		}
		// Serialize publication/retirement with the target check and this update.
		if e = governanceLock(tx, row.DepartmentID); e != nil {
			return 0, nil, id, e
		}
		head, version, e := governanceRead(tx, p, kind, ref.ID, ref.Version)
		if e != nil {
			return 0, nil, id, e
		}
		if workspaceID(head.WorkspaceID) != row.WorkspaceID || head.DepartmentID != row.DepartmentID {
			return 0, nil, id, denied
		}
		if version.State != "published" {
			return 0, nil, id, conflict
		}
		def, e := decode[GovernanceDefinition](version.Definition)
		if e != nil {
			return 0, nil, id, e
		}
		if e = governanceValidateReferences(tx, p, head.DepartmentID, row.WorkspaceID, def); e != nil {
			return 0, nil, id, e
		}
		parameters, e := governanceScheduleParameters(tx, p, kind, version, def, v.Parameters)
		if e != nil {
			return 0, nil, id, e
		}
		actor, e := s.etlPrincipal(tx.Statement.Context, row.ActorID)
		if e != nil {
			return 0, nil, id, denied
		}
		if _, e = governanceWorkspaceWrite(tx, actor, row.WorkspaceID); e != nil {
			return 0, nil, id, e
		}
		if e = s.governanceScheduleAuthorizeSources(tx, actor, kind, def, parameters, nil); e != nil {
			return 0, nil, id, e
		}
		if proposed.Cron != "" {
			proposed.NextRunAt, e = governanceCronTime(proposed.Cron, proposed.Timezone, proposed.NextRunAt.Add(-time.Nanosecond), true)
			if e != nil {
				return 0, nil, id, e
			}
		}
		row.WorkflowVersion, row.Timezone, row.IntervalSeconds, row.Cron = ref.Version, proposed.Timezone, proposed.IntervalSeconds, proposed.Cron
		row.MissedPolicy, row.NextRunAt, row.Parameters = proposed.MissedPolicy, proposed.NextRunAt, encode(parameters)
		row.Version++
		row.setTargetReference()
		if e = tx.Save(&row).Error; e != nil {
			return 0, nil, id, e
		}
		return 200, encode(row), id, nil
	}))
	for _, state := range []string{"pause", "resume"} {
		action := state
		write.POST("/governance/schedules/:id/"+action, s.require("governance", "governor"), s.mutatePath("governance_schedule_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			v, e := bind[struct {
				BaseVersion int `json:"base_version"`
			}](b)
			if e != nil {
				return 0, nil, id, e
			}
			var row GovernanceSchedule
			if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; e != nil {
				return 0, nil, id, e
			}
			if !admin(p) && (row.ActorID != p.UserID || row.DepartmentID != p.DepartmentID) {
				return 0, nil, id, denied
			}
			if _, e = governanceWorkspaceWrite(tx, p, row.WorkspaceID); e != nil {
				return 0, nil, id, e
			}
			if v.BaseVersion != row.Version {
				return 0, nil, id, conflict
			}
			row.setTargetReference()
			target := "paused"
			if action == "resume" {
				target = "active"
				_, version, e := governanceRead(tx, p, row.TargetKind, row.WorkflowID, row.WorkflowVersion)
				if e != nil {
					return 0, nil, id, e
				}
				if version.State != "published" {
					return 0, nil, id, conflict
				}
				def, e := decode[GovernanceDefinition](version.Definition)
				if e != nil {
					return 0, nil, id, e
				}
				values, e := decode[map[string]json.RawMessage](row.Parameters)
				if e != nil {
					return 0, nil, id, e
				}
				if _, e = governanceScheduleParameters(tx, p, row.TargetKind, version, def, values); e != nil {
					return 0, nil, id, e
				}
				if e = governanceValidateReferences(tx, p, row.DepartmentID, row.WorkspaceID, def); e != nil {
					return 0, nil, id, e
				}

			}
			if row.State == target {
				return 0, nil, id, conflict
			}
			row.State = target
			row.Version++
			if e = tx.Save(&row).Error; e != nil {
				return 0, nil, id, e
			}
			return 200, encode(row), id, nil
		}))
	}
}
func (s *Server) governanceScheduleTick(tx *gorm.DB, now time.Time) error {
	if e := tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return e
	}
	rows := []GovernanceSchedule{}
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='active' AND next_run_at<=?", now).Order("next_run_at,id").Limit(25).Find(&rows).Error; e != nil {
		return e
	}
	for _, row := range rows {
		row.setTargetReference()
		fire, next, due := governanceScheduleDue(row, now)
		code := "SCHEDULE_MISSED_SKIPPED"
		var runID *string
		var count int64
		if e := tx.Table("governance_schedule_triggers").Where("schedule_id=? AND scheduled_at=?", row.ID, fire).Count(&count).Error; e != nil {
			return e
		}
		if count > 0 {
			row.NextRunAt = next
			if e := tx.Save(&row).Error; e != nil {
				return e
			}
			continue
		}
		if due {
			var active int64
			refKey := "workflow"
			if row.TargetKind == "dataflow" {
				refKey = "flow"
			}
			if e := tx.Model(&GovernanceRun{}).Where("kind=? AND actor_id=? AND definition->?->>'id'=? AND state IN ('queued','running')", row.TargetKind, row.ActorID, refKey, row.WorkflowID).Count(&active).Error; e != nil {
				return e
			}
			code = "SCHEDULE_CONCURRENCY_SKIPPED"
			if active == 0 {
				p, authErr := s.etlPrincipal(tx.Statement.Context, row.ActorID)
				code = "SCHEDULE_AUTHORIZATION_DENIED"
				if authErr == nil {
					e := tx.Transaction(func(nested *gorm.DB) error {
						parameters, err := decode[map[string]json.RawMessage](row.Parameters)
						if err != nil {
							return err
						}
						var id string
						if row.TargetKind == "dataflow" {
							_, _, id, err = s.enqueueGovernanceFlow(workspaceContext(nested, row.WorkspaceID, true), p, *row.Dataflow, encode(governanceFlowRunInput{Parameters: parameters}))
						} else {
							_, _, id, err = s.enqueueGovernanceWorkflowParameters(workspaceContext(nested, row.WorkspaceID, true), p, row.Workflow, nil, parameters)
						}
						if err == nil {
							runID = &id
						}
						return err
					})
					if e == nil {
						code = "SCHEDULE_TRIGGERED"
					} else {
						var pe *Problem
						if errors.As(e, &pe) {
							code = pe.Code
						} else if errors.Is(e, gorm.ErrRecordNotFound) {
							code = "GOVERNANCE_REFERENCE_UNAVAILABLE"
						} else {
							return e
						}
					}
				}
			}
		}
		if e := tx.Exec("INSERT INTO governance_schedule_triggers(schedule_id,scheduled_at,run_id,code) VALUES(?,?,?,?)", row.ID, fire, runID, code).Error; e != nil {
			return e
		}
		row.NextRunAt = next
		row.LastErrorCode = ""
		if code != "SCHEDULE_TRIGGERED" {
			row.LastErrorCode = code
		}
		if runID != nil {
			row.LastRunID = runID
		}
		if e := tx.Save(&row).Error; e != nil {
			return e
		}
	}
	return nil
}
func governanceCanRetire(tx *gorm.DB, kind, id string, version int) error {
	if kind == "model" {
		var pending int64
		if e := tx.Model(&GovernanceModelDeployment{}).Where("model_id=? AND model_version=? AND state IN ('queued','running','unknown')", id, version).Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return problem(409, "GOVERNANCE_MODEL_DEPLOYMENT_IN_USE", "请先完成或核对该物理模型的建表任务")
		}
		if e := tx.Model(&Record{}).Where("kind='file_resource' AND state NOT IN ('disabled','archived') AND body->'config'->'governance_input'->'model'->>'id'=? AND (body->'config'->'governance_input'->'model'->>'version')::integer=?", id, version).Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return problem(409, "GOVERNANCE_FILE_MODEL_IN_USE", "结构化文件契约仍在引用该模型版本")
		}
	}
	if kind == "workflow" || kind == "dataflow" {
		var schedules int64
		if e := tx.Model(&GovernanceSchedule{}).Where("target_kind=? AND workflow_id=? AND workflow_version=? AND state='active'", kind, id, version).Count(&schedules).Error; e != nil {
			return e
		}
		if schedules > 0 {
			return problem(409, "GOVERNANCE_SCHEDULE_IN_USE", "请先暂停引用此版本的调度")
		}
	}
	var active int64
	q := tx.Model(&GovernanceRun{}).Where("state IN ('queued','running')")
	if kind == "workflow" {
		q = q.Where("definition->'workflow'->>'id'=? AND (definition->'workflow'->>'version')::integer=?", id, version)
	} else {
		q = q.Where("definition->'pinned' @> ?::jsonb", string(encode([]gin.H{{"ref": GovernanceRef{ID: id, Version: version}}})))
	}
	if e := q.Count(&active).Error; e != nil {
		return e
	}
	if active > 0 {
		return problem(409, "GOVERNANCE_RUN_IN_USE", "运行仍在使用此版本，请等待结束或取消后再停用")
	}
	return nil
}

// Storage retains the legacy FK column names; the public target is unambiguous.
func (v *GovernanceSchedule) setTargetReference() {
	ref := GovernanceRef{ID: v.WorkflowID, Version: v.WorkflowVersion}
	if v.TargetKind == "dataflow" {
		v.Dataflow = &ref
		v.Workflow = GovernanceRef{}
	} else {
		v.TargetKind = "workflow"
		v.Workflow = ref
		v.Dataflow = nil
	}
}
func governanceScheduleTarget(v GovernanceSchedule) (string, GovernanceRef, error) {
	if v.Dataflow != nil {
		if v.Workflow.ID != "" || v.Workflow.Version != 0 || !validGovernanceRef(v.Dataflow) {
			return "", GovernanceRef{}, invalid
		}
		return "dataflow", *v.Dataflow, nil
	}
	if !validGovernanceRef(&v.Workflow) {
		return "", GovernanceRef{}, invalid
	}
	return "workflow", v.Workflow, nil
}
func governanceScheduleParameters(tx *gorm.DB, p identity.Principal, kind string, version GovernanceVersion, def GovernanceDefinition, values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if kind == "workflow" {
		if def.Workflow == nil {
			return nil, invalid
		}
		return governanceWorkflowParameters(tx, p, *def.Workflow, values)
	}
	if def.Dataflow == nil {
		return nil, invalid
	}
	if e := requireGovernanceFlowMode(version.ExecutionMode, "batch"); e != nil {
		return nil, e
	}
	_, e := governanceMaterializeFlow(*def.Dataflow, values, nil)
	if e != nil {
		return nil, e
	}
	defaults, e := governanceFlowParameters(*def.Dataflow)
	if e != nil {
		return nil, e
	}
	for key, value := range values {
		defaults[key] = value
	}
	return defaults, nil
}

// Recheck the inputs that the next scheduled run will actually read. Workflow
// artifact bindings replace their placeholder source and are checked at dispatch.
func (s *Server) governanceScheduleAuthorizeSources(tx *gorm.DB, p identity.Principal, kind string, def GovernanceDefinition, values map[string]json.RawMessage, bindings map[string]string) error {
	if kind == "workflow" {
		if def.Workflow == nil {
			return invalid
		}
		for _, task := range def.Workflow.Tasks {
			if governanceWorkflowTaskKind(task) != "dataflow" {
				continue
			}
			head, version, e := governanceRead(tx, p, "dataflow", task.Dataflow.ID, task.Dataflow.Version)
			if e != nil {
				return e
			}
			if version.State != "published" {
				return conflict
			}
			child, e := decode[GovernanceDefinition](version.Definition)
			if e != nil {
				return e
			}
			if e = governanceValidateReferences(tx, p, head.DepartmentID, workspaceID(head.WorkspaceID), child); e != nil {
				return e
			}
			if child.Dataflow == nil {
				return invalid
			}
			defaults, e := governanceFlowParameters(*child.Dataflow)
			if e != nil {
				return e
			}
			for key := range defaults {
				if value, ok := values[key]; ok {
					defaults[key] = value
				}
			}
			if e = s.governanceScheduleAuthorizeSources(tx, p, "dataflow", child, defaults, task.Inputs); e != nil {
				return e
			}
		}
		return nil
	}
	if def.Dataflow == nil {
		return invalid
	}
	graph, e := governanceMaterializeFlow(*def.Dataflow, values, nil)
	if e != nil {
		return e
	}
	for _, node := range graph.Nodes {
		if node.Source == nil {
			continue
		}
		if _, bound := bindings[node.ID]; bound {
			continue
		}
		if _, e = s.governanceInputBasisParameters(tx, p, node.Source.Resource, node.Source.Fields, true, node.Source.Parameters); e != nil {
			return e
		}
	}
	return nil
}
