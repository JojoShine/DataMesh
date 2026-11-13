package workflow

import (
	"context"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type GovernanceIssueNotice struct {
	ID           string     `json:"id" gorm:"primaryKey"`
	IssueID      string     `json:"issue_id"`
	UserID       string     `json:"-"`
	Code         string     `json:"code"`
	IssueVersion int        `json:"issue_version"`
	CreatedAt    time.Time  `json:"created_at"`
	SeenAt       *time.Time `json:"seen_at"`
}

func (GovernanceIssueNotice) TableName() string { return "governance_issue_notices" }
func governanceIssueNotice(tx *gorm.DB, issue GovernanceQualityIssue, code string) error {
	for _, user := range []string{issue.ActorID, issue.AssigneeID} {
		id, e := newID()
		if e != nil {
			return e
		}
		notice := GovernanceIssueNotice{ID: id, IssueID: issue.ID, UserID: user, Code: code, IssueVersion: issue.Version}
		if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&notice).Error; e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) sweepGovernanceIssueNotices(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows := []GovernanceQualityIssue{}
		if e := tx.Where("state NOT IN ('closed','superseded') AND due_at<now() AND NOT EXISTS(SELECT 1 FROM governance_issue_notices n WHERE n.issue_id=governance_quality_issues.id AND n.issue_version=governance_quality_issues.version AND n.code='overdue')").Order("due_at,id").Limit(100).Find(&rows).Error; e != nil {
			return e
		}
		for _, row := range rows {
			if e := governanceIssueNotice(tx, row, "overdue"); e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Server) registerGovernanceIssueNotices(api, write *gin.RouterGroup) {
	api.GET("/governance/issue-notices", s.require("governance", "governor"), func(c *gin.Context) {
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
		p := principal(c)
		q := workspaceQuery(s.db.WithContext(c.Request.Context()).Model(&GovernanceIssueNotice{}), "(SELECT r.workspace_id FROM governance_quality_issues i JOIN governance_runs r ON r.id=i.run_id WHERE i.id=governance_issue_notices.issue_id)").Where("user_id=? AND EXISTS(SELECT 1 FROM governance_quality_issues i WHERE i.id=governance_issue_notices.issue_id AND i.department_id=? AND (i.actor_id=? OR i.assignee_id=?))", p.UserID, p.DepartmentID, p.UserID, p.UserID)
		switch c.Query("seen") {
		case "":
		case "true":
			q = q.Where("seen_at IS NOT NULL")
		case "false":
			q = q.Where("seen_at IS NULL")
		default:
			s.fail(c, invalid)
			return
		}
		var total int64
		rows := []GovernanceIssueNotice{}
		if e = q.Count(&total).Error; e == nil {
			e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	write.POST("/governance/issue-notices/:id/seen", s.require("governance", "governor"), s.mutatePath("issue_notice_seen", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		var notice GovernanceIssueNotice
		if e := tx.Where("id=? AND user_id=?", id, p.UserID).Take(&notice).Error; e != nil {
			return 0, nil, id, e
		}
		if _, e := governanceIssueRead(tx, p, notice.IssueID, false); e != nil {
			return 0, nil, id, e
		}
		// Preserve the first read timestamp across retries and concurrent tabs.
		e := tx.Model(&notice).Where("seen_at IS NULL").Update("seen_at", time.Now().UTC()).Error
		if e == nil {
			e = tx.Where("id=? AND user_id=?", id, p.UserID).Take(&notice).Error
		}
		return 200, encode(notice), id, e
	}))
}
