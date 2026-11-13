package workflow

import (
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceQualityIssue struct {
	OriginKind         string          `json:"origin_kind" gorm:"default:dataflow"`
	Evidence           json.RawMessage `json:"-" gorm:"type:jsonb;default:'{}'"`
	MonitorID          *string         `json:"monitor_id,omitempty"`
	ObservationID      *string         `json:"observation_id,omitempty"`
	ID                 string          `json:"id" gorm:"primaryKey"`
	DepartmentID       string          `json:"department_id"`
	ActorID            string          `json:"actor_id"`
	AssigneeID         string          `json:"assignee_id"`
	RunID              string          `json:"run_id"`
	FlowID             string          `json:"flow_id"`
	FlowVersion        int             `json:"flow_version"`
	DueAt              time.Time       `json:"due_at"`
	State              string          `json:"state"`
	Version            int             `json:"version"`
	RetestRunID        *string         `json:"retest_run_id"`
	ReplacementRunID   *string         `json:"replacement_run_id,omitempty"`
	ReplacementIssueID *string         `json:"replacement_issue_id,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	ClosedAt           *time.Time      `json:"closed_at"`
}

func (GovernanceQualityIssue) TableName() string { return "governance_quality_issues" }

type GovernanceQualityIssueEvent struct {
	ID        int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	IssueID   string    `json:"issue_id"`
	ActorID   string    `json:"actor_id"`
	Action    string    `json:"action"`
	Note      string    `json:"note"`
	RunID     *string   `json:"run_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (GovernanceQualityIssueEvent) TableName() string { return "governance_quality_issue_events" }
func governanceIssueRead(tx *gorm.DB, p identity.Principal, id string, lock bool) (GovernanceQualityIssue, error) {
	var issue GovernanceQualityIssue
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	e := q.Take(&issue).Error
	if e == nil && !admin(p) && (issue.DepartmentID != p.DepartmentID || (issue.ActorID != p.UserID && issue.AssigneeID != p.UserID)) {
		e = denied
	}
	if e == nil {
		var run GovernanceRun
		e = tx.Where("id=?", issue.RunID).Take(&run).Error
		if e == nil && run.WorkspaceID == "" {
			e = problem(409, "LIGHTWEIGHT_QUALITY_ISSUE_UNAVAILABLE", "轻量治理请在私有结果中查看和处理质量问题")
		}
		if e == nil {
			e = governanceWorkspaceMatch(tx.Statement.Context, run.WorkspaceID)
			if e == nil && workspaceScope(tx.Statement.Context).Mutation {
				_, e = governanceWorkspaceWrite(tx, p, run.WorkspaceID)
			}
		}
	}
	return issue, e
}
func (s *Server) registerGovernanceQualityIssues(api, write *gin.RouterGroup) {
	s.registerGovernanceIssueEvidence(api)
	s.registerGovernanceIssueNotices(api, write)
	api.GET("/governance/quality-issues", s.require("governance", "governor"), func(c *gin.Context) {
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
		q := workspaceQuery(s.db.WithContext(c.Request.Context()).Model(&GovernanceQualityIssue{}), "(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_quality_issues.run_id)").Where("(SELECT r.workspace_id FROM governance_runs r WHERE r.id=governance_quality_issues.run_id) IS NOT NULL")
		if !admin(p) {
			q = q.Where("department_id=? AND (actor_id=? OR assignee_id=?)", p.DepartmentID, p.UserID, p.UserID)
		}
		if state := c.Query("state"); state != "" {
			if !slices.Contains([]string{"open", "in_progress", "retesting", "closed", "superseded"}, state) {
				s.fail(c, invalid)
				return
			}
			q = q.Where("state=?", state)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []GovernanceQualityIssue{}
		if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	api.GET("/governance/quality-issues/:id", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		issue, e := governanceIssueRead(tx, principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		events := []GovernanceQualityIssueEvent{}
		if e = tx.Where("issue_id=?", issue.ID).Order("id").Find(&events).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, struct {
			GovernanceQualityIssue
			Events []GovernanceQualityIssueEvent `json:"events"`
		}{issue, events})
	})
	write.POST("/governance/quality-issues", s.require("governance", "governor"), s.mutate("governance_quality_create", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			RunID      string    `json:"run_id"`
			AssigneeID string    `json:"assignee_id"`
			DueAt      time.Time `json:"due_at"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		run, e := governanceRunRead(tx, p, v.RunID, false)
		if e != nil {
			return 0, nil, v.RunID, e
		}
		if lightweightRunMarker(run) {
			return 0, nil, run.ID, problem(409, "LIGHTWEIGHT_QUALITY_ISSUE_UNAVAILABLE", "轻量治理请在私有结果中查看和处理质量问题")
		}
		if run.Kind == "profile" {
			return s.createProfileIssue(tx, p, run, v.AssigneeID, v.DueAt)
		}
		if run.Kind != "dataflow" {
			return 0, nil, run.ID, invalid
		}
		if e = governanceMember(tx, v.AssigneeID, run.DepartmentID); e != nil {
			return 0, nil, run.ID, e
		}
		if !v.DueAt.After(time.Now()) || v.DueAt.After(time.Now().AddDate(1, 0, 0)) {
			return 0, nil, run.ID, invalid
		}
		execution, e := decode[governanceFlowExecution](run.Definition)
		if e != nil {
			return 0, nil, run.ID, e
		}
		eligible := run.State == "failed" && slices.Contains([]string{"GOVERNANCE_QUALITY_BLOCKED", "GOVERNANCE_OUTPUT_DUPLICATE_KEY", "GOVERNANCE_OUTPUT_NULL", "GOVERNANCE_OUTPUT_TYPE", "GOVERNANCE_STANDARD_FAILED", "GOVERNANCE_CODE_UNMATCHED"}, run.ErrorCode)
		if run.State == "succeeded" {
			report, e := decode[governanceFlowReport](run.Result)
			if e != nil {
				return 0, nil, run.ID, e
			}
			eligible = report.RejectedRows > 0
		}
		if !eligible {
			return 0, nil, run.ID, problem(409, "QUALITY_ISSUE_NOT_REQUIRED", "此运行没有可整改的质量异常")
		}
		if e = governanceLock(tx, run.DepartmentID); e != nil {
			return 0, nil, run.ID, e
		}
		var count int64
		if e = tx.Table("governance_snapshot_purges").Where("run_id=?", run.ID).Count(&count).Error; e != nil {
			return 0, nil, run.ID, e
		}
		if count > 0 {
			return 0, nil, run.ID, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "运行快照已清理，无法创建需要原始快照的整改任务")
		}
		if e = tx.Model(&GovernanceQualityIssue{}).Where("run_id=?", run.ID).Count(&count).Error; e != nil {
			return 0, nil, run.ID, e
		}
		if count > 0 {
			return 0, nil, run.ID, conflict
		}
		id, e := newID()
		if e != nil {
			return 0, nil, run.ID, e
		}
		issue := GovernanceQualityIssue{ID: id, DepartmentID: run.DepartmentID, ActorID: p.UserID, AssigneeID: v.AssigneeID, RunID: run.ID, FlowID: execution.Flow.ID, FlowVersion: execution.Flow.Version, DueAt: v.DueAt.UTC(), State: "open", Version: 1}
		if e = tx.Create(&issue).Error; e != nil {
			return 0, nil, id, e
		}
		if e = tx.Create(&GovernanceQualityIssueEvent{IssueID: id, ActorID: p.UserID, Action: "created", RunID: &run.ID}).Error; e != nil {
			return 0, nil, id, e
		}
		if e = governanceIssueNotice(tx, issue, "assigned"); e != nil {
			return 0, nil, id, e
		}
		return 201, encode(issue), id, nil
	}))
	for _, action := range []string{"notes", "assign", "retest", "close", "supersede"} {
		a := action
		write.POST("/governance/quality-issues/:id/"+a, s.require("governance", "governor"), s.mutatePath("governance_quality_"+a, s.governanceIssueAction(a)))
	}
}
func (s *Server) governanceIssueAction(action string) func(*gorm.DB, identity.Principal, string, []byte) (int, json.RawMessage, string, error) {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			BaseVersion      int        `json:"base_version"`
			Note             string     `json:"note"`
			AssigneeID       string     `json:"assignee_id"`
			DueAt            *time.Time `json:"due_at"`
			ReplacementRunID string     `json:"replacement_run_id"`
		}](b)
		if e != nil {
			return 0, nil, id, e
		}
		issue, e := governanceIssueRead(tx, p, id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if v.BaseVersion != issue.Version || governanceIssueTerminal(issue.State) {
			return 0, nil, id, conflict
		}
		status := 200
		event := GovernanceQualityIssueEvent{IssueID: id, ActorID: p.UserID, Action: action}
		switch action {
		case "notes":
			if !valid(v.Note, 4000) {
				return 0, nil, id, invalid
			}
			event.Note = v.Note
			issue.State = "in_progress"
		case "assign":
			if e = governanceMember(tx, v.AssigneeID, issue.DepartmentID); e != nil {
				return 0, nil, id, e
			}
			issue.AssigneeID = v.AssigneeID
			if v.DueAt != nil {
				if !v.DueAt.After(time.Now()) || v.DueAt.After(time.Now().AddDate(1, 0, 0)) {
					return 0, nil, id, invalid
				}
				issue.DueAt = v.DueAt.UTC()
			}
		case "retest":
			if issue.RetestRunID != nil {
				var last GovernanceRun
				if e = tx.Where("id=?", *issue.RetestRunID).Take(&last).Error; e != nil {
					return 0, nil, id, e
				}
				if last.State == "queued" || last.State == "running" {
					return 0, nil, id, conflict
				}
			}
			var next string
			if governanceProfileIssueKind(issue.OriginKind) {
				next, e = s.retestProfileIssue(tx, p, issue)
			} else {
				next, e = s.retestFlowIssue(tx, p, issue)
			}
			if e != nil {
				return 0, nil, id, e
			}
			issue.RetestRunID = &next
			event.RunID = &next
			issue.State = "retesting"
			status = 201
		case "close":
			if issue.RetestRunID == nil {
				return 0, nil, id, problem(409, "QUALITY_RETEST_REQUIRED", "必须通过真实复测后关闭问题")
			}
			if governanceProfileIssueKind(issue.OriginKind) {
				if e = s.closeProfileIssue(tx, p, issue, v.Note); e != nil {
					return 0, nil, id, e
				}
			} else {
				var run GovernanceRun
				e := tx.Where("id=? AND department_id=?", *issue.RetestRunID, issue.DepartmentID).Take(&run).Error
				if e != nil {
					return 0, nil, id, e
				}
				if run.State != "succeeded" || run.Kind != "dataflow" {
					return 0, nil, id, conflict
				}
				def, e := decode[governanceFlowExecution](run.Definition)
				if e != nil {
					return 0, nil, id, e
				}
				report, e := decode[governanceFlowReport](run.Result)
				if e != nil {
					return 0, nil, id, e
				}
				if def.Flow.ID != issue.FlowID || def.Flow.Version != issue.FlowVersion || report.RejectedRows != 0 {
					return 0, nil, id, problem(409, "QUALITY_RETEST_FAILED", "复测仍有质量异常，不能关闭")
				}
				var original GovernanceRun
				if e = tx.Where("id=?", issue.RunID).Take(&original).Error; e != nil {
					return 0, nil, id, e
				}
				basis, e := decode[governanceFlowExecution](original.Definition)
				if e != nil {
					return 0, nil, id, e
				}
				if !run.CreatedAt.After(issue.CreatedAt) || !governanceIssueFlowMatches(def, basis) {
					return 0, nil, id, problem(409, "QUALITY_RETEST_EVIDENCE", "复测与原运行参数、输入契约或固定规则不一致")
				}
				actor, e := s.etlPrincipal(tx.Statement.Context, run.ActorID)
				if e != nil {
					return 0, nil, id, e
				}
				if e = s.governanceFlowAuthorize(governanceReadContext(tx.Statement.Context), actor, def); e != nil {
					return 0, nil, id, e
				}
			}
			now := time.Now().UTC()
			issue.State = "closed"
			issue.ClosedAt = &now
			event.RunID = issue.RetestRunID
		case "supersede":
			if !valid(v.Note, 4000) {
				return 0, nil, id, invalid
			}
			if e = s.supersedeQualityIssue(tx, p, &issue, v.ReplacementRunID, v.Note); e != nil {
				return 0, nil, id, e
			}
			event.Note = v.Note
			event.RunID = issue.ReplacementRunID
		default:
			return 0, nil, id, invalid
		}
		issue.Version++
		save := tx
		if governanceProfileIssueKind(issue.OriginKind) {
			save = save.Omit("FlowID", "FlowVersion")
		}
		if e = save.Save(&issue).Error; e != nil {
			return 0, nil, id, e
		}
		if e = tx.Create(&event).Error; e != nil {
			return 0, nil, id, e
		}
		code := "updated"
		if action == "assign" {
			code = "assigned"
		}
		if e = governanceIssueNotice(tx, issue, code); e != nil {
			return 0, nil, id, e
		}
		return status, encode(issue), id, nil
	}
}

func governanceIssueTerminal(state string) bool { return state == "closed" || state == "superseded" }
