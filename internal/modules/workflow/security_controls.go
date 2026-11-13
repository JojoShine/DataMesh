package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"time"
)

func securityRead(tx *gorm.DB, id string, lock bool) (SecurityControl, SecurityControlVersion, error) {
	var h SecurityControl
	q := tx.Where("id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&h).Error; e != nil {
		return h, SecurityControlVersion{}, e
	}
	var v SecurityControlVersion
	e := tx.Where("control_id=? AND version=?", id, h.LatestVersion).Take(&v).Error
	return h, v, e
}
func securityControlView(h SecurityControl, v SecurityControlVersion) gin.H {
	return gin.H{"id": h.ID, "kind": h.Kind, "owner_id": h.OwnerID, "department_id": h.DepartmentID, "generation": h.Generation, "version": v.Version, "published_version": h.PublishedVersion, "state": v.State, "definition": v.Definition, "created_by": v.CreatedBy, "reviewer_id": v.ReviewerID, "reason": v.Reason, "updated_at": h.UpdatedAt}
}
func (s *Server) registerSecurity(api, write *gin.RouterGroup) {
	g := api.Group("/security", s.require("security", "auditor", "admin"))
	g.GET("/resources", s.securityResources)
	s.registerResourceSecurity(api, write)
	g.GET("/overview", s.securityOverview)
	g.GET("/controls", s.securityControls)
	g.GET("/controls/:id/versions", s.securityVersions)
	g.GET("/controls/:id", func(c *gin.Context) {
		h, v, e := securityRead(s.db.WithContext(c.Request.Context()), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, securityControlView(h, v))
	})
	g.GET("/scans", s.securityScans)
	g.GET("/incidents", s.securityIncidents)
	g.GET("/delivery-proofs", s.securityProofs)
	// Historical own proofs remain readable after withdrawal, without granting data access.
	api.GET("/security/delivery-proofs/:id", s.requireSecurityProofReader, s.securityProofDetail)
	write.POST("/security/delivery-proofs/:id/verify", s.requireSecurityProofReader, s.mutatePath("verify_security_proof", s.securityVerifyProof))
	write.POST("/security/extract-trace", s.requireSecurityProofReader, s.securityExtractTrace)
	api.GET("/assets/security-context", s.requireDataConsumer, s.securityResourceContext)
	api.GET("/security/resource-context", s.require("security", "admin", "auditor"), s.securityResourceContext)
	write.POST("/security/controls", s.require("security", "admin"), s.mutate("create_security_control", s.createSecurityControl))
	for _, a := range []string{"versions", "submit", "approve", "reject", "retire"} {
		action := a
		write.POST("/security/controls/:id/"+a, s.require("security", "admin"), s.mutatePath("security_control_"+a, s.securityControlAction(action)))
	}
	write.POST("/security/scans", s.require("security", "admin"), s.mutate("scan_security_metadata", s.scanSecurityMetadata))
	write.POST("/security/preflight", s.require("security", "admin"), s.mutate("security_preflight", s.securityPreflight))
	write.POST("/security/incidents", s.require("security", "admin"), s.mutate("create_security_incident", s.createSecurityIncident))
	for _, a := range []string{"freeze", "resolve"} {
		action := a
		write.POST("/security/incidents/:id/"+a, s.require("security", "admin"), s.mutatePath("security_incident_"+a, s.securityIncidentAction(action)))
	}
	write.POST("/security/risk-check", s.require("security", "admin"), s.mutate("security_risk_check", s.securityRiskCheck))
}

