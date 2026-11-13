package workflow

import (
	"encoding/json"
	"errors"
	"math"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type governancePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type governanceViewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}
type governanceLayout struct {
	Revision  int                           `json:"revision"`
	Positions map[string]governancePosition `json:"positions"`
	Viewport  governanceViewport            `json:"viewport"`
}
type governanceLayoutRecord struct {
	UserID            string `gorm:"primaryKey"`
	DefinitionID      string `gorm:"primaryKey"`
	DefinitionVersion int    `gorm:"primaryKey"`
	Revision          int
	Positions         json.RawMessage `gorm:"type:jsonb"`
	Viewport          json.RawMessage `gorm:"type:jsonb"`
	UpdatedAt         time.Time
}

func (governanceLayoutRecord) TableName() string { return "governance_flow_layouts" }
func (s *Server) registerGovernanceFlowLayouts(api, write *gin.RouterGroup) {
	api.GET("/governance/dataflows/:id/versions/:version/layout", s.require("governance", "governor"), func(c *gin.Context) {
		v, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		_, _, e = governanceRead(s.db.WithContext(c.Request.Context()), principal(c), "dataflow", c.Param("id"), v)
		if e != nil {
			s.fail(c, e)
			return
		}
		var row governanceLayoutRecord
		e = s.db.WithContext(c.Request.Context()).Where("user_id=? AND definition_id=? AND definition_version=?", principal(c).UserID, c.Param("id"), v).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			c.JSON(200, governanceLayout{Positions: map[string]governancePosition{}, Viewport: governanceViewport{Zoom: 1}})
			return
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"revision": row.Revision, "positions": row.Positions, "viewport": row.Viewport})
	})
	write.PUT("/governance/dataflows/:id/versions/:version/layout", s.require("governance", "governor"), func(c *gin.Context) {
		version, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		s.mutatePath("save_governance_layout", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			return s.saveGovernanceLayout(tx, p, id, version, b)
		})(c)
	})
}
func (s *Server) saveGovernanceLayout(tx *gorm.DB, p identity.Principal, id string, version int, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[governanceLayout](b)
	if e != nil {
		return 0, nil, id, e
	}
	_, record, e := governanceRead(tx, p, "dataflow", id, version)
	if e != nil {
		return 0, nil, id, e
	}
	def, e := decode[GovernanceDefinition](record.Definition)
	if e != nil {
		return 0, nil, id, e
	}
	if def.Dataflow == nil {
		return 0, nil, id, invalid
	}
	known := map[string]bool{}
	for _, node := range def.Dataflow.Nodes {
		known[node.ID] = true
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 100000 }
	if input.Positions == nil || input.Revision < 0 || input.Revision > 1000000000 || len(input.Positions) > len(known) || !finite(input.Viewport.X) || !finite(input.Viewport.Y) || input.Viewport.Zoom < .1 || input.Viewport.Zoom > 4 || math.IsNaN(input.Viewport.Zoom) {
		return 0, nil, id, invalid
	}
	for node, pos := range input.Positions {
		if !known[node] || !finite(pos.X) || !finite(pos.Y) {
			return 0, nil, id, invalid
		}
	}
	next := governanceLayoutRecord{UserID: p.UserID, DefinitionID: id, DefinitionVersion: version, Revision: input.Revision + 1, Positions: encode(input.Positions), Viewport: encode(input.Viewport), UpdatedAt: time.Now()}
	var result *gorm.DB
	if input.Revision == 0 {
		result = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&next)
	} else {
		result = tx.Model(&governanceLayoutRecord{}).Where("user_id=? AND definition_id=? AND definition_version=? AND revision=?", p.UserID, id, version, input.Revision).Updates(map[string]interface{}{"revision": next.Revision, "positions": next.Positions, "viewport": next.Viewport, "updated_at": next.UpdatedAt})
	}
	if result.Error != nil {
		return 0, nil, id, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, nil, id, problem(409, "GOVERNANCE_LAYOUT_CONFLICT", "画布布局已在其他窗口修改，请刷新布局后再保存")
	}
	input.Revision = next.Revision
	return 200, encode(input), id, nil
}
