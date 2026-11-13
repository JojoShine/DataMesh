package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceRun struct {
	WorkspaceID     string          `json:"workspace_id"`
	ID              string          `json:"id" gorm:"primaryKey"`
	Kind            string          `json:"kind"`
	DepartmentID    string          `json:"department_id"`
	ActorID         string          `json:"actor_id"`
	Definition      json.RawMessage `json:"definition" gorm:"type:jsonb"`
	State           string          `json:"state"`
	RetryOf         *string         `json:"retry_of"`
	Processed       int64           `json:"processed"`
	Result          json.RawMessage `json:"result" gorm:"type:jsonb"`
	ErrorCode       string          `json:"error_code"`
	CancelRequested bool            `json:"cancel_requested"`
	LeaseOwner      string          `json:"-"`
	LeaseExpiresAt  time.Time       `json:"-"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	StartedAt       *time.Time      `json:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at"`
}

func (GovernanceRun) TableName() string { return "governance_runs" }

type GovernanceRunEvent struct {
	ErrorLine   int       `json:"error_line"`
	ErrorColumn int       `json:"error_column"`
	ErrorField  string    `json:"error_field"`
	InputCount  int64     `json:"input_count"`
	OutputCount int64     `json:"output_count"`
	DurationMS  int64     `json:"duration_ms"`
	ID          int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	RunID       string    `json:"run_id"`
	NodeID      string    `json:"node_id"`
	Code        string    `json:"code"`
	Processed   int64     `json:"processed"`
	CreatedAt   time.Time `json:"created_at"`
}

func (GovernanceRunEvent) TableName() string { return "governance_run_events" }
func governanceRunRead(tx *gorm.DB, p identity.Principal, id string, lock bool) (GovernanceRun, error) {
	var run GovernanceRun
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.Take(&run).Error
	lightweight := err == nil && lightweightRunMarker(run)
	if lightweight && p.UserID != "" {
		def, e := decode[governanceFlowExecution](run.Definition)
		if e != nil || !lightweightScopeMatches(p, def.Lightweight.VisitorScope) {
			err = denied
		}
	}
	if lightweight && (workspaceScope(tx.Statement.Context).ID != "" || (p.UserID != "" && (run.ActorID != p.UserID || run.DepartmentID != p.DepartmentID))) {
		err = denied
	}
	if err == nil && !lightweight {
		err = governanceWorkspaceMatch(tx.Statement.Context, run.WorkspaceID)
	}
	if err == nil && !lightweight && workspaceScope(tx.Statement.Context).Mutation {
		_, err = governanceWorkspaceWrite(tx, p, run.WorkspaceID)
	}
	if err == nil && !admin(p) && (run.DepartmentID != p.DepartmentID || run.ActorID != p.UserID) {
		err = denied
	}
	return run, err
}
func governanceRunEvent(tx *gorm.DB, run GovernanceRun, code string) error {
	return tx.Create(&GovernanceRunEvent{RunID: run.ID, Code: code, Processed: run.Processed}).Error
}
func (s *Server) registerGovernanceRuns(api, write *gin.RouterGroup) {
	api.GET("/governance/runs", s.require("governance", "governor"), s.governanceRuns)
	api.GET("/governance/runs/:id", s.require("governance", "governor"), s.governanceRunDetail)
	api.GET("/governance/runs/:id/events", s.require("governance", "governor"), s.governanceRunEvents)
	write.POST("/governance/runs/:id/cancel", s.require("governance", "governor"), s.mutatePath("governance_cancel_run", s.governanceCancelRun))
	write.POST("/governance/runs/:id/retry", s.require("governance", "governor"), s.mutatePath("governance_retry_run", s.governanceRetryRun))
	write.POST("/governance/profiles", s.require("governance", "governor"), s.mutate("governance_create_profile", s.governanceCreateProfile))
	s.registerGovernanceDataflows(api, write)
}
func (s *Server) governanceRuns(c *gin.Context) {
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	page, err := intQuery(c, "page", 1, 100000)
	if err != nil {
		s.fail(c, err)
		return
	}
	size, err := intQuery(c, "page_size", 20, 100)
	if err != nil {
		s.fail(c, err)
		return
	}
	q := tx.Model(&GovernanceRun{})
	scope := sandboxVisitorScope(p)
	if p.FullExperience && scope == "" {
		q = q.Where("definition->'lightweight' IS NULL")
	} else {
		q = q.Where("definition->'lightweight' IS NULL OR (actor_id=? AND department_id=? AND COALESCE(definition->'lightweight'->>'visitor_scope','')=?)", p.UserID, p.DepartmentID, scope)
	}
	if kind := c.Query("kind"); kind != "" {
		if !slices.Contains([]string{"profile", "dataflow", "workflow", "dataflow_preview", "master"}, kind) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("kind=?", kind)
	}
	if state := c.Query("state"); state != "" {
		if !slices.Contains([]string{"queued", "running", "succeeded", "failed", "cancelled", "interrupted"}, state) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("state=?", state)
	}
	if id := c.Query("definition_id"); id != "" {
		if !valid(id, 128) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("definition->'flow'->>'id'=? OR definition->'workflow'->>'id'=?", id, id)
	}
	if !admin(p) {
		q = q.Where("department_id=? AND actor_id=?", p.DepartmentID, p.UserID)
	}
	if id := c.Query("resource_id"); id != "" {
		q = q.Where("definition->'input'->'resource'->>'resource_id'=?", id)
	}
	q, err = s.governanceWorkspaceFilter(c, q, "workspace_id", true)
	if err != nil {
		s.fail(c, err)
		return
	}
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	rows := []GovernanceRun{}
	if err = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		s.fail(c, err)
		return
	}
	// Reports require a fresh input authorization check through the detail route.
	for i := range rows {
		rows[i].Result = json.RawMessage("null")
	}
	c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}
