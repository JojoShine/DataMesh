package workflow

import (
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func explicitRole(p identity.Principal, roles ...string) bool {
	if admin(p) {
		return true
	}
	for _, r := range roles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}
func capabilityRoles(cap string) []string {
	switch cap {
	case "operate_resource":
		return []string{"operator"}
	case "govern_resource":
		return []string{"governor"}
	case "authority_review":
		return []string{"admin"}
	case "manage_resource", "department_review", "mount_review", "share_approve":
		return []string{"provider"}
	}
	return nil
}
func requireResourceCapability(tx *gorm.DB, p identity.Principal, ref Resource, cap string) error {
	if len(capabilityRoles(cap)) == 0 {
		return invalid
	}
	if admin(p) {
		return nil
	}
	if !explicitRole(p, capabilityRoles(cap)...) {
		return denied
	}
	dept := p.DepartmentID
	resourceIDs := []string{ref.ID}
	if ref.ID != "" {
		kind := ref.Type
		if kind == "raw_object" {
			kind = "object"
		}
		if kind == "governed_dataset" {
			kind = "dataset"
		}
		if !slices.Contains([]string{"object", "dataset", "source", "task", "rule", "directory", "mount", "table_resource", "file_resource", "api_resource"}, kind) {
			return invalid
		}
		r, e := get(tx, kind, ref.ID, false)
		if e != nil {
			return e
		}
		dept = r.DepartmentID
		if (cap == "operate_resource" && (kind == "mount" || kind == "task" || kind == "object" || extendedResource(kind))) || (cap == "govern_resource" && kind == "rule") {
			resourceIDs = append(resourceIDs, r.ParentID)
		}
	}
	var n int64
	q := tx.Model(&Record{}).Where("kind='assignment' AND state='active' AND body->>'user_id'=? AND body->>'capability'=? AND (body->>'expires_at')::timestamptz>now()", p.UserID, cap).
		Where("department_id=? AND (body->>'resource_id'='*' OR body->>'resource_id' IN ?)", dept, resourceIDs)
	if cap == "authority_review" {
		q = tx.Model(&Record{}).Where("kind='assignment' AND state='active' AND body->>'user_id'=? AND body->>'capability'=? AND (body->>'expires_at')::timestamptz>now() AND (department_id='*' OR department_id=?) AND (body->>'resource_id'='*' OR body->>'resource_id'=?)", p.UserID, cap, dept, ref.ID)
	}
	if e := q.Count(&n).Error; e != nil {
		return e
	}
	if n == 0 {
		return problem(403, "RESOURCE_SCOPE_REQUIRED", "当前账号未获派此资源的操作或审核权限")
	}
	return nil
}
func requireProcessingAuthorization(tx *gorm.DB, p identity.Principal, ref Resource, fields []string, purpose string) error {
	if e := securityResourceFrozen(tx, ref); e != nil {
		return e
	}
	if admin(p) {
		return nil
	}
	if !explicitRole(p, "governor", "provider") {
		return denied
	}
	var rows []Record
	if e := tx.Where("kind='processing_authorization' AND state='active' AND body->>'user_id'=? AND body->'resource'->>'resource_id'=? AND (body->>'expires_at')::timestamptz>now()", p.UserID, ref.ID).Find(&rows).Error; e != nil {
		return e
	}
	for _, r := range rows {
		v, e := decode[ProcessingAuthorization](r.Body)
		if e != nil {
			return e
		}
		if v.Resource != ref || v.Purpose != purpose {
			continue
		}
		ok := true
		for _, f := range fields {
			ok = ok && slices.Contains(v.Fields, f)
		}
		if ok {
			return nil
		}
	}
	return problem(403, "PROCESSING_AUTHORIZATION_REQUIRED", "需要匹配来源、字段、用途和期限的加工授权")
}
func (s *Server) registerResourceAccess(api, write *gin.RouterGroup) {
	for _, entry := range []struct{ kind, path string }{{"assignment", "resource-assignments"}, {"processing_authorization", "processing-authorizations"}} {
		kind, path := entry.kind, entry.path
		module := "security"
		if kind == "processing_authorization" {
			module = "exchange"
		}
		api.GET("/"+path, s.require(module), s.accessList(kind))
		write.POST("/"+path, s.require(module), s.mutate("create_"+kind, s.createAccess(kind)))
		write.POST("/"+path+"/:id/revoke", s.require(module), s.mutatePath("revoke_"+kind, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			v, e := bind[VersionAction](b)
			if e != nil || !valid(v.Reason, 1000) {
				return 0, nil, id, invalid
			}
			r, e := get(tx, kind, id, true)
			if e != nil {
				return 0, nil, id, e
			}
			if kind == "assignment" {
				if !admin(p) {
					return 0, nil, id, denied
				}
			} else {
				a, err := decode[ProcessingAuthorization](r.Body)
				if err != nil {
					return 0, nil, id, err
				}
				if err = requireResourceCapability(tx, p, a.Resource, "share_approve"); err != nil {
					return 0, nil, id, err
				}
			}
			if r.State != "active" || r.Version != v.ExpectedVersion {
				return 0, nil, id, conflict
			}
			body, err := decode[map[string]json.RawMessage](r.Body)
			if err != nil {
				return 0, nil, id, err
			}
			body["revocation"] = encode(gin.H{"actor_id": p.UserID, "reason": v.Reason, "revoked_at": time.Now().UTC()})
			r.Body = encode(body)
			r.State = "revoked"
			r.Version++
			if e = save(tx, &r); e != nil {
				return 0, nil, id, e
			}
			return 200, encode(gin.H{"id": id, "state": r.State}), id, nil
		}))
	}
	api.GET("/resource-access", s.require("exchange"), func(c *gin.Context) {
		p := principal(c)
		ref := Resource{Type: c.DefaultQuery("resource_type", "directory"), ID: c.Query("resource_id")}
		out := map[string]bool{}
		for _, cap := range []string{"manage_resource", "operate_resource", "govern_resource", "department_review", "mount_review", "authority_review", "share_approve"} {
			e := requireResourceCapability(s.db.WithContext(c.Request.Context()), p, ref, cap)
			if e != nil {
				if _, ok := e.(*Problem); !ok {
					s.fail(c, e)
					return
				}
			}
			out[cap] = e == nil
		}
		c.JSON(200, out)
	})
}
func (s *Server) accessList(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := principal(c)
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
		q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind=?", kind)
		if !admin(p) {
			q = q.Where("body->>'user_id'=? OR (department_id=? AND EXISTS (SELECT 1 FROM business_records a WHERE a.kind='assignment' AND a.state='active' AND a.body->>'user_id'=? AND a.body->>'capability'='share_approve' AND a.body->>'resource_id'='*' AND a.department_id=business_records.department_id AND (a.body->>'expires_at')::timestamptz>now()))", p.UserID, p.DepartmentID, p.UserID)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		var rows []Record
		if e = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []json.RawMessage{}
		for _, r := range rows {
			items = append(items, encode(gin.H{"id": r.ID, "state": r.State, "version": r.Version, "department_id": r.DepartmentID, "definition": json.RawMessage(r.Body)}))
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	}
}
func (s *Server) createAccess(kind string) mutation {
	return func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		dept := ""
		target := ""
		var body json.RawMessage
		if kind == "assignment" {
			if !admin(p) {
				return 0, nil, "", denied
			}
			v, e := bind[ResourceAssignment](b)
			if e != nil {
				return 0, nil, "", e
			}
			if !valid(v.UserID, 128) || !valid(v.DepartmentID, 128) || !valid(v.ResourceID, 128) || !valid(v.Reason, 1000) || len(capabilityRoles(v.Capability)) == 0 || !v.ExpiresAt.After(time.Now()) || v.ExpiresAt.After(time.Now().AddDate(1, 0, 0)) {
				return 0, nil, "", invalid
			}
			var user identity.User
			if e = tx.Where("id=? AND active=true", v.UserID).Take(&user).Error; e != nil {
				return 0, nil, "", e
			}
			var rs []identity.UserRole
			if e = tx.Where("user_id=?", v.UserID).Find(&rs).Error; e != nil {
				return 0, nil, "", e
			}
			matches := false
			for _, r := range rs {
				matches = matches || slices.Contains(capabilityRoles(v.Capability), r.Role)
			}
			if !matches || (v.Capability != "authority_review" && user.DepartmentID != v.DepartmentID) || (v.DepartmentID == "*" && v.Capability != "authority_review") {
				return 0, nil, "", denied
			}
			dept, target, body = v.DepartmentID, v.UserID, encode(v)
		} else {
			v, e := bind[ProcessingAuthorization](b)
			if e != nil {
				return 0, nil, "", e
			}
			if !valid(v.UserID, 128) || !valid(v.Purpose, 1000) || !valid(v.ApprovalRef, 1000) || !v.ExpiresAt.After(time.Now()) || v.ExpiresAt.After(time.Now().AddDate(1, 0, 0)) || (v.UserID == p.UserID && !admin(p)) || (v.AllowSharing && (!v.AllowDerived || !valid(v.SharingBasis, 2000))) {
				return 0, nil, "", invalid
			}
			r, fields, e := resource(tx, v.Resource, true)
			if e != nil {
				return 0, nil, "", e
			}
			if e = requireResourceCapability(tx, p, v.Resource, "share_approve"); e != nil {
				return 0, nil, "", e
			}
			if !validFields(v.Fields, fields) {
				return 0, nil, "", invalid
			}
			var u identity.User
			if e = tx.Where("id=? AND active=true", v.UserID).Take(&u).Error; e != nil {
				return 0, nil, "", e
			}
			dept, target, body = r.DepartmentID, v.UserID, encode(v)
		}
		id, e := newID()
		if e != nil {
			return 0, nil, "", e
		}
		r := Record{Kind: kind, ID: id, DepartmentID: dept, OwnerID: p.UserID, ParentID: target, State: "active", Version: 1, Body: body}
		if e = tx.Create(&r).Error; e != nil {
			return 0, nil, "", e
		}
		return 201, encode(gin.H{"id": id, "state": "active", "version": 1, "department_id": dept, "definition": body}), id, nil
	}
}
