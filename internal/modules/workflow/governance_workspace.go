package workflow

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GovernanceWorkspace struct {
	RequiredReleaseKinds []string  `json:"required_release_kinds" gorm:"serializer:json;type:jsonb;default:'[]'"`
	ID                   string    `json:"id" gorm:"primaryKey"`
	DepartmentID         string    `json:"department_id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	OwnerID              *string   `json:"owner_id"`
	State                string    `json:"state"`
	IsLegacy             bool      `json:"is_legacy"`
	MaxConcurrentRuns    int       `json:"max_concurrent_runs"`
	Version              int       `json:"version"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func (GovernanceWorkspace) TableName() string { return "governance_workspaces" }

type governanceWorkspaceInput struct {
	RequiredReleaseKinds *[]string `json:"required_release_kinds,omitempty"`
	ReleasePolicyReason  string    `json:"release_policy_reason"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	BaseVersion          int       `json:"base_version"`
	MaxConcurrentRuns    int       `json:"max_concurrent_runs"`
}

func (s *Server) registerGovernanceWorkspaces(api, write *gin.RouterGroup) {
	api.GET("/governance/workspaces", s.require("governance", "governor"), func(c *gin.Context) {
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
		q := s.db.WithContext(c.Request.Context()).Model(&GovernanceWorkspace{})
		if !admin(principal(c)) {
			q = q.Where("department_id=?", principal(c).DepartmentID)
		}
		if search := c.Query("search"); search != "" {
			if len(search) > 200 {
				s.fail(c, invalid)
				return
			}
			q = q.Where("name ILIKE ?", "%"+search+"%")
		}
		if state := c.Query("state"); state != "" {
			if state != "active" && state != "archived" {
				s.fail(c, invalid)
				return
			}
			q = q.Where("state=?", state)
		}
		var count int64
		if e = q.Count(&count).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []GovernanceWorkspace{}
		if e = q.Order("updated_at DESC,id").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": count, "page": page, "page_size": size})
	})
	api.GET("/governance/workspaces/:id", s.require("governance", "governor"), func(c *gin.Context) {
		row, e := governanceWorkspaceRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, row)
	})
	write.POST("/governance/workspaces", s.require("governance", "governor"), s.mutate("governance_workspace_create", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[governanceWorkspaceInput](b)
		if e != nil {
			return 0, nil, "", e
		}
		input.Name = strings.TrimSpace(input.Name)
		if !valid(input.Name, 800) || utf8.RuneCountInString(input.Name) > 200 || utf8.RuneCountInString(input.Description) > 4000 {
			return 0, nil, "", invalid
		}
		if input.MaxConcurrentRuns == 0 {
			input.MaxConcurrentRuns = 2
		}
		if input.MaxConcurrentRuns < 1 || input.MaxConcurrentRuns > 10 {
			return 0, nil, "", invalid
		}
		id, e := newID()
		if e != nil {
			return 0, nil, "", e
		}
		owner := p.UserID
		row := GovernanceWorkspace{ID: id, DepartmentID: p.DepartmentID, Name: input.Name, Description: input.Description, OwnerID: &owner, State: "active", MaxConcurrentRuns: input.MaxConcurrentRuns, Version: 1}
		if input.RequiredReleaseKinds != nil {
			if e = governanceWorkspaceReleasePolicy(&row, p, *input.RequiredReleaseKinds, input.ReleasePolicyReason); e != nil {
				return 0, nil, id, e
			}
		}
		if e = tx.Create(&row).Error; e != nil {
			return 0, nil, id, e
		}
		return 201, encode(row), id, nil
	}))
	for _, action := range []string{"update", "archive"} {
		kind := action
		write.POST("/governance/workspaces/:id/"+kind, s.require("governance", "governor"), s.mutatePath("governance_workspace_"+kind, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			input, e := bind[governanceWorkspaceInput](b)
			if e != nil {
				return 0, nil, id, e
			}
			row, e := governanceWorkspaceLock(tx, id)
			if e != nil {
				return 0, nil, id, e
			}
			if !admin(p) && (row.DepartmentID != p.DepartmentID || row.OwnerID == nil || *row.OwnerID != p.UserID) {
				return 0, nil, id, denied
			}
			if input.BaseVersion != row.Version {
				return 0, nil, id, problem(409, "GOVERNANCE_WORKSPACE_VERSION_CONFLICT", "空间信息已更新，请刷新后再操作")
			}
			if row.State != "active" {
				return 0, nil, id, problem(409, "GOVERNANCE_WORKSPACE_ARCHIVED", "空间已经归档，历史内容仍可查看")
			}
			if kind == "archive" {
				if row.IsLegacy {
					return 0, nil, id, problem(409, "GOVERNANCE_WORKSPACE_LEGACY", "历史治理空间用于兼容原任务，请逐项管理其中的任务")
				}
				var count int64
				if e = tx.Model(&GovernanceRun{}).Where("workspace_id=? AND state IN ('queued','running')", id).Count(&count).Error; e != nil {
					return 0, nil, id, e
				}
				var deployments int64
				if e = tx.Table("governance_model_deployments j").Joins("JOIN governance_definitions d ON d.id=j.model_id").Where("d.workspace_id=? AND j.state IN ('queued','running','unknown')", id).Count(&deployments).Error; e != nil {
					return 0, nil, id, e
				}
				count += deployments
				if count > 0 {
					return 0, nil, id, problem(409, "GOVERNANCE_WORKSPACE_BUSY", "空间还有等待或正在执行的任务，请先查看运行记录并等待完成或取消，再归档")
				}
				if e = tx.Model(&GovernanceSchedule{}).Where("workspace_id=? AND state='active'", id).Updates(map[string]interface{}{"state": "paused", "version": gorm.Expr("version+1"), "updated_at": time.Now().UTC()}).Error; e != nil {
					return 0, nil, id, e
				}
				row.State = "archived"
			} else {
				input.Name = strings.TrimSpace(input.Name)
				if !valid(input.Name, 800) || utf8.RuneCountInString(input.Name) > 200 || utf8.RuneCountInString(input.Description) > 4000 {
					return 0, nil, id, invalid
				}
				if input.MaxConcurrentRuns == 0 {
					input.MaxConcurrentRuns = row.MaxConcurrentRuns
				}
				if input.MaxConcurrentRuns < 1 || input.MaxConcurrentRuns > 10 {
					return 0, nil, id, invalid
				}
				row.Name = input.Name
				row.Description = input.Description
				row.MaxConcurrentRuns = input.MaxConcurrentRuns
				if input.RequiredReleaseKinds != nil {
					if e = governanceWorkspaceReleasePolicy(&row, p, *input.RequiredReleaseKinds, input.ReleasePolicyReason); e != nil {
						return 0, nil, id, e
					}
				}
			}
			row.Version++
			row.UpdatedAt = time.Now().UTC()
			if e = tx.Save(&row).Error; e != nil {
				return 0, nil, id, e
			}
			return 200, encode(row), id, nil
		}))
	}
	s.registerGovernanceWorkspaceSources(api, write)
	api.GET("/governance/workspaces/:id/overview", s.require("governance", "governor"), s.governanceWorkspaceOverview)
}

func governanceWorkspaceReleasePolicy(row *GovernanceWorkspace, p identity.Principal, kinds []string, reason string) error {
	if row.DepartmentID != p.DepartmentID || (!admin(p) && (row.OwnerID == nil || *row.OwnerID != p.UserID)) {
		return denied
	}
	if len(kinds) > 6 {
		return invalid
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if !slices.Contains([]string{"master", "scenario", "model", "dataflow", "workflow", "quality_template"}, kind) || seen[kind] {
			return invalid
		}
		seen[kind] = true
	}
	slices.Sort(kinds)
	before := append([]string{}, row.RequiredReleaseKinds...)
	slices.Sort(before)
	if !reflect.DeepEqual(before, kinds) && !valid(reason, 4000) {
		return problem(400, "GOVERNANCE_RELEASE_POLICY_REASON", "调整必审范围需要填写原因")
	}
	row.RequiredReleaseKinds = kinds
	return nil
}
