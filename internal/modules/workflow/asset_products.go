package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func assetProductRead(tx *gorm.DB, p identity.Principal, id string, manage, lock bool) (AssetProduct, AssetProductRevision, error) {
	var product AssetProduct
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&product).Error; e != nil {
		return product, AssetProductRevision{}, e
	}
	reviewAccess := assetProductReview(p, product)
	if manage && !(assetProductManage(p, product) || reviewAccess) {
		return product, AssetProductRevision{}, denied
	}
	if !manage && (product.PublishedID == "" || product.State == "retired") {
		return product, AssetProductRevision{}, gorm.ErrRecordNotFound
	}
	revisionID := product.PublishedID
	if manage && product.DraftID != "" {
		revisionID = product.DraftID
	}
	var revision AssetProductRevision
	e := tx.Where("id=? AND product_id=?", revisionID, id).Take(&revision).Error
	return product, revision, e
}
func assetProductView(p identity.Principal, product AssetProduct, revision AssetProductRevision) json.RawMessage {
	certification := "unreviewed"
	if revision.ValidUntil != nil && revision.State == "published" {
		certification = "valid"
		if !revision.ValidUntil.After(time.Now()) {
			certification = "expired"
		}
	}
	return encode(gin.H{"id": product.ID, "department_id": product.DepartmentID, "owner_id": product.OwnerID, "state": product.State, "version": product.Version, "draft_id": product.DraftID, "published_id": product.PublishedID, "revision_id": revision.ID, "revision_state": revision.State, "definition": revision.Definition, "members": revision.Members, "semantics": revision.Semantics, "reviewer_id": revision.ReviewerID, "review_note": revision.ReviewNote, "valid_until": revision.ValidUntil, "certification_status": certification, "replacement_id": product.ReplacementID, "reason": product.Reason, "can_manage": assetProductManage(p, product), "can_review": product.State != "retired" && assetProductReview(p, product) && revision.State == "submitted"})
}

