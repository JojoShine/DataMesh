package workflow

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AssetFeedback struct {
	ID                   string          `json:"id" gorm:"primaryKey"`
	ReporterID           string          `json:"-"`
	ReporterDepartmentID string          `json:"-"`
	DepartmentID         string          `json:"-"`
	WorkspaceID          string          `json:"workspace_id"`
	ResourceType         string          `json:"resource_type"`
	ResourceID           string          `json:"resource_id"`
	ContractVersion      int             `json:"contract_version"`
	DirectoryID          string          `json:"directory_id"`
	ResourceName         string          `json:"resource_name"`
	Category             string          `json:"category"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	Fields               json.RawMessage `json:"fields" gorm:"type:jsonb"`
	RunID                string          `json:"-"`
	IssueID              *string         `json:"issue_id"`
	State                string          `json:"state"`
	Response             string          `json:"response"`
	Version              int             `json:"version"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	IssueState           string          `json:"issue_state" gorm:"-"`
	CanRemediate         bool            `json:"can_remediate" gorm:"-"`
}

func (AssetFeedback) TableName() string { return "asset_feedback" }

type AssetFeedbackEvent struct {
	ID         int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	FeedbackID string    `json:"-"`
	Action     string    `json:"action"`
	Response   string    `json:"response"`
	CreatedAt  time.Time `json:"created_at"`
}

func (AssetFeedbackEvent) TableName() string { return "asset_feedback_events" }

