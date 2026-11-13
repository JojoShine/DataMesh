package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AssetSavedResource struct {
	ID              string    `json:"id" gorm:"primaryKey"`
	UserID          string    `json:"-"`
	ResourceType    string    `json:"resource_type"`
	ResourceID      string    `json:"resource_id"`
	ContractVersion int       `json:"contract_version"`
	ResourceName    string    `json:"resource_name"`
	DirectoryID     string    `json:"directory_id"`
	Bookmarked      bool      `json:"bookmarked"`
	Following       bool      `json:"following"`
	Fingerprint     string    `json:"-"`
	Available       bool      `json:"available"`
	Observation     int       `json:"-"`
	Version         int       `json:"version"`
	CheckedAt       time.Time `json:"checked_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (AssetSavedResource) TableName() string { return "asset_saved_resources" }

type AssetNotice struct {
	ProductID       string     `json:"product_id"`
	ID              string     `json:"id" gorm:"primaryKey"`
	UserID          string     `json:"-"`
	DedupeKey       string     `json:"-"`
	Category        string     `json:"category"`
	ResourceName    string     `json:"resource_name"`
	ResourceID      string     `json:"resource_id"`
	ContractVersion int        `json:"contract_version"`
	Message         string     `json:"message"`
	ReadAt          *time.Time `json:"read_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (AssetNotice) TableName() string { return "asset_notices" }

func assetPreferenceLock(tx *gorm.DB, user string) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 73621))", user).Error
}
func assetSharedLabel(tx *gorm.DB, snapshot SharingSnapshot) (string, error) {
	r, e := get(tx, "directory_revision", snapshot.DirectoryRevisionID, false)
	if e != nil {
		return "", e
	}
	d, e := decode[DirectoryRevisionData](r.Body)
	return d.Definition.Name, e
}
func assetWatchSnapshot(tx *gorm.DB, ref Resource) (string, bool, error) {
	snapshot, e := resolveActiveSharing(tx, ref, false)
	if e != nil {
		var p *Problem
		if errors.As(e, &p) || errors.Is(e, gorm.ErrRecordNotFound) {
			return "unavailable", false, nil
		}
		return "", false, e
	}
	return string(encode(snapshot)), true, nil
}
func (s *Server) registerAssetFollowing(api, write *gin.RouterGroup) {
	gate := s.require("assets")
	api.GET("/assets/saved-state", gate, func(c *gin.Context) {
		version, e := intQuery(c, "contract_version", 1, 100000000)
		if e != nil {
			s.fail(c, e)
			return
		}
		var row AssetSavedResource
		e = s.db.WithContext(c.Request.Context()).Where("user_id=? AND resource_type=? AND resource_id=? AND contract_version=?", principal(c).UserID, c.Query("resource_type"), c.Query("resource_id"), version).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			c.JSON(200, gin.H{"version": 0, "bookmarked": false, "following": false})
			return
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, row)
	})
	api.GET("/assets/saved", gate, func(c *gin.Context) {
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
		q := s.db.WithContext(c.Request.Context()).Model(&AssetSavedResource{}).Where("user_id=?", principal(c).UserID)
		switch c.Query("scope") {
		case "bookmarks":
			q = q.Where("bookmarked")
		case "following":
			q = q.Where("following")
		case "":
			q = q.Where("bookmarked OR following")
		default:
			s.fail(c, invalid)
			return
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []AssetSavedResource{}
		if e = q.Order("updated_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	api.GET("/assets/notices", gate, func(c *gin.Context) {
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
		q := s.db.WithContext(c.Request.Context()).Model(&AssetNotice{}).Where("user_id=?", principal(c).UserID)
		var unread int64
		if e = q.Session(&gorm.Session{}).Where("read_at IS NULL").Count(&unread).Error; e != nil {
			s.fail(c, e)
			return
		}
		if c.Query("unread") == "true" {
			q = q.Where("read_at IS NULL")
		} else if c.Query("unread") != "" && c.Query("unread") != "false" {
			s.fail(c, invalid)
			return
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []AssetNotice{}
		if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "unread": unread, "page": page, "page_size": size})
	})
	write.POST("/assets/saved", gate, s.mutate("asset_save", s.saveAssetPreference))
	write.POST("/assets/notices/refresh", gate, s.mutate("asset_notice_refresh", s.refreshAssetNotices))
	write.POST("/assets/notices/:id/read", gate, s.mutatePath("asset_notice_read", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if _, e := bind[struct{}](b); e != nil {
			return 0, nil, id, e
		}
		var row AssetNotice
		if e := tx.Where("id=? AND user_id=?", id, p.UserID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error; e != nil {
			return 0, nil, id, e
		}
		if row.ReadAt == nil {
			now := time.Now().UTC()
			row.ReadAt = &now
			if e := tx.Save(&row).Error; e != nil {
				return 0, nil, id, e
			}
		}
		return 200, encode(row), id, nil
	}))
}
func (s *Server) saveAssetPreference(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		Resource    Resource `json:"resource"`
		BaseVersion int      `json:"base_version"`
		Bookmarked  bool     `json:"bookmarked"`
		Following   bool     `json:"following"`
	}](b)
	if e != nil {
		return 0, nil, "", e
	}
	if e = assetPreferenceLock(tx, p.UserID); e != nil {
		return 0, nil, "", e
	}
	var row AssetSavedResource
	e = tx.Where("user_id=? AND resource_type=? AND resource_id=? AND contract_version=?", p.UserID, v.Resource.Type, v.Resource.ID, v.Resource.Version).Take(&row).Error
	fresh := errors.Is(e, gorm.ErrRecordNotFound)
	if e != nil && !fresh {
		return 0, nil, "", e
	}
	if row.Version != v.BaseVersion {
		return 0, nil, row.ID, conflict
	}
	if fresh || (!row.Bookmarked && v.Bookmarked) || (!row.Following && v.Following) {
		if !v.Bookmarked && !v.Following {
			return 0, nil, "", invalid
		}
		var count int64
		if e = tx.Model(&AssetSavedResource{}).Where("user_id=? AND (bookmarked OR following)", p.UserID).Count(&count).Error; e != nil {
			return 0, nil, "", e
		}
		if count >= 100 && !row.Bookmarked && !row.Following {
			return 0, nil, "", problem(409, "ASSET_SAVED_LIMIT", "最多保留 100 份收藏或关注，请先整理不再使用的资产")
		}
		snapshot, err := requireSharingSnapshot(tx, v.Resource, nil)
		if err != nil {
			return 0, nil, "", err
		}
		name, err := assetSharedLabel(tx, snapshot)
		if err != nil {
			return 0, nil, "", err
		}
		if fresh {
			id, err := newID()
			if err != nil {
				return 0, nil, "", err
			}
			row = AssetSavedResource{ID: id, UserID: p.UserID, ResourceType: v.Resource.Type, ResourceID: v.Resource.ID, ContractVersion: v.Resource.Version}
		}
		// Starting a watch establishes today's baseline, not a fictional history.
		row.ResourceName = name
		row.DirectoryID = snapshot.DirectoryID
		if fresh || (!row.Following && v.Following) {
			row.Fingerprint = string(encode(snapshot))
			row.Available = true
			row.CheckedAt = time.Now().UTC()
		}
	}
	row.Bookmarked = v.Bookmarked
	row.Following = v.Following
	row.Version++
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, row.ID, e
	}
	return 200, encode(row), row.ID, nil
}
func createAssetNotice(tx *gorm.DB, n AssetNotice) error {
	id, e := newID()
	if e != nil {
		return e
	}
	n.ID = id
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "dedupe_key"}}, DoNothing: true}).Create(&n).Error
}
func (s *Server) refreshAssetNotices(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, "", e
	}
	if e := assetPreferenceLock(tx, p.UserID); e != nil {
		return 0, nil, "", e
	}
	rows := []AssetSavedResource{}
	if e := tx.Where("user_id=? AND (bookmarked OR following)", p.UserID).Order("id").Find(&rows).Error; e != nil {
		return 0, nil, "", e
	}
	now := time.Now().UTC()
	for _, row := range rows {
		fingerprint, available, e := assetWatchSnapshot(tx, Resource{Type: row.ResourceType, ID: row.ResourceID, Version: row.ContractVersion})
		if e != nil {
			return 0, nil, "", e
		}
		if row.Fingerprint != fingerprint {
			row.Observation++
			if row.Following {
				kind, message := "sharing_changed", "共享目录或挂载依据已变化，请重新核对当前字段、条件与授权。"
				if !available {
					kind, message = "sharing_unavailable", "所关注版本暂不可共享，可能已暂停、下架或契约变化；原授权不能绕过当前共享校验。"
				} else if !row.Available {
					kind, message = "sharing_restored", "所关注版本已重新开放共享，请核对使用条件和现有授权。"
				}
				if e = createAssetNotice(tx, AssetNotice{UserID: p.UserID, DedupeKey: fmt.Sprintf("watch:%s:%d", row.ID, row.Observation), Category: kind, ResourceName: row.ResourceName, ResourceID: row.ResourceID, ContractVersion: row.ContractVersion, Message: message}); e != nil {
					return 0, nil, "", e
				}
			}
		}
		if e := tx.Model(&row).Updates(map[string]interface{}{"fingerprint": fingerprint, "available": available, "observation": row.Observation, "checked_at": now}).Error; e != nil {
			return 0, nil, "", e
		}
	}
	// Expiry is derived from the actual grant, never from a renewable UI label.
	grants := []Record{}
	if e := tx.Where("kind='grant' AND owner_id=? AND state='active' AND body->>'grantee_user_id'=? AND (body->'scope'->>'expires_at')::timestamptz<=?", p.UserID, p.UserID, now.Add(7*24*time.Hour)).Find(&grants).Error; e != nil {
		return 0, nil, "", e
	}
	for _, r := range grants {
		g, e := decode[GrantData](r.Body)
		if e != nil {
			return 0, nil, "", e
		}
		name := "已授权资产"
		if g.Snapshot != nil {
			name, e = assetSharedLabel(tx, *g.Snapshot)
			if e != nil {
				return 0, nil, "", e
			}
		}
		category, message := "grant_expiring", "授权将在 7 天内到期。需要继续使用时，请重新申请并等待审批；本提醒不会延长授权。"
		if !g.Scope.ExpiresAt.After(now) {
			category, message = "grant_expired", "授权已到期。如需继续使用，请核对共享条件并重新申请。"
		}
		if e = createAssetNotice(tx, AssetNotice{UserID: p.UserID, DedupeKey: "grant:" + r.ID + ":" + strconv.FormatInt(g.Scope.ExpiresAt.UnixNano(), 10) + ":" + category, Category: category, ResourceName: name, ResourceID: g.Scope.Resource.ID, ContractVersion: g.Scope.Resource.Version, Message: message}); e != nil {
			return 0, nil, "", e
		}
	}
	return 200, encode(gin.H{"checked_at": now, "checked_resources": len(rows), "checked_grants": len(grants)}), p.UserID, nil
}