// Consumer metadata is an allowlist snapshot of an approved directory and explicitly
// selected published semantics. Source configs and full governance definitions stay internal.
func (s *Server) assetProductSnapshot(tx *gorm.DB, p identity.Principal, definition AssetProductDefinition) ([]AssetProductMember, AssetProductSemantics, error) {
	members := []AssetProductMember{}
	semantics := AssetProductSemantics{Terms: []AssetProductSemantic{}}
	for _, ref := range definition.Resources {
		evidence, e := s.loadAssetEvidence(tx, ref)
		if e != nil {
			return nil, semantics, e
		}
		if evidence.Notice != "" {
			return nil, semantics, problem(409, "ASSET_PRODUCT_SOURCE_UNAVAILABLE", "所选API来源已不可交付，请先恢复来源或选择其他共享资源")
		}
		members = append(members, AssetProductMember{Resource: ref, Name: evidence.Name, Fields: evidence.Fields})
	}
	load := func(kind string, ref GovernanceRef) (AssetProductSemantic, error) {
		head, v, e := governanceRead(tx, p, kind, ref.ID, ref.Version)
		if e != nil {
			return AssetProductSemantic{}, e
		}
		if v.State != "published" {
			return AssetProductSemantic{}, problem(409, "PUBLISHED_SEMANTIC_REQUIRED", "请选择有权读取的已发布业务域或术语版本")
		}
		if kind == "term" && head.WorkspaceID != nil {
			if _, e = governanceWorkspaceRead(tx, p, *head.WorkspaceID, false); e != nil {
				return AssetProductSemantic{}, e
			}
		}
		d, e := decode[GovernanceDefinition](v.Definition)
		if e != nil {
			return AssetProductSemantic{}, e
		}
		return AssetProductSemantic{ID: ref.ID, Version: ref.Version, Name: d.Name, Description: d.Description}, nil
	}
	if definition.Domain != nil {
		d, e := load("domain", *definition.Domain)
		if e != nil {
			return nil, semantics, e
		}
		semantics.Domain = &d
	}
	for _, ref := range definition.Terms {
		term, e := load("term", ref)
		if e != nil {
			return nil, semantics, e
		}
		semantics.Terms = append(semantics.Terms, term)
	}
	return members, semantics, nil
}
func (s *Server) registerAssetProducts(api, write *gin.RouterGroup) {
	read := api.Group("/assets", s.require("assets"))
	read.GET("/products", s.assetProductList)
	read.GET("/products/:id", func(c *gin.Context) {
		manage, e := assetManageQuery(c)
		if e != nil {
			s.fail(c, e)
			return
		}
		p, r, e := assetProductRead(s.db.WithContext(c.Request.Context()), principal(c), c.Param("id"), manage, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.Data(200, "application/json", assetProductView(principal(c), p, r))
	})
	read.GET("/product-semantics", s.require("assets", "provider", "governor"), s.assetProductSemantics)
	read.GET("/products/:id/checks", s.assetProductChecks)
	read.GET("/usage", s.assetUsage)
	write.POST("/assets/products", s.require("assets", "provider", "governor"), s.mutate("create_asset_product", s.createAssetProduct))
	for _, action := range []string{"revisions", "submit", "approve", "reject", "deprecate", "retire"} {
		a := action
		write.POST("/assets/products/:id/"+a, s.require("assets", "provider", "governor"), s.mutatePath("asset_product_"+a, s.assetProductAction(a)))
	}
	write.POST("/assets/products/:id/check", s.require("assets"), s.mutatePath("check_asset_product", s.checkAssetProduct))
}
func assetManageQuery(c *gin.Context) (bool, error) {
	switch c.Query("manage") {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, invalid
	}
}
func (s *Server) assetProductList(c *gin.Context) {
	manage, e := assetManageQuery(c)
	if e != nil {
		s.fail(c, e)
		return
	}
	p := principal(c)
	if manage && !assetProductManager(p) {
		s.fail(c, denied)
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
	search := c.Query("search")
	if len(search) > 200 {
		s.fail(c, invalid)
		return
	}
	tx := s.db.WithContext(c.Request.Context())
	q := tx.Table("asset_products p").Joins("JOIN asset_product_revisions r ON r.id=CASE WHEN ? AND p.draft_id<>'' THEN p.draft_id ELSE p.published_id END", manage)
	if manage {
		if !admin(p) {
			q = q.Where("p.department_id=?", p.DepartmentID)
			if !role(p, "governor") {
				q = q.Where("p.owner_id=?", p.UserID)
			}
		}
	} else {
		q = q.Where("p.published_id<>'' AND p.state IN ('published','deprecated')")
	}
	if search != "" {
		q = q.Where("(r.definition->>'name' ILIKE ? OR r.definition->>'description' ILIKE ?)", "%"+search+"%", "%"+search+"%")
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	rows := []AssetProduct{}
	if e = q.Select("p.*").Order("p.updated_at DESC,p.id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	// Load the page's immutable revision snapshots together instead of querying per product.
	ids := []string{}
	for _, row := range rows {
		id := row.PublishedID
		if manage && row.DraftID != "" {
			id = row.DraftID
		}
		ids = append(ids, id)
	}
	revisions := []AssetProductRevision{}
	if len(ids) > 0 {
		if e = tx.Where("id IN ?", ids).Find(&revisions).Error; e != nil {
			s.fail(c, e)
			return
		}
	}
	indexed := map[string]AssetProductRevision{}
	for _, r := range revisions {
		indexed[r.ID] = r
	}
	items := []json.RawMessage{}
	for i, row := range rows {
		revision, ok := indexed[ids[i]]
		if !ok {
			s.fail(c, gorm.ErrRecordNotFound)
			return
		}
		items = append(items, assetProductView(p, row, revision))
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) createAssetProduct(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	def, e := bind[AssetProductDefinition](b)
	if e != nil {
		return 0, nil, "", e
	}
	if e = validateAssetProduct(def); e != nil {
		return 0, nil, "", e
	}
	if def.Terms == nil {
		def.Terms = []GovernanceRef{}
	}
	members, semantics, e := s.assetProductSnapshot(tx, p, def)
	if e != nil {
		return 0, nil, "", e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	revisionID, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	now := time.Now().UTC()
	product := AssetProduct{ID: id, DepartmentID: p.DepartmentID, OwnerID: p.UserID, State: "draft", Version: 1, DraftID: revisionID, CreatedAt: now, UpdatedAt: now}
	if e = tx.Create(&product).Error; e != nil {
		return 0, nil, "", e
	}
	revision := AssetProductRevision{ID: revisionID, ProductID: id, State: "draft", Definition: encode(def), Members: encode(members), Semantics: encode(semantics), CreatedAt: now}
	if e = tx.Create(&revision).Error; e != nil {
		return 0, nil, "", e
	}
	return 201, assetProductView(p, product, revision), id, nil
}

type assetProductActionInput struct {
	ExpectedVersion int                     `json:"expected_version"`
	Definition      *AssetProductDefinition `json:"definition"`
	Reason          string                  `json:"reason"`
	ValidUntil      *time.Time              `json:"valid_until"`
	ReplacementID   string                  `json:"replacement_id"`
}

func (s *Server) assetProductAction(action string) pathMutation {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[assetProductActionInput](b)
		if e != nil {
			return 0, nil, "", e
		}
		product, revision, e := assetProductRead(tx, p, id, true, true)
		if e != nil {
			return 0, nil, "", e
		}
		if v.ExpectedVersion != product.Version {
			return 0, nil, "", conflict
		}
		if action == "approve" || action == "reject" {
			if !assetProductReview(p, product) {
				return 0, nil, "", denied
			}
		} else if !assetProductManage(p, product) {
			return 0, nil, "", denied
		}
		if product.State == "retired" {
			return 0, nil, "", conflict
		}
		if action != "revisions" && !valid(v.Reason, 2000) {
			return 0, nil, "", invalid
		}
		now := time.Now().UTC()
		switch action {
		case "revisions":
			if v.Definition == nil || validateAssetProduct(*v.Definition) != nil {
				return 0, nil, "", invalid
			}
			if revision.State == "submitted" {
				return 0, nil, "", conflict
			}
			if v.Definition.Terms == nil {
				v.Definition.Terms = []GovernanceRef{}
			}
			members, semantics, err := s.assetProductSnapshot(tx, p, *v.Definition)
			if err != nil {
				return 0, nil, "", err
			}
			revisionID, err := newID()
			if err != nil {
				return 0, nil, "", err
			}
			revision = AssetProductRevision{ID: revisionID, ProductID: id, State: "draft", Definition: encode(v.Definition), Members: encode(members), Semantics: encode(semantics), CreatedAt: now}
			if e = tx.Create(&revision).Error; e != nil {
				return 0, nil, "", e
			}
			product.DraftID = revision.ID
		case "submit":
			if product.DraftID == "" || revision.State != "draft" {
				return 0, nil, "", conflict
			}
			revision.State = "submitted"
			revision.ReviewNote = v.Reason
		case "approve":
			if product.DraftID == "" || revision.State != "submitted" {
				return 0, nil, "", conflict
			}
			if v.ValidUntil == nil || !v.ValidUntil.After(now) || v.ValidUntil.After(now.Add(366*24*time.Hour)) {
				return 0, nil, "", invalid
			}
			definition, err := decode[AssetProductDefinition](revision.Definition)
			if err != nil {
				return 0, nil, "", err
			}
			members, semantics, err := s.assetProductSnapshot(tx, p, definition)
			if err != nil {
				return 0, nil, "", err
			}
			revision.Members = encode(members)
			revision.Semantics = encode(semantics)
			revision.State = "published"
			revision.ReviewerID = p.UserID
			revision.ReviewNote = v.Reason
			revision.ValidUntil = v.ValidUntil
			product.PublishedID = revision.ID
			product.DraftID = ""
			product.State = "published"
			product.ReplacementID = ""
			product.Reason = ""
		case "reject":
			if product.DraftID == "" || revision.State != "submitted" {
				return 0, nil, "", conflict
			}
			revision.State = "rejected"
			revision.ReviewerID = p.UserID
			revision.ReviewNote = v.Reason
		case "deprecate":
			if product.State != "published" || revision.State == "submitted" {
				return 0, nil, "", conflict
			}
			if v.ReplacementID != "" {
				if v.ReplacementID == id || !valid(v.ReplacementID, 64) {
					return 0, nil, "", invalid
				}
				var replacement AssetProduct
				if e = tx.Where("id=? AND state='published' AND published_id<>''", v.ReplacementID).Take(&replacement).Error; e != nil {
					return 0, nil, "", e
				}
			}
			product.State = "deprecated"
			product.ReplacementID = v.ReplacementID
			product.Reason = v.Reason
		case "retire":
			if product.State != "deprecated" {
				return 0, nil, "", conflict
			}
			product.State = "retired"
			product.Reason = v.Reason
		default:
			return 0, nil, "", invalid
		}
		// Published definitions are never edited; only the draft or its review metadata changes.
		if action != "deprecate" && action != "retire" {
			if e = tx.Save(&revision).Error; e != nil {
				return 0, nil, "", e
			}
		}
		product.Version++
		product.UpdatedAt = now
		if e = tx.Save(&product).Error; e != nil {
			return 0, nil, "", e
		}
		return 200, assetProductView(p, product, revision), id, nil
	}
}
func assetProductExpectedError(err error) bool {
	var p *Problem
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.As(err, &p)
}
func (s *Server) assetProductSemantics(c *gin.Context) {
	kind := c.Query("kind")
	if kind != "domain" && kind != "term" {
		s.fail(c, invalid)
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
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 200 {
		s.fail(c, invalid)
		return
	}
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	q := tx.Table("governance_definitions d").Joins("JOIN governance_versions v ON v.definition_id=d.id").Where("d.kind=? AND v.state='published'", kind)
	if !admin(p) {
		q = q.Where("d.department_id=? OR (d.kind='domain' AND v.definition->>'domain_scope'='enterprise')", p.DepartmentID)
		// Published department semantics follow the existing department workspace read boundary.
	}
	if search != "" {
		q = q.Where("v.definition->>'name' ILIKE ?", "%"+search+"%")
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []AssetProductSemantic{}
	if e = q.Select("v.definition_id AS id,v.version,v.definition->>'name' AS name,v.definition->>'description' AS description").Order("d.created_at DESC,d.id,v.version DESC").Limit(size).Offset((page - 1) * size).Scan(&items).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