func feedbackAccess(tx *gorm.DB, p identity.Principal, id string, governance, lock bool) (AssetFeedback, error) {
	var row AssetFeedback
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	e := q.Take(&row).Error
	if e != nil {
		return row, e
	}
	if !governance {
		if row.ReporterID != p.UserID {
			return row, denied
		}
		return row, nil
	}
	if !admin(p) && row.DepartmentID != p.DepartmentID {
		return row, denied
	}
	if row.WorkspaceID != "" {
		if e = governanceWorkspaceMatch(tx.Statement.Context, row.WorkspaceID); e != nil {
			return row, e
		}
		_, e = governanceWorkspaceRead(tx, p, row.WorkspaceID, lock)
	}
	return row, e
}
func feedbackEnrich(tx *gorm.DB, p identity.Principal, row *AssetFeedback, governance bool) error {
	if row.IssueID != nil {
		var issue GovernanceQualityIssue
		if e := tx.Where("id=?", *row.IssueID).Take(&issue).Error; e != nil {
			return e
		}
		row.IssueState = issue.State
	}
	if governance && row.RunID != "" && row.WorkspaceID != "" {
		_, e := governanceRunRead(tx, p, row.RunID, false)
		if e == nil {
			row.CanRemediate = true
		} else {
			var problem *Problem
			if !errors.As(e, &problem) {
				return e
			}
		}
	}
	return nil
}
func (s *Server) registerAssetFeedback(api, write *gin.RouterGroup) {
	for _, governance := range []bool{false, true} {
		prefix := "/assets/feedback"
		gate := s.require("assets")
		if governance {
			prefix = "/governance/asset-feedback"
			gate = s.require("governance", "governor")
		}
		api.GET(prefix, gate, func(c *gin.Context) {
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
			tx := s.db.WithContext(c.Request.Context())
			p := principal(c)
			q := tx.Model(&AssetFeedback{})
			if governance {
				if !admin(p) {
					q = q.Where("department_id=?", p.DepartmentID)
				}
				if id := workspaceScope(tx.Statement.Context).ID; id != "" {
					q = q.Where("workspace_id='' OR workspace_id=?", id)
				}
			} else {
				q = q.Where("reporter_id=?", p.UserID)
			}
			if id := c.Query("resource_id"); id != "" {
				q = q.Where("resource_id=?", id)
			}
			if state := c.Query("state"); state != "" {
				if !slices.Contains([]string{"submitted", "in_progress", "resolved"}, state) {
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
			rows := []AssetFeedback{}
			if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
				s.fail(c, e)
				return
			}
			for i := range rows {
				if e = feedbackEnrich(tx, p, &rows[i], governance); e != nil {
					s.fail(c, e)
					return
				}
			}
			c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
		})
		api.GET(prefix+"/:id", gate, func(c *gin.Context) {
			tx := s.db.WithContext(c.Request.Context())
			p := principal(c)
			row, e := feedbackAccess(tx, p, c.Param("id"), governance, false)
			if e != nil {
				s.fail(c, e)
				return
			}
			if e = feedbackEnrich(tx, p, &row, governance); e != nil {
				s.fail(c, e)
				return
			}
			events := []AssetFeedbackEvent{}
			if e = tx.Where("feedback_id=?", row.ID).Order("id").Find(&events).Error; e != nil {
				s.fail(c, e)
				return
			}
			c.JSON(200, struct {
				AssetFeedback
				Events []AssetFeedbackEvent `json:"events"`
			}{row, events})
		})
	}
	write.POST("/assets/feedback", s.require("assets"), s.mutate("asset_feedback_create", s.createAssetFeedback))
	write.POST("/governance/asset-feedback/:id/reply", s.require("governance", "governor"), s.mutatePath("asset_feedback_reply", s.replyAssetFeedback))
	write.POST("/governance/asset-feedback/:id/remediate", s.require("governance", "governor"), s.mutatePath("asset_feedback_remediate", s.remediateAssetFeedback))
}
func (s *Server) createAssetFeedback(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		Resource    Resource `json:"resource"`
		GrantID     string   `json:"grant_id"`
		Category    string   `json:"category"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Fields      []string `json:"fields"`
	}](b)
	if e != nil {
		return 0, nil, "", e
	}
	if !explicitRole(p, "consumer", "provider") {
		return 0, nil, "", denied
	}
	if !valid(v.Title, 200) || !valid(v.Description, 4000) || len(v.Fields) > 100 || !slices.Contains([]string{"quality", "freshness", "meaning", "access"}, v.Category) {
		return 0, nil, "", invalid
	}
	var snap SharingSnapshot
	var department string
	if v.GrantID != "" {
		grant, err := get(tx, "grant", v.GrantID, false)
		if err != nil {
			return 0, nil, "", err
		}
		g, err := decode[GrantData](grant.Body)
		if err != nil {
			return 0, nil, "", err
		}
		if g.GranteeUserID != p.UserID || g.Scope.Resource != v.Resource || g.Snapshot == nil {
			return 0, nil, "", denied
		}
		for _, f := range v.Fields {
			if !slices.Contains(g.Scope.Fields, f) {
				return 0, nil, "", denied
			}
		}
		snap = *g.Snapshot
		department = grant.DepartmentID
	} else {
		snap, e = requireSharingSnapshot(tx, v.Resource, nil)
		if e != nil {
			return 0, nil, "", e
		}
		r, _, err := resource(tx, v.Resource, false)
		if err != nil {
			return 0, nil, "", err
		}
		department = r.DepartmentID
	}
	if e = validateSharingFields(tx, snap, v.Fields); e != nil {
		return 0, nil, "", e
	}
	revision, e := get(tx, "directory_revision", snap.DirectoryRevisionID, false)
	if e != nil {
		return 0, nil, "", e
	}
	directory, e := decode[DirectoryRevisionData](revision.Body)
	if e != nil {
		return 0, nil, "", e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	if v.Fields == nil {
		v.Fields = []string{}
	}
	row := AssetFeedback{ID: id, ReporterID: p.UserID, ReporterDepartmentID: p.DepartmentID, DepartmentID: department, ResourceType: v.Resource.Type, ResourceID: v.Resource.ID, ContractVersion: v.Resource.Version, DirectoryID: snap.DirectoryID, ResourceName: directory.Definition.Name, Category: v.Category, Title: v.Title, Description: v.Description, Fields: encode(v.Fields), State: "submitted", Version: 1}
	// Historical grants preserve the reported contract. Never attach a newer
	// source run just because an API now follows the latest published result.
	if v.Resource.Type == "governed_dataset" {
		r, err := get(tx, "dataset", v.Resource.ID, false)
		if err != nil {
			return 0, nil, id, err
		}
		if r.Version == v.Resource.Version {
			data, err := decode[DatasetData](r.Body)
			if err != nil {
				return 0, nil, id, err
			}
			if data.GovernanceRunID != "" {
				var run GovernanceRun
				if err = tx.Where("id=?", data.GovernanceRunID).Take(&run).Error; err != nil {
					return 0, nil, id, err
				}
				row.RunID = run.ID
				row.WorkspaceID = run.WorkspaceID
				row.DepartmentID = run.DepartmentID
			}
		}
	}
	if e = tx.Create(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if e = tx.Create(&AssetFeedbackEvent{FeedbackID: id, Action: "submitted"}).Error; e != nil {
		return 0, nil, id, e
	}
	return 201, encode(row), id, nil
}
func (s *Server) replyAssetFeedback(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		BaseVersion int    `json:"base_version"`
		State       string `json:"state"`
		Response    string `json:"response"`
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	row, e := feedbackAccess(tx, p, id, true, true)
	if e != nil {
		return 0, nil, id, e
	}
	if row.Version != v.BaseVersion || row.State == "resolved" {
		return 0, nil, id, conflict
	}
	if !valid(v.Response, 4000) || !slices.Contains([]string{"in_progress", "resolved"}, v.State) {
		return 0, nil, id, invalid
	}
	if v.State == "resolved" && row.IssueID != nil {
		issue, err := governanceIssueRead(tx, p, *row.IssueID, true)
		if err != nil {
			return 0, nil, id, err
		}
		complete, err := governanceIssueResolutionComplete(tx, p, issue)
		if err != nil {
			return 0, nil, id, err
		}
		if !complete {
			return 0, nil, id, problem(409, "ASSET_REMEDIATION_PENDING", "关联整改单通过复测并关闭后，才能完成反馈")
		}
	}
	row.State = v.State
	row.Response = v.Response
	row.Version++
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if e = tx.Create(&AssetFeedbackEvent{FeedbackID: id, Action: v.State, Response: v.Response}).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(row), id, nil
}
func (s *Server) remediateAssetFeedback(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		BaseVersion int       `json:"base_version"`
		AssigneeID  string    `json:"assignee_id"`
		DueAt       time.Time `json:"due_at"`
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	row, e := feedbackAccess(tx, p, id, true, true)
	if e != nil {
		return 0, nil, id, e
	}
	if row.Version != v.BaseVersion || row.State == "resolved" || row.IssueID != nil {
		return 0, nil, id, conflict
	}
	if row.RunID == "" || row.WorkspaceID == "" {
		return 0, nil, id, problem(409, "ASSET_RUN_REQUIRED", "此资产没有可关联的治理运行，请直接受理并回复，或联系提供部门处理")
	}
	run, e := governanceRunRead(tx, p, row.RunID, false)
	if e != nil {
		return 0, nil, id, e
	}
	if run.Kind != "dataflow" || lightweightRunMarker(run) {
		return 0, nil, id, invalid
	}
	if e = governanceLock(tx, run.DepartmentID); e != nil {
		return 0, nil, id, e
	}
	var issue GovernanceQualityIssue
	e = tx.Where("run_id=?", run.ID).Take(&issue).Error
	if e == nil {
		issue, e = governanceIssueRead(tx, p, issue.ID, true)
		if e != nil {
			return 0, nil, id, e
		}
		if governanceIssueTerminal(issue.State) {
			return 0, nil, id, problem(409, "ASSET_REMEDIATION_CLOSED", "此运行已有关闭的整改单，请核对原处理结论后回复反馈")
		}
	} else if errors.Is(e, gorm.ErrRecordNotFound) {
		if e = governanceMember(tx, v.AssigneeID, run.DepartmentID); e != nil {
			return 0, nil, id, e
		}
		if !v.DueAt.After(time.Now()) || v.DueAt.After(time.Now().AddDate(1, 0, 0)) {
			return 0, nil, id, invalid
		}
		var count int64
		if e = tx.Table("governance_snapshot_purges").Where("run_id=?", run.ID).Count(&count).Error; e != nil {
			return 0, nil, id, e
		}
		if count > 0 {
			return 0, nil, id, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "原始运行快照已清理，无法建立复测整改")
		}
		def, err := decode[governanceFlowExecution](run.Definition)
		if err != nil {
			return 0, nil, id, err
		}
		issueID, err := newID()
		if err != nil {
			return 0, nil, id, err
		}
		issue = GovernanceQualityIssue{ID: issueID, DepartmentID: run.DepartmentID, ActorID: p.UserID, AssigneeID: v.AssigneeID, RunID: run.ID, FlowID: def.Flow.ID, FlowVersion: def.Flow.Version, DueAt: v.DueAt.UTC(), State: "open", Version: 1}
		if e = tx.Create(&issue).Error; e != nil {
			return 0, nil, id, e
		}
		if e = tx.Create(&GovernanceQualityIssueEvent{IssueID: issue.ID, ActorID: p.UserID, Action: "created", Note: "来自资产使用反馈：" + row.Title, RunID: &run.ID}).Error; e != nil {
			return 0, nil, id, e
		}
	} else {
		return 0, nil, id, e
	}
	row.IssueID = &issue.ID
	row.State = "in_progress"
	row.Version++
	row.Response = "已关联治理整改，处理完成后将回复核验结果。"
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if e = tx.Create(&AssetFeedbackEvent{FeedbackID: id, Action: "remediation", Response: row.Response}).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(row), id, nil
}