// Resource owners use the same version workflow, restricted to the selected resource.
func (s *Server) registerResourceSecurity(api, write *gin.RouterGroup) {
	gate := s.require("exchange", "provider", "admin")
	api.GET("/resource-security/controls", gate, func(c *gin.Context) {
		version, e := intQuery(c, "contract_version", 1, 100000000)
		if e != nil {
			s.fail(c, e)
			return
		}
		ref := Resource{Type: c.Query("resource_type"), ID: c.Query("resource_id"), Version: version}
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		if _, _, e = resource(tx, ref, false); e != nil {
			s.fail(c, e)
			return
		}
		if e = requireResourceCapability(tx, p, ref, "manage_resource"); e != nil {
			if e = requireResourceCapability(tx, p, ref, "share_approve"); e != nil {
				s.fail(c, e)
				return
			}
		}
		rows := []SecurityControl{}
		if e = tx.Table("security_controls h").Select("h.*").Joins("JOIN security_control_versions v ON v.control_id=h.id AND v.version=h.latest_version").Where("h.kind='classification' AND v.definition->'resource'=?::jsonb", string(encode(ref))).Order("h.updated_at DESC,h.id").Limit(100).Scan(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []gin.H{}
		for _, h := range rows {
			_, v, err := securityRead(tx, h.ID, false)
			if err != nil {
				s.fail(c, err)
				return
			}
			items = append(items, securityControlView(h, v))
		}
		c.JSON(200, gin.H{"items": items, "total": len(items), "page": 1, "page_size": 100, "can_manage": requireResourceCapability(tx, p, ref, "manage_resource") == nil, "can_review": requireResourceCapability(tx, p, ref, "share_approve") == nil})
	})
	api.GET("/resource-security/controls/:id/versions", gate, s.securityVersions)
	write.POST("/resource-security/controls", gate, s.mutate("create_resource_security_label", s.createSecurityControl))
	for _, a := range []string{"versions", "submit", "approve", "reject", "retire"} {
		write.POST("/resource-security/controls/:id/"+a, gate, s.mutatePath("resource_security_"+a, s.securityControlAction(a)))
	}
}
func securityControlPermission(tx *gorm.DB, p identity.Principal, kind string, ref *Resource, review bool) error {
	if admin(p) {
		return nil
	}
	if kind != "classification" || ref == nil || !platform(p, "exchange") {
		return denied
	}
	cap := "manage_resource"
	if review {
		cap = "share_approve"
	}
	return requireResourceCapability(tx, p, *ref, cap)
}
func (s *Server) securityControls(c *gin.Context) {
	kind := c.Query("kind")
	if !securityKind(kind) {
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
	q := s.db.WithContext(c.Request.Context()).Table("security_controls h").Joins("JOIN security_control_versions v ON v.control_id=h.id AND v.version=h.latest_version").Where("h.kind=?", kind)
	if c.Query("published") == "true" {
		q = s.db.WithContext(c.Request.Context()).Table("security_controls h").Joins("JOIN security_control_versions v ON v.control_id=h.id AND v.version=h.published_version").Where("h.kind=? AND v.state='published'", kind)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	type result struct {
		SecurityControl
		Definition json.RawMessage
		State      string
		Version    int
		ReviewerID string
		CreatedBy  string
		Reason     string
	}
	rows := []result{}
	if e = q.Select("h.*,v.definition,v.state,v.version,v.created_by,v.reviewer_id,v.reason").Order("h.updated_at DESC,h.id").Limit(size).Offset((page - 1) * size).Scan(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []gin.H{}
	for _, r := range rows {
		items = append(items, securityControlView(r.SecurityControl, SecurityControlVersion{Version: r.Version, Definition: r.Definition, State: r.State, ReviewerID: r.ReviewerID, CreatedBy: r.CreatedBy, Reason: r.Reason}))
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) securityValidateRefs(tx *gorm.DB, p identity.Principal, kind string, d SecurityDefinition) error {
	if e := validateSecurityDefinition(kind, d); e != nil {
		return e
	}
	if d.Resource != nil {
		_, schema, e := resource(tx, *d.Resource, false)
		if e != nil {
			return e
		}
		if e := securityValidateRowFilters(d.RowFilters, schema); e != nil {
			return e
		}
		for _, field := range append(slices.Clone(d.MaskFields), d.Field) {
			if field != "" && !slices.ContainsFunc(schema, func(f Field) bool { return f.Name == field }) {
				return invalid
			}
		}
	}
	if d.Template != nil {
		var v SecurityControlVersion
		var h SecurityControl
		if e := tx.Where("id=? AND kind='watermark' AND published_version=?", d.Template.ID, d.Template.Version).Take(&h).Error; e != nil {
			return e
		}
		if e := tx.Where("control_id=? AND version=? AND state='published'", h.ID, d.Template.Version).Take(&v).Error; e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) createSecurityControl(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	input, e := bind[struct {
		Kind       string             `json:"kind"`
		Definition SecurityDefinition `json:"definition"`
	}](b)
	if e != nil {
		return 0, nil, "", e
	}
	if e = securityControlPermission(tx, p, input.Kind, input.Definition.Resource, false); e != nil {
		return 0, nil, "", e
	}
	if e = s.securityValidateRefs(tx, p, input.Kind, input.Definition); e != nil {
		return 0, nil, "", e
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	h := SecurityControl{ID: id, Kind: input.Kind, OwnerID: p.UserID, DepartmentID: p.DepartmentID, Generation: 1, LatestVersion: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	v := SecurityControlVersion{CreatedBy: p.UserID, ControlID: id, Version: 1, State: "draft", Definition: encode(securityNormalizeDefinition(input.Definition)), CreatedAt: time.Now().UTC()}
	if e = tx.Create(&h).Error; e != nil {
		return 0, nil, "", e
	}
	if e = tx.Create(&v).Error; e != nil {
		return 0, nil, "", e
	}
	return 201, encode(securityControlView(h, v)), id, nil
}
func (s *Server) securityControlAction(action string) pathMutation {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[struct {
			ExpectedVersion int                 `json:"expected_version"`
			Definition      *SecurityDefinition `json:"definition"`
			Reason          string              `json:"reason"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		h, v, e := securityRead(tx, id, true)
		if e != nil {
			return 0, nil, "", e
		}
		current, err := decode[SecurityDefinition](v.Definition)
		if err != nil {
			return 0, nil, "", err
		}
		if e = securityControlPermission(tx, p, h.Kind, current.Resource, action == "approve" || action == "reject"); e != nil {
			return 0, nil, "", e
		}
		if input.ExpectedVersion != h.Generation {
			return 0, nil, "", conflict
		}
		if action != "versions" && !valid(input.Reason, 2000) {
			return 0, nil, "", invalid
		}
		switch action {
		case "versions":
			if v.State == "submitted" || input.Definition == nil {
				return 0, nil, "", conflict
			}
			if !admin(p) && (input.Definition.Resource == nil || current.Resource == nil || (input.Definition.Resource.ID != current.Resource.ID || input.Definition.Resource.Type != current.Resource.Type)) {
				return 0, nil, "", denied
			}
			if e = s.securityValidateRefs(tx, p, h.Kind, *input.Definition); e != nil {
				return 0, nil, "", e
			}
			h.LatestVersion++
			v = SecurityControlVersion{CreatedBy: p.UserID, ControlID: id, Version: h.LatestVersion, State: "draft", Definition: encode(securityNormalizeDefinition(*input.Definition)), CreatedAt: time.Now().UTC()}
			if e = tx.Create(&v).Error; e != nil {
				return 0, nil, "", e
			}
		case "submit":
			if v.State != "draft" {
				return 0, nil, "", conflict
			}
			v.State = "submitted"
			v.Reason = input.Reason
		case "approve", "reject":
			if v.CreatedBy == p.UserID {
				return 0, nil, "", denied
			}
			if v.State != "submitted" {
				return 0, nil, "", conflict
			}
			v.ReviewerID = p.UserID
			v.Reason = input.Reason
			if action == "reject" {
				v.State = "rejected"
			} else {
				d, err := decode[SecurityDefinition](v.Definition)
				if err != nil {
					return 0, nil, "", err
				}
				if e = s.securityValidateRefs(tx, p, h.Kind, d); e != nil {
					return 0, nil, "", e
				}
				if h.Kind == "policy" && (d.RequireProof || d.Visible || d.FileMark) {
					if _, _, err = securitySigningKey(""); err != nil {
						return 0, nil, "", err
					}
				}
				v.State = "published"
				h.PublishedVersion = v.Version
			}
		case "retire":
			if h.PublishedVersion == 0 || v.State == "submitted" {
				return 0, nil, "", conflict
			}
			if e = tx.Model(&SecurityControlVersion{}).Where("control_id=? AND version=?", id, h.PublishedVersion).Update("state", "retired").Error; e != nil {
				return 0, nil, "", e
			}
			h.PublishedVersion = 0
			if v.State == "published" {
				v.State = "retired"
			}
		default:
			return 0, nil, "", invalid
		}
		if e = tx.Save(&v).Error; e != nil {
			return 0, nil, "", e
		}
		h.Generation++
		h.UpdatedAt = time.Now().UTC()
		if e = tx.Save(&h).Error; e != nil {
			return 0, nil, "", e
		}
		return 200, encode(securityControlView(h, v)), id, nil
	}
}

func (s *Server) requireSecurityProofReader(c *gin.Context) {
	p := principal(c)
	if platform(p, "security") && role(p, "admin", "auditor") {
		return
	}
	s.requireDataConsumer(c)
}

func (s *Server) securityVersions(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	h, v, e := securityRead(tx, c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	p := principal(c)
	if !platform(p, "security") || !role(p, "admin", "auditor") {
		d, err := decode[SecurityDefinition](v.Definition)
		if err != nil {
			s.fail(c, err)
			return
		}
		if err = securityControlPermission(tx, p, h.Kind, d.Resource, false); err != nil {
			if err = securityControlPermission(tx, p, h.Kind, d.Resource, true); err != nil {
				s.fail(c, err)
				return
			}
		}
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
	rows := []SecurityControlVersion{}
	var total int64
	q := tx.Model(&SecurityControlVersion{}).Where("control_id=?", h.ID)
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	if e = q.Order("version DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}
