package workflow

import (
	"encoding/json"
	"errors"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceWorkspaceSource struct {
	WorkspaceID     string    `json:"workspace_id" gorm:"primaryKey"`
	ResourceType    string    `json:"resource_type" gorm:"primaryKey"`
	ResourceID      string    `json:"resource_id" gorm:"primaryKey"`
	ContractVersion int       `json:"contract_version" gorm:"primaryKey"`
	ActorID         string    `json:"actor_id"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (GovernanceWorkspaceSource) TableName() string { return "governance_workspace_sources" }
func governanceWorkspaceReference(tx *gorm.DB, p identity.Principal, id string, resource Resource) error {
	space, e := governanceWorkspaceRead(tx, p, id, false)
	if e != nil {
		return e
	}
	if resource.Type == "master_snapshot" {
		row, body, e := masterSnapshotRead(tx, p, resource.ID)
		if e != nil {
			return e
		}
		if body.WorkspaceID != id || row.Version != resource.Version {
			return conflict
		}
		return nil
	}
	if space.IsLegacy {
		return nil
	}
	if resource.Type == "governed_dataset" {
		source, e := governanceDatasetWorkspace(tx, resource.ID)
		if e != nil {
			return e
		}
		if source != id {
			record, e := get(tx, "dataset", resource.ID, false)
			if e != nil {
				return e
			}
			if record.State != "published" {
				return problem(409, "GOVERNANCE_WORKSPACE_SOURCE_NOT_PUBLISHED", "其他空间的成果需要先发布并取得使用权限，才能作为本空间来源")
			}
		}
	}
	var count int64
	if e = tx.Model(&GovernanceWorkspaceSource{}).Where("workspace_id=? AND resource_type=? AND resource_id=? AND contract_version=? AND active", id, resource.Type, resource.ID, resource.Version).Count(&count).Error; e != nil {
		return e
	}
	if count != 1 {
		return problem(409, "GOVERNANCE_WORKSPACE_SOURCE_REQUIRED", "请先在当前空间的来源中关联这份数据及其版本，再用于处理")
	}
	return nil
}
func (s *Server) registerGovernanceWorkspaceSources(api, write *gin.RouterGroup) {
	api.GET("/governance/workspaces/:id/source-candidates", s.require("governance", "governor"), func(c *gin.Context) {
		if _, e := governanceWorkspaceRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false); e != nil {
			s.fail(c, e)
			return
		}
		c.Set("governance_source_candidates", true)
		s.metadataCatalog(c)
	})
	api.GET("/governance/workspaces/:id/sources", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		if _, e := governanceWorkspaceRead(tx, principal(c), c.Param("id"), false); e != nil {
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
		q := tx.Model(&GovernanceWorkspaceSource{}).Where("workspace_id=? AND active", c.Param("id"))
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []GovernanceWorkspaceSource{}
		if e = q.Order("created_at DESC,resource_id").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []gin.H{}
		for _, row := range rows {
			_, target, err := metadataTarget(tx, principal(c), row.ResourceID, false)
			if err != nil {
				var pe *Problem
				if !errors.Is(err, gorm.ErrRecordNotFound) && !(errors.As(err, &pe) && (pe.Status == 403 || pe.Status == 404 || pe.Status == 410)) {
					s.fail(c, err)
					return
				}
				items = append(items, gin.H{"reference": row, "resource": nil, "available": false})
				continue
			}
			items = append(items, gin.H{"reference": row, "resource": target, "available": target.SchemaVersion == row.ContractVersion})
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	})
	for _, remove := range []bool{false, true} {
		detach := remove
		path := "/governance/workspaces/:id/sources"
		action := "governance_workspace_source_add"
		if detach {
			path += "/remove"
			action = "governance_workspace_source_remove"
		}
		write.POST(path, s.require("governance", "governor"), s.mutatePath(action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			v, e := bind[struct {
				Resource Resource `json:"resource"`
			}](b)
			if e != nil {
				return 0, nil, id, e
			}
			if _, e = governanceWorkspaceWrite(tx, p, id); e != nil {
				return 0, nil, id, e
			}
			if !valid(v.Resource.ID, 128) || v.Resource.Version < 1 {
				return 0, nil, id, invalid
			}
			if !detach {
				if v.Resource.Type != "table_resource" && v.Resource.Type != "file_resource" && v.Resource.Type != "api_resource" {
					return 0, nil, id, problem(409, "GOVERNANCE_EXCHANGE_SOURCE_REQUIRED", "请从共享交换平台登记的表、文件或API资源中选择来源；治理成果不能直接关联为来源")
				}
				record, target, e := metadataTarget(tx, p, v.Resource.ID, false)
				if e != nil {
					return 0, nil, id, e
				}
				if !metadataResourceActive(record) {
					return 0, nil, id, problem(409, "GOVERNANCE_SOURCE_UNAVAILABLE", "这份共享交换资源已停用，请选择可用来源")
				}
				if target.ResourceType != v.Resource.Type || target.SchemaVersion != v.Resource.Version {
					return 0, nil, id, problem(409, "GOVERNANCE_INPUT_CHANGED", "数据结构版本已变化，请重新选择来源")
				}
			}
			row := GovernanceWorkspaceSource{WorkspaceID: id, ResourceType: v.Resource.Type, ResourceID: v.Resource.ID, ContractVersion: v.Resource.Version, ActorID: p.UserID, Active: true}
			if detach {
				var count int64
				if e = tx.Table("governance_versions v").Joins("JOIN governance_definitions d ON d.id=v.definition_id").Where("d.workspace_id=? AND d.kind='dataflow' AND v.state<>'retired' AND EXISTS(SELECT 1 FROM jsonb_array_elements(v.definition->'dataflow'->'nodes') n WHERE n->'source'->'resource'->>'resource_id'=? AND n->'source'->'resource'->>'resource_type'=? AND (n->'source'->'resource'->>'contract_version')::int=?)", id, v.Resource.ID, v.Resource.Type, v.Resource.Version).Count(&count).Error; e != nil {
					return 0, nil, id, e
				}
				if count > 0 {
					return 0, nil, id, problem(409, "GOVERNANCE_WORKSPACE_SOURCE_IN_USE", "本空间还有数据流引用这份来源，请先调整对应数据流")
				}
				if e = tx.Model(&GovernanceWorkspaceSource{}).Where("workspace_id=? AND resource_type=? AND resource_id=? AND contract_version=?", id, v.Resource.Type, v.Resource.ID, v.Resource.Version).Updates(map[string]interface{}{"active": false, "updated_at": time.Now().UTC()}).Error; e != nil {
					return 0, nil, id, e
				}
				row.Active = false
			} else {
				if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "workspace_id"}, {Name: "resource_type"}, {Name: "resource_id"}, {Name: "contract_version"}}, DoUpdates: clause.Assignments(map[string]interface{}{"active": true, "actor_id": p.UserID, "updated_at": time.Now().UTC()})}).Create(&row).Error; e != nil {
					return 0, nil, id, e
				}
			}
			return 200, encode(row), id, nil
		}))
	}
}
