package workflow

import (
	"context"
	"encoding/json"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceStream struct {
	Freshness         *GovernanceStreamFreshness `json:"freshness,omitempty" gorm:"-"`
	ExecutionPlan     *GovernanceExecutionPlan   `json:"execution_plan,omitempty" gorm:"-"`
	ID                string                     `json:"id" gorm:"primaryKey"`
	WorkspaceID       string                     `json:"workspace_id"`
	DepartmentID      string                     `json:"department_id"`
	ActorID           string                     `json:"actor_id"`
	FlowID            string                     `json:"flow_id"`
	FlowVersion       int                        `json:"flow_version"`
	Definition        json.RawMessage            `json:"-" gorm:"type:jsonb"`
	Sources           json.RawMessage            `json:"-" gorm:"type:jsonb"`
	Positions         json.RawMessage            `json:"-" gorm:"type:jsonb"`
	State             string                     `json:"state"`
	DesiredState      string                     `json:"desired_state"`
	Generation        int                        `json:"generation"`
	JobID             string                     `json:"job_id,omitempty"`
	SessionID         string                     `json:"-"`
	OperationID       string                     `json:"-"`
	SubmissionStarted bool                       `json:"-"`
	StopTrigger       string                     `json:"-"`
	Savepoint         string                     `json:"-"`
	ErrorCode         string                     `json:"error_code,omitempty"`
	InputEvents       int64                      `json:"input_events"`
	OutputRows        int64                      `json:"output_rows"`
	RejectedRows      int64                      `json:"rejected_rows"`
	OutputOffset      int64                      `json:"output_offset"`
	CaughtUp          bool                       `json:"caught_up"`
	LeaseOwner        string                     `json:"-"`
	LeaseExpiresAt    time.Time                  `json:"-"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

func (GovernanceStream) TableName() string { return "governance_streams" }

type governanceStreamSource struct {
	SourceFingerprint string     `json:"source_fingerprint"`
	Binding           CDCBinding `json:"binding"`
	Fields            []Field    `json:"fields"`
}
type governanceStreamSnapshot struct {
	ID        string          `json:"id"`
	Offset    int64           `json:"offset"`
	Positions json.RawMessage `json:"positions"`
}

func streamTopic(id, node string) string { return "datamesh_gov_" + id + "_" + node }
func streamOutputTopic(id string) string { return streamTopic(id, "results") }

func governanceStreamRead(tx *gorm.DB, p identity.Principal, id string, lock bool) (GovernanceStream, error) {
	var row GovernanceStream
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.Take(&row).Error
	if err == nil {
		err = governanceWorkspaceMatch(tx.Statement.Context, row.WorkspaceID)
	}
	if err == nil && workspaceScope(tx.Statement.Context).Mutation {
		_, err = governanceWorkspaceWrite(tx, p, row.WorkspaceID)
	}
	if err == nil && !admin(p) && (row.ActorID != p.UserID || row.DepartmentID != p.DepartmentID) {
		err = denied
	}
	if err == nil {
		def, e := decode[governanceFlowExecution](row.Definition)
		if e != nil {
			return row, e
		}
		plan := governanceEnginePlan(def, "continuous")
		row.ExecutionPlan = &plan
	}
	return row, err
}
func (s *Server) registerGovernanceStreams(api, write *gin.RouterGroup) {
	api.GET("/governance/streams", s.require("governance", "governor"), s.governanceStreams)
	api.GET("/governance/streams/:id", s.require("governance", "governor"), func(c *gin.Context) {
		row, e := governanceStreamRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		row.Freshness = s.observeStreamFreshness(c.Request.Context(), row)
		c.JSON(200, row)
	})
	write.POST("/governance/dataflows/:id/versions/:version/stream", s.require("governance", "governor"), func(c *gin.Context) {
		version, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		ref := GovernanceRef{ID: c.Param("id"), Version: version}
		s.mutate("governance_start_stream", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
			return s.governanceStartStream(tx, p, ref, b)
		})(c)
	})
	for _, action := range []string{"pause", "resume", "reconcile", "snapshot", "stop"} {
		write.POST("/governance/streams/:id/"+action, s.require("governance", "governor"), s.mutatePath("governance_stream_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			return s.governanceStreamAction(tx, p, id, action, b)
		}))
	}
}
func (s *Server) governanceStreams(c *gin.Context) {
	p := principal(c)
	q := s.db.WithContext(c.Request.Context()).Model(&GovernanceStream{})
	if c.Query("version") != "" {
		version, e := intQuery(c, "version", 1, 1000000)
		if e != nil {
			s.fail(c, e)
			return
		}
		q = q.Where("flow_version=?", version)
	}
	if id := c.Query("definition_id"); id != "" {
		if !valid(id, 128) {
			s.fail(c, invalid)
			return
		}
		q = q.Where("flow_id=?", id)
	}
	if !admin(p) {
		q = q.Where("actor_id=? AND department_id=?", p.UserID, p.DepartmentID)
	}
	q, e := s.governanceWorkspaceFilter(c, q, "workspace_id", true)
	if e != nil {
		s.fail(c, e)
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
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []GovernanceStream{}
	if e = q.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; e != nil {
		s.fail(c, e)
		return
	}
	for i := range items {
		def, e := decode[governanceFlowExecution](items[i].Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		plan := governanceEnginePlan(def, "continuous")
		items[i].ExecutionPlan = &plan
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) governanceStartStream(tx *gorm.DB, p identity.Principal, ref GovernanceRef, b []byte) (int, json.RawMessage, string, error) {
	prepared, e := s.prepareGovernanceFlow(tx, p, ref, b, nil, "")
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if e = requireGovernanceFlowMode(prepared.Mode, "continuous"); e != nil {
		return 0, nil, ref.ID, e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if e = streamScriptConfigured(prepared.Execution); e != nil {
		return 0, nil, ref.ID, e
	}
	sources, e := s.governanceStreamSources(tx, p, prepared.Execution)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	topics := map[string]string{}
	for node := range sources {
		topics[node] = streamTopic(id, node)
	}
	if _, e = compileGovernanceFlink(prepared.Execution, topics, streamOutputTopic(id), streamJobName(id, 1)); e != nil {
		return 0, nil, ref.ID, e
	}
	compute, e := dorisOpen(tx.Statement.Context, true)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	compute.Close()
	var pending int64
	// A requested cancellation still occupies capacity until Flink confirms it.
	if e = tx.Model(&GovernanceStream{}).Where("state<>'failed'").Count(&pending).Error; e != nil {
		return 0, nil, ref.ID, e
	}
	if pending >= 2 {
		return 0, nil, ref.ID, problem(409, "STREAM_CAPACITY", "持续更新任务已达到当前环境的并发上限")
	}
	plan := governanceEnginePlan(prepared.Execution, "continuous")
	prepared.Execution.ExecutionPlan = &plan
	row := GovernanceStream{ID: id, WorkspaceID: prepared.WorkspaceID, DepartmentID: prepared.DepartmentID, ActorID: p.UserID, FlowID: ref.ID, FlowVersion: ref.Version, Definition: encode(prepared.Execution), Sources: encode(sources), Positions: encode(map[string]int64{}), State: "starting", DesiredState: "running", Generation: 1, LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if e = tx.Create(&row).Error; e != nil {
		return 0, nil, ref.ID, e
	}
	return 201, encode(row), id, nil
}
func (s *Server) governanceStreamAction(tx *gorm.DB, p identity.Principal, id, action string, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	row, e := governanceStreamRead(tx, p, id, true)
	if e != nil {
		return 0, nil, id, e
	}
	def, e := decode[governanceFlowExecution](row.Definition)
	if e != nil {
		return 0, nil, id, e
	}
	switch action {
	case "stop":
		row.DesiredState = "failed"
	case "pause":
		if row.State == "paused" || row.DesiredState == "failed" {
			return 0, nil, id, conflict
		}
		row.DesiredState = "paused"
	case "resume":
		var pending int64
		if e = tx.Model(&GovernanceRun{}).Where("definition->'stream_snapshot'->>'id'=? AND state IN ('queued','running')", row.ID).Count(&pending).Error; e != nil {
			return 0, nil, id, e
		}
		if pending > 0 {
			return 0, nil, id, problem(409, "STREAM_SNAPSHOT_RUNNING", "成果快照正在生成，完成后才能恢复持续更新")
		}
		if row.State != "paused" || row.Savepoint == "" || row.DesiredState == "failed" {
			return 0, nil, id, conflict
		}
		if e = s.governanceFlowAuthorize(tx.Statement.Context, p, def); e != nil {
			return 0, nil, id, e
		}
		row.Generation++
		row.State = "starting"
		row.DesiredState = "running"
		row.JobID = ""
		row.SessionID = ""
		row.OperationID = ""
		row.SubmissionStarted = false
		row.StopTrigger = ""
		row.ErrorCode = ""
		row.CaughtUp = false
	case "reconcile":
		if row.State != "unknown" {
			return 0, nil, id, conflict
		}
	case "snapshot":
		return s.governanceStreamSnapshotRun(tx, p, row, def)
	default:
		return 0, nil, id, invalid
	}
	row.UpdatedAt = time.Now()
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(row), id, nil
}

func (s *Server) governanceStreamAuthorize(ctx context.Context, row GovernanceStream, def governanceFlowExecution) (identity.Principal, error) {
	p, e := s.etlPrincipal(ctx, row.ActorID)
	if e != nil {
		return p, e
	}
	if e = s.governanceFlowAuthorize(ctx, p, def); e != nil {
		return p, e
	}
	ctx = context.WithValue(ctx, governanceWorkspaceContextKey{}, governanceWorkspaceScope{ID: row.WorkspaceID, Internal: true})
	_, e = governanceWorkspaceWrite(s.db.WithContext(ctx), p, row.WorkspaceID)
	return p, e
}
