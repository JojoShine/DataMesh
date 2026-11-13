package workflow

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Consumption is shared by both platforms; maintenance and approval routes retain
// their exchange-only gates and resource capability checks.
func (s *Server) requireDataConsumer(c *gin.Context) {
	p := principal(c)
	if !platform(p, "exchange") && !platform(p, "assets") {
		s.fail(c, denied)
		return
	}
	c.Next()
}

func (s *Server) registerAssets(api, write *gin.RouterGroup) {
	assets := api.Group("/assets", s.require("assets"), func(c *gin.Context) {
		c.Set("asset_consumer", true)
		c.Next()
	})
	assets.GET("/directories", s.directoryList)
	assets.GET("/resource-detail", s.assetDetail)
	s.registerAssetProducts(api, write)
	s.registerAssetFeedback(api, write)
	s.registerAssetFollowing(api, write)
	assets.GET("/applications", s.list("application"))
	assets.GET("/grants", s.list("grant"))
}

// Names come from the approved snapshot used by the original application, even
// when a directory is subsequently renamed or withdrawn.
func assetConsumptionView(tx *gorm.DB, r Record, raw json.RawMessage) (json.RawMessage, error) {
	out, err := decode[map[string]interface{}](raw)
	if err != nil {
		return nil, err
	}
	var snapshot *SharingSnapshot
	var ref Resource
	if r.Kind == "application" {
		data, e := decode[ApplicationData](r.Body)
		if e != nil {
			return nil, e
		}
		snapshot, ref = data.Snapshot, data.Request.Resource
	} else {
		data, e := decode[GrantData](r.Body)
		if e != nil {
			return nil, e
		}
		snapshot, ref = data.Snapshot, data.Scope.Resource
		if _, e = requireSharingSnapshot(tx, ref, snapshot); e != nil {
			var issue *Problem
			if !errors.As(e, &issue) && !errors.Is(e, gorm.ErrRecordNotFound) {
				return nil, e
			}
			out["available"] = false
			out["availability_reason"] = "资源已暂停、版本变化或共享条件失效，请重新发现并申请"
		} else {
			out["available"] = true
		}
	}
	if snapshot != nil {
		revision, e := get(tx, "directory_revision", snapshot.DirectoryRevisionID, false)
		if e != nil {
			return nil, e
		}
		data, e := decode[DirectoryRevisionData](revision.Body)
		if e != nil {
			return nil, e
		}
		out["resource_name"] = data.Definition.Name
	}
	return encode(out), nil
}
