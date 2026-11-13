package workflow

import (
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) registerExchange(api, write *gin.RouterGroup) {
	write.POST("/applications", s.requireDataConsumer, s.mutate("create_application", s.createApplication))
	for _, action := range []string{"approve", "reject", "withdraw"} {
		a := action
		gate := s.require("exchange")
		if a == "withdraw" {
			gate = s.requireDataConsumer
		}
		write.POST("/applications/:id/"+a, gate, s.mutatePath("application_"+a, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			return s.decide(tx, p, id, b, a)
		}))
	}
	write.POST("/grants/:id/revoke", s.require("exchange"), s.mutatePath("revoke_grant", s.revokeGrant))
}
func resource(tx *gorm.DB, ref Resource, lock bool) (Record, []Field, error) {
	if extendedResource(ref.Type) {
		r, v, e := managedResource(tx, ref, lock)
		return r, v.Fields, e
	}
	kind := "dataset"
	if ref.Type == "raw_object" {
		kind = "object"
	} else if ref.Type != "governed_dataset" {
		return Record{}, nil, invalid
	}
	r, e := get(tx, kind, ref.ID, lock)
	if e != nil {
		return r, nil, e
	}
	if ref.Version != r.Version {
		return r, nil, problem(409, "CONTRACT_VERSION_CHANGED", "资源契约版本已变化")
	}
	if kind == "dataset" {
		v, err := decode[DatasetData](r.Body)
		if err != nil {
			return r, nil, err
		}
		if r.State != "published" {
			return r, nil, problem(409, "DATASET_NOT_PUBLISHED", "数据集未发布或已下架")
		}
		return r, v.Definition.Fields, nil
	}
	v, e := decode[Object](r.Body)
	return r, v.Fields, e
}
func validFields(requested []string, fields []Field) bool {
	if len(requested) == 0 || len(requested) > 100 {
		return false
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f.Name] = true
	}
	seen := map[string]bool{}
	for _, f := range requested {
		if !allowed[f] || seen[f] {
			return false
		}
		seen[f] = true
	}
	return true
}
func (s *Server) createApplication(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[GrantRequest](b)
	if e != nil {
		return 0, nil, "", e
	}
	if v.DepartmentID != p.DepartmentID || !valid(v.Purpose, 1000) || !v.ExpiresAt.After(time.Now()) || v.ExpiresAt.After(time.Now().Add(90*24*time.Hour)) {
		return 0, nil, "", invalid
	}
	if !explicitRole(p, "consumer", "provider") {
		return 0, nil, "", denied
	}
	if grantMethod(v) != resourceMethod(v.Resource) {
		return 0, nil, "", problem(400, "DELIVERY_METHOD_MISMATCH", "申请交付方式与资源不一致")
	}
	snapshot, e := requireSharingSnapshot(tx, v.Resource, nil)
	if e != nil {
		return 0, nil, "", e
	}
	if e = validateSharingFields(tx, snapshot, v.Fields); e != nil {
		return 0, nil, "", e
	}
	r, fields, e := resource(tx, v.Resource, false)
	if e != nil {
		return 0, nil, "", e
	}
	if !validResourceFields(v.Resource, v.Fields, fields) || (v.Resource.Type == "raw_object" && !slices.Contains(v.Fields, "id")) {
		return 0, nil, "", problem(400, "INVALID_FIELDS", "请选择契约内的字段；原始事件授权必须包含主键 id")
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	application := Record{Kind: "application", ID: id, DepartmentID: r.DepartmentID, OwnerID: p.UserID, ParentID: r.ID, State: "submitted", Version: 1, Body: encode(ApplicationData{ApplicantID: p.UserID, Request: v, Snapshot: &snapshot})}
	if e = tx.Create(&application).Error; e != nil {
		return 0, nil, "", e
	}
	out, e := s.view(tx.Statement.Context, application)
	return 201, out, id, e
}
func (s *Server) decide(tx *gorm.DB, p identity.Principal, id string, b []byte, action string) (int, json.RawMessage, string, error) {
	initial, e := get(tx, "application", id, false)
	if e != nil {
		return 0, nil, id, e
	}
	initialData, e := decode[ApplicationData](initial.Body)
	if e != nil {
		return 0, nil, id, e
	}
	if action != "withdraw" {
		if initialData.Snapshot == nil {
			return 0, nil, id, problem(409, "LEGACY_AUTHORIZATION", "旧申请需按目录流程重新提交")
		}
		if _, e = requireSharingSnapshot(tx, initialData.Request.Resource, initialData.Snapshot); e != nil {
			return 0, nil, id, e
		}
	}
	r, e := get(tx, "application", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	v, e := decode[ApplicationData](r.Body)
	if e != nil {
		return 0, nil, id, e
	}
	if r.State != "submitted" {
		return 0, nil, id, conflict
	}
	var reason *string
	if action != "approve" {
		decision, err := bind[struct {
			Reason string `json:"reason"`
		}](b)
		if err != nil || !valid(decision.Reason, 1000) {
			return 0, nil, id, invalid
		}
		reason = &decision.Reason
	} else {
		if _, err := bind[struct{}](b); err != nil {
			return 0, nil, id, err
		}
	}
	if action == "withdraw" {
		if p.UserID != v.ApplicantID && !admin(p) {
			return 0, nil, id, denied
		}
		r.State = "withdrawn"
	} else {
		target, fields, err := resource(tx, v.Request.Resource, true)
		if err != nil {
			return 0, nil, id, err
		}
		if p.UserID == v.ApplicantID && !admin(p) {
			return 0, nil, id, problem(403, "SELF_APPROVAL_DENIED", "申请人不能审批自己的申请")
		}
		if e = requireResourceCapability(tx, p, v.Request.Resource, "share_approve"); e != nil {
			return 0, nil, id, e
		}
		if action == "reject" {
			r.State = "rejected"
		} else {
			if !validMaterials(v.Request.Materials) || grantMethod(v.Request) != resourceMethod(v.Request.Resource) {
				return 0, nil, id, problem(409, "APPLICATION_SUPPLEMENT_REQUIRED", "申请材料不全，请一次性告知补正内容")
			}
			if v.Snapshot == nil {
				return 0, nil, id, conflict
			}
			if e = validateSharingFields(tx, *v.Snapshot, v.Request.Fields); e != nil {
				return 0, nil, id, e
			}
			if !validResourceFields(v.Request.Resource, v.Request.Fields, fields) || time.Now().After(v.Request.ExpiresAt) {
				return 0, nil, id, problem(409, "APPLICATION_EXPIRED", "申请已过期或字段契约已变化")
			}
			var applicant identity.User
			if err = tx.Where("id=? AND active=true AND department_id=?", v.ApplicantID, v.Request.DepartmentID).Take(&applicant).Error; err != nil {
				return 0, nil, id, problem(409, "APPLICANT_CHANGED", "申请人账号或部门已变化，请重新申请")
			}
			grantID, err := newID()
			if err != nil {
				return 0, nil, id, err
			}
			grant := Record{Kind: "grant", ID: grantID, DepartmentID: target.DepartmentID, OwnerID: v.ApplicantID, ParentID: r.ID, State: "active", Version: 1, Body: encode(GrantData{ApplicationID: r.ID, GranteeUserID: v.ApplicantID, Scope: v.Request, ApprovedBy: p.UserID, ApprovedAt: time.Now().UTC(), Snapshot: v.Snapshot})}
			if err = tx.Create(&grant).Error; err != nil {
				return 0, nil, id, err
			}
			r.State = "approved"
		}
	}
	v.DecisionReason = reason
	r.Body = encode(v)
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 200, out, id, e
}
func (s *Server) revokeGrant(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	r, e := get(tx, "grant", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	v, e := decode[GrantData](r.Body)
	if e != nil {
		return 0, nil, id, e
	}
	kind := "dataset"
	if v.Scope.Resource.Type == "raw_object" {
		kind = "object"
	} else if extendedResource(v.Scope.Resource.Type) {
		kind = v.Scope.Resource.Type
	}
	target, e := get(tx, kind, v.Scope.Resource.ID, false)
	if e != nil {
		return 0, nil, id, e
	}
	if e = requireResourceCapability(tx, p, Resource{Type: kind, ID: target.ID}, "share_approve"); e != nil {
		return 0, nil, id, e
	}
	if r.State != "active" {
		return 0, nil, id, conflict
	}
	r.State = "revoked"
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	out, e := s.view(tx.Statement.Context, r)
	return 200, out, id, e
}
