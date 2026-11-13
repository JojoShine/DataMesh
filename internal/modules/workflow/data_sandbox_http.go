package workflow

import (
	"encoding/json"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func sandboxOwner(tx *gorm.DB, p identity.Principal) *gorm.DB {
	q := tx.Where("owner_id=? AND department_id=? AND visitor_scope=?", p.UserID, p.DepartmentID, sandboxVisitorScope(p))
	if p.UserID == "" || p.DepartmentID == "" || p.FullExperience && p.CSRFToken == "" {
		q = q.Where("FALSE")
	}
	return q
}
func sandboxRead(tx *gorm.DB, p identity.Principal, id string, lock bool) (DataSandbox, error) {
	var box DataSandbox
	q := sandboxOwner(tx, p).Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Take(&box).Error; err != nil {
		return box, err
	}
	if box.State != "active" || !box.ExpiresAt.After(time.Now()) {
		return box, conflict
	}
	return box, nil
}
func (s *Server) registerDataSandboxes(api, write *gin.RouterGroup) {
	reads := api.Group("/data-sandboxes", s.require("governance", "governor"))
	writes := write.Group("/data-sandboxes", s.require("governance", "governor"), sandboxMutationScope)
	reads.GET("/:id/tables", s.sandboxTables)
	reads.GET("/:id/tables/:table_id", s.sandboxTableDetail)
	reads.GET("/:id/tables/:table_id/records", s.sandboxTableRecords)
	reads.GET("/:id/tables/:table_id/download", s.sandboxDownload)
	writes.POST("/:id/tables", s.sandboxUpload)
	writes.POST("/:id/tables/:table_id/remove", s.sandboxTableRemove)
	writes.POST("/:id/tables/:table_id/lightweight", s.sandboxReplay("sandbox_handoff", s.sandboxHandoffCreate))
	writes.POST("/:id/files", s.sandboxImportFile)
	reads.GET("/:id/queries", s.sandboxQueries)
	reads.GET("/:id/queries/:query_id", s.sandboxQueryDetail)
	reads.GET("/:id/queries/:query_id/records", s.sandboxQueryRecords)
	writes.POST("/:id/queries", s.sandboxReplay("sandbox_query", s.sandboxQueryCreate))
	writes.POST("/:id/queries/:query_id/cancel", s.sandboxQueryCancel)
	reads.GET("", s.sandboxList)
	reads.GET("/:id", s.sandboxDetail)
	writes.POST("", s.mutate("sandbox_create", s.sandboxCreate))
	writes.POST("/:id/close", s.mutatePath("sandbox_close", s.sandboxClose))
}
func (s *Server) sandboxList(c *gin.Context) {
	page, size, err := lightweightPage(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	q := sandboxOwner(s.db.WithContext(c.Request.Context()), principal(c)).Model(&DataSandbox{})
	var total int64
	items := []DataSandbox{}
	if err = q.Count(&total).Error; err == nil {
		err = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&items).Error
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) sandboxDetail(c *gin.Context) {
	box, err := sandboxRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, box)
}
func (s *Server) sandboxCreate(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &in) != nil || !valid(strings.TrimSpace(in.Name), 100) || p.FullExperience && p.CSRFToken == "" {
		return 0, nil, "", invalid
	}
	// Owner-level serialization makes the four-box quota atomic across sessions.
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "sandbox-owner:"+p.UserID).Error; err != nil {
		return 0, nil, "", err
	}
	var count int64
	if err := tx.Model(&DataSandbox{}).Where("owner_id=? AND state IN ?", p.UserID, []string{"active", "closing"}).Count(&count).Error; err != nil {
		return 0, nil, "", err
	}
	if count >= sandboxMaxActive {
		return 0, nil, "", problem(409, "SANDBOX_LIMIT", "最多保留4个活动沙箱")
	}
	id, err := newID()
	if err != nil {
		return 0, nil, "", err
	}
	now := time.Now().UTC()
	ttl := 7 * 24 * time.Hour
	if p.FullExperience {
		ttl = 24 * time.Hour
	}
	box := DataSandbox{ID: id, OwnerID: p.UserID, DepartmentID: p.DepartmentID, VisitorScope: sandboxVisitorScope(p), Name: strings.TrimSpace(in.Name), State: "active", Version: 1, ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now}
	var owner identity.User
	if err = tx.Where("id=? AND active=true", p.UserID).Take(&owner).Error; err != nil {
		return 0, nil, id, err
	}
	box.OwnerVersion = owner.Version
	if err = tx.Create(&box).Error; err != nil {
		return 0, nil, id, err
	}
	return 201, encode(box), id, nil
}
func (s *Server) sandboxClose(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	var in struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if json.Unmarshal(b, &in) != nil {
		return 0, nil, id, invalid
	}
	box, err := sandboxRead(tx, p, id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if box.Version != in.ExpectedVersion {
		return 0, nil, id, conflict
	}
	box.State = "closing"
	box.Version++
	box.UpdatedAt = time.Now().UTC()
	if err = tx.Save(&box).Error; err != nil {
		return 0, nil, id, err
	}
	return 200, encode(box), id, nil
}