func (s *Server) governanceRunDetail(c *gin.Context) {
	p := principal(c)
	run, err := governanceRunRead(s.db.WithContext(c.Request.Context()), p, c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	if run.State == "succeeded" && run.Kind == "master" {
		if _, _, e := s.masterRunAccess(s.db.WithContext(c.Request.Context()), p, run.ID); e != nil {
			s.fail(c, e)
			return
		}
	}
	if run.State == "succeeded" && run.Kind == "profile" {
		def, e := decode[governanceProfileDefinition](run.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		actor, e := s.etlPrincipal(c.Request.Context(), run.ActorID)
		if e != nil {
			s.fail(c, denied)
			return
		}
		if e = s.governanceCheckInput(c.Request.Context(), actor, def.Input, false); e != nil {
			s.fail(c, e)
			return
		}
	}
	if run.State == "succeeded" && (run.Kind == "dataflow" || run.Kind == "dataflow_preview") {
		actor, e := s.etlPrincipal(c.Request.Context(), run.ActorID)
		if e != nil {
			s.fail(c, denied)
			return
		}
		def, e := decode[governanceFlowExecution](run.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		if e = s.governanceFlowAuthorize(c.Request.Context(), actor, def); e != nil {
			s.fail(c, e)
			return
		}
	}
	c.JSON(200, run)
}
func (s *Server) governanceRunEvents(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	run, err := governanceRunRead(tx, principal(c), c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	page, err := intQuery(c, "page", 1, 100000)
	if err != nil {
		s.fail(c, err)
		return
	}
	size, err := intQuery(c, "page_size", 50, 100)
	if err != nil {
		s.fail(c, err)
		return
	}
	q := tx.Model(&GovernanceRunEvent{}).Where("run_id=?", run.ID)
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	events := []GovernanceRunEvent{}
	if err = q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&events).Error; err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": events, "total": total, "page": page, "page_size": size})
}
func (s *Server) governanceCancelRun(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, err := bind[struct{}](b); err != nil {
		return 0, nil, id, err
	}
	run, err := governanceRunRead(tx, p, id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if run.State != "queued" && run.State != "running" {
		return 0, nil, id, conflict
	}
	run.CancelRequested = true
	if run.State == "queued" {
		now := time.Now().UTC()
		run.State = "cancelled"
		run.FinishedAt = &now
		run.ErrorCode = "GOVERNANCE_CANCELLED"
	}
	if run.Kind == "workflow" && run.State == "cancelled" {
		if err = tx.Model(&GovernanceWorkflowTaskRun{}).Where("parent_run_id=? AND state='pending'", run.ID).Update("state", "cancelled").Error; err != nil {
			return 0, nil, id, err
		}
	}
	if err = tx.Save(&run).Error; err != nil {
		return 0, nil, id, err
	}
	if err = governanceRunEvent(tx, run, "CANCEL_REQUESTED"); err != nil {
		return 0, nil, id, err
	}
	return 200, encode(run), id, nil
}
func (s *Server) governanceRetryRun(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, err := bind[struct{}](b); err != nil {
		return 0, nil, id, err
	}
	run, err := governanceRunRead(tx, p, id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if lightweightRunMarker(run) {
		return 0, nil, id, problem(409, "LIGHTWEIGHT_RERUN_REQUIRED", "请从轻量治理单元发起新运行")
	}
	tx = workspaceContext(tx, run.WorkspaceID, true)
	if run.State != "failed" && run.State != "interrupted" && run.State != "cancelled" {
		return 0, nil, id, conflict
	}
	if run.Kind == "master" {
		def, e := decode[governanceMasterExecution](run.Definition)
		if e != nil {
			return 0, nil, id, e
		}
		return s.enqueueMaster(tx, p, def.Master)
	}
	if run.Kind == "workflow" {
		return s.retryGovernanceWorkflow(tx, p, run)
	}
	if run.Kind == "dataflow_preview" {
		def, e := decode[governanceFlowExecution](run.Definition)
		if e != nil || def.Preview == nil {
			return 0, nil, id, invalid
		}
		return s.enqueueGovernanceCodePreview(tx, p, governanceCodePreviewInput{Definition: def.Pinned[0].Definition, NodeID: def.Preview.NodeID, SampleLimit: 20}, &run.ID)
	}
	if run.Kind == "dataflow" {
		def, err := decode[governanceFlowExecution](run.Definition)
		if err != nil {
			return 0, nil, id, err
		}
		if def.Trial != nil || def.WorkflowTrial != nil {
			return 0, nil, id, problem(409, "GOVERNANCE_RELEASE_TRIAL_REQUIRED", "请从变更申请发起候选版本的试运行")
		}
		status, _, nextID, err := s.enqueueGovernanceFlow(tx, p, def.Flow, encode(governanceFlowRunInput{Parameters: def.Parameters}))
		if err != nil {
			return 0, nil, id, err
		}
		var next GovernanceRun
		if err = tx.Where("id=?", nextID).Take(&next).Error; err != nil {
			return 0, nil, id, err
		}
		next.RetryOf = &run.ID
		if err = tx.Save(&next).Error; err != nil {
			return 0, nil, id, err
		}
		return status, encode(next), nextID, nil
	}
	if run.Kind != "profile" {
		return 0, nil, id, conflict
	}
	def, err := decode[governanceProfileDefinition](run.Definition)
	if err != nil {
		return 0, nil, id, err
	}
	return s.enqueueGovernanceProfile(tx, p, governanceProfileInput{Resource: def.Input.Resource, Fields: fieldNames(def.Input.Fields), Mode: def.Mode, SampleRows: def.SampleRows}, &run.ID)
}
func (s *Server) governanceClaim(ctx context.Context) (GovernanceRun, bool, error) {
	var run GovernanceRun
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
			return e
		}
		// A lost read snapshot must never be presented as the same resumed observation.
		expired := []GovernanceRun{}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("kind<>'workflow' AND state='running' AND lease_expires_at<CURRENT_TIMESTAMP").Find(&expired).Error; e != nil {
			return e
		}
		now := time.Now().UTC()
		for _, r := range expired {
			r.State = "interrupted"
			r.ErrorCode = "GOVERNANCE_SNAPSHOT_LOST"
			r.FinishedAt = &now
			r.Result = json.RawMessage("null")
			if e := tx.Save(&r).Error; e != nil {
				return e
			}
			if e := governanceRunEvent(tx, r, "GOVERNANCE_SNAPSHOT_LOST"); e != nil {
				return e
			}
		}
		var active int64
		if e := tx.Model(&GovernanceRun{}).Where("kind<>'workflow' AND state='running'").Count(&active).Error; e != nil {
			return e
		}
		if active >= 2 {
			return nil
		}
		queued := []GovernanceRun{}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("kind<>'workflow' AND state='queued'").Where("(EXISTS(SELECT 1 FROM governance_workspaces w WHERE w.id=governance_runs.workspace_id AND w.state='active' AND (SELECT count(*) FROM governance_runs active WHERE active.workspace_id=w.id AND active.kind<>'workflow' AND active.state='running')<w.max_concurrent_runs) OR (workspace_id IS NULL AND kind='dataflow' AND jsonb_typeof(definition->'lightweight')='object' AND length(definition->'lightweight'->>'unit_id')>0 AND definition->'lightweight'->>'version' ~ '^[1-9][0-9]*$' AND definition->'flow'->>'id'=definition->'lightweight'->>'unit_id' AND definition->'flow'->>'version'=definition->'lightweight'->>'version'))").Order("created_at,id").Limit(1).Find(&queued).Error; e != nil {
			return e
		}
		if len(queued) == 0 {
			return nil
		}
		run = queued[0]
		token, e := newID()
		if e != nil {
			return e
		}
		run.State = "running"
		run.LeaseOwner = token
		run.LeaseExpiresAt = now.Add(30 * time.Second)
		run.StartedAt = &now
		if e = tx.Save(&run).Error; e != nil {
			return e
		}
		code := "PROFILE_STARTED"
		if run.Kind == "master" {
			code = "MASTER_STARTED"
		}
		if run.Kind == "dataflow" || run.Kind == "dataflow_preview" {
			code = "FLOW_STARTED"
		}
		if e = governanceRunEvent(tx, run, code); e != nil {
			return e
		}
		claimed = true
		return nil
	})
	return run, claimed, err
}
func (s *Server) StartGovernanceWorkers(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := s.exportTick(ctx); e != nil && ctx.Err() == nil {
					slog.Warn("governance export reconciliation pending")
				}
			}
		}
	}()
	wg.Add(1)
	go func() { defer wg.Done(); s.runStreamWorker(ctx) }()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				run, ok, e := s.governanceClaim(ctx)
				if e != nil && ctx.Err() == nil {
					slog.Error("governance queue unavailable")
				}
				if !ok {
					continue
				}
				func() {
					defer func() {
						if recover() != nil {
							slog.Error("governance executor panic", "run_id", run.ID)
						}
					}()
					s.executeGovernanceRun(ctx, run)
				}()
			}
		}()
	}
	return func() { cancel(); wg.Wait() }
}
func (s *Server) executeGovernanceRun(parent context.Context, run GovernanceRun) {
	ctx, cancel := context.WithTimeout(parent, min(s.etl.Timeout, 10*time.Minute))
	defer cancel()
	p, err := s.etlPrincipal(ctx, run.ActorID)
	def, decodeErr := decode[governanceProfileDefinition](run.Definition)
	var masterDef governanceMasterExecution
	if run.Kind == "master" {
		masterDef, decodeErr = decode[governanceMasterExecution](run.Definition)
	}
	var flowDef governanceFlowExecution
	if run.Kind == "dataflow" || run.Kind == "dataflow_preview" {
		flowDef, decodeErr = decode[governanceFlowExecution](run.Definition)
	}
	authorize := func(checkCtx context.Context) error {
		if run.Kind == "master" {
			return s.authorizeMaster(checkCtx, p, masterDef)
		}
		if run.Kind == "dataflow" || run.Kind == "dataflow_preview" {
			return s.governanceFlowAuthorize(checkCtx, p, flowDef)
		}
		return s.governanceCheckInput(checkCtx, p, def.Input, false)
	}
	if err == nil {
		err = decodeErr
	}
	guard := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		var current GovernanceRun
		if e := s.db.WithContext(ctx).Where("id=? AND lease_owner=? AND state='running' AND lease_expires_at>CURRENT_TIMESTAMP", run.ID, run.LeaseOwner).Take(&current).Error; e != nil {
			return problem(409, "GOVERNANCE_LEASE_LOST", "运行租约已失效")
		}
		if current.CancelRequested {
			return problem(409, "GOVERNANCE_CANCELLED", "运行已取消")
		}
		return authorize(ctx)
	}
	done := make(chan struct{})
	monitorError := make(chan error, 1)
	var monitor sync.WaitGroup
	monitor.Add(1)
	go func() {
		defer monitor.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if e := guard(); e != nil {
					monitorError <- e
					cancel()
					return
				}
				result := s.db.WithContext(ctx).Model(&GovernanceRun{}).Where("id=? AND lease_owner=? AND state='running' AND lease_expires_at>CURRENT_TIMESTAMP AND cancel_requested=false", run.ID, run.LeaseOwner).Updates(map[string]interface{}{"lease_expires_at": time.Now().Add(30 * time.Second), "updated_at": time.Now()})
				if result.Error != nil || result.RowsAffected != 1 {
					monitorError <- problem(409, "GOVERNANCE_LEASE_LOST", "运行租约已失效")
					cancel()
					return
				}
			}
		}
	}()
	var report json.RawMessage
	if err == nil {
		err = guard()
	}
	if err == nil {
		if run.Kind == "dataflow" || run.Kind == "dataflow_preview" {
			report, run.Processed, err = s.executeGovernanceFlow(ctx, p, run, flowDef, guard)
		} else if run.Kind == "master" {
			report, run.Processed, err = s.executeMaster(ctx, p, run, masterDef, guard)
		} else {
			report, run.Processed, err = s.executeGovernanceProfile(ctx, p, run, def, guard)
		}
	}
	if err == nil && (run.Kind == "profile" || run.Kind == "master") {
		err = guard()
	}
	close(done)
	monitor.Wait()
	if (run.Kind == "dataflow" || run.Kind == "dataflow_preview") && err == nil {
		return
	}
	select {
	case monitorErr := <-monitorError:
		err = monitorErr
	default:
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	finishErr := s.db.WithContext(finishCtx).Transaction(func(tx *gorm.DB) error {
		current, e := governanceRunRead(tx, identity.Principal{Roles: []string{"admin"}}, run.ID, true)
		if e != nil {
			return e
		}
		if current.State != "running" || current.LeaseOwner != run.LeaseOwner || !current.LeaseExpiresAt.After(time.Now()) {
			return nil
		}
		if err == nil {
			err = authorize(finishCtx)
		}
		current.State = "succeeded"
		current.Result = report
		code := "PROFILE_COMPLETED"
		if run.Kind == "master" {
			code = "MASTER_COMPLETED"
		}
		if err != nil {
			current.State = "failed"
			current.Result = json.RawMessage("null")
			current.ErrorCode = "GOVERNANCE_EXECUTION_FAILED"
			var databaseError *pgconn.PgError
			if errors.As(err, &databaseError) {
				slog.Error("governance database execution failed", "run_id", run.ID, "sqlstate", databaseError.Code, "position", databaseError.Position)
			}
			var dorisError *mysql.MySQLError
			if errors.As(err, &dorisError) {
				// Driver messages can contain source values or generated SQL.
				slog.Error("governance Doris execution failed", "run_id", run.ID, "code", dorisError.Number, "sqlstate", string(dorisError.SQLState[:]))
			}
			var pe *Problem
			if errors.As(err, &pe) {
				current.ErrorCode = pe.Code
			}
			if errors.Is(err, context.DeadlineExceeded) {
				current.ErrorCode = "GOVERNANCE_TIMEOUT"
			}
			if parent.Err() != nil {
				current.State = "interrupted"
				current.ErrorCode = "GOVERNANCE_SNAPSHOT_LOST"
			}
			code = current.ErrorCode
		}
		if current.CancelRequested {
			current.State = "cancelled"
			current.Result = json.RawMessage("null")
			current.ErrorCode = "GOVERNANCE_CANCELLED"
			code = current.ErrorCode
		}
		now := time.Now().UTC()
		current.FinishedAt = &now
		current.Processed = run.Processed
		if e = tx.Save(&current).Error; e != nil {
			return e
		}
		var diagnostic *scriptExecutionError
		if errors.As(err, &diagnostic) {
			if e = tx.Create(&GovernanceRunEvent{RunID: run.ID, NodeID: diagnostic.NodeID, Code: code, Processed: run.Processed, ErrorLine: diagnostic.Line, ErrorColumn: diagnostic.Column, ErrorField: diagnostic.Field}).Error; e != nil {
				return e
			}
		}
		if e = governanceRunEvent(tx, current, code); e != nil {
			return e
		}
		return s.audit(tx, p, "governance_run_finished", run.ID, current.State, "")
	})
	if finishErr != nil {
		slog.Error("governance run finalization unavailable", "run_id", run.ID)
	} else if run.Kind == "profile" {
		if e := s.sweepGovernanceMonitors(finishCtx, run.ID); e != nil {
			slog.Error("governance monitor observation pending", "run_id", run.ID, "code", captureErrorCode(e))
		}
	}
}
