package workflow

import (
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ApplicationMaterials struct {
	LegalBasis            string `json:"legal_basis"`
	Scenario              string `json:"scenario"`
	Scope                 string `json:"scope"`
	Method                string `json:"method"`
	DepartmentApprovalRef string `json:"department_approval_ref"`
}
type SupplementRecord struct {
	Action    string                `json:"action"`
	ActorID   string                `json:"actor_id"`
	Reason    string                `json:"reason"`
	Materials *ApplicationMaterials `json:"materials,omitempty"`
	At        time.Time             `json:"at"`
}

func validMaterials(v *ApplicationMaterials) bool {
	return v != nil && valid(v.LegalBasis, 2000) && valid(v.Scenario, 2000) && valid(v.Scope, 2000) && slices.Contains([]string{"query", "download", "invoke"}, v.Method) && valid(v.DepartmentApprovalRef, 1000)
}

func requireDatasetProcessingBasis(tx *gorm.DB, r Record, p identity.Principal) error {
	if admin(p) {
		return nil
	}
	var administrators int64
	if err := tx.Table("users u").Joins("JOIN user_roles ur ON ur.user_id=u.id").Where("u.id=? AND u.active=true AND ur.role='admin'", r.OwnerID).Count(&administrators).Error; err != nil {
		return err
	}
	if administrators > 0 {
		return nil
	}
	data, e := decode[DatasetData](r.Body)
	if e != nil {
		return e
	}
	if data.GovernanceRunID != "" {
		return requireGovernanceOutputSharingBasis(tx, r, data)
	}
	obj, e := get(tx, "object", data.Definition.SourceObjectID, false)
	if e != nil {
		return e
	}
	var rows []Record
	if e = tx.Where("kind='processing_authorization' AND state='active' AND body->>'user_id'=? AND body->'resource'->>'resource_id'=? AND (body->>'expires_at')::timestamptz>now()", r.OwnerID, obj.ID).Find(&rows).Error; e != nil {
		return e
	}
	for _, row := range rows {
		v, err := decode[ProcessingAuthorization](row.Body)
		if err != nil {
			return err
		}
		if !v.AllowDerived || !v.AllowSharing || v.SharingBasis == "" || v.Resource.Type != "raw_object" || v.Resource.Version != obj.Version || v.Purpose != "数据治理" {
			continue
		}
		ok := true
		for _, f := range data.Definition.Fields {
			ok = ok && slices.Contains(v.Fields, f.Name)
		}
		if ok {
			return nil
		}
	}
	return problem(403, "DERIVED_SHARING_BASIS_REQUIRED", "治理成果缺少有效的来源加工及成果共享依据")
}
func resolveActiveSharing(tx *gorm.DB, ref Resource, lock bool) (SharingSnapshot, error) {
	var out SharingSnapshot
	if _, _, e := resource(tx, ref, lock); e != nil {
		return out, e
	}
	var mounts []Record
	if e := tx.Where("kind='mount' AND state='active' AND body->>'resource_key'=?", resourceKey(ref)).Find(&mounts).Error; e != nil {
		return out, e
	}
	if len(mounts) != 1 {
		return out, problem(409, "ACTIVE_MOUNT_REQUIRED", "资源尚未完成目录挂载，或共享服务已暂停")
	}
	dir, e := get(tx, "directory", mounts[0].ParentID, lock)
	if e != nil {
		return out, e
	}
	mount, e := get(tx, "mount", mounts[0].ID, lock)
	if e != nil {
		return out, e
	}
	d, e := decode[DirectoryData](dir.Body)
	if e != nil {
		return out, e
	}
	m, e := decode[MountData](mount.Body)
	if e != nil {
		return out, e
	}
	if dir.State != "published" || mount.State != "active" || m.ActiveID == "" {
		return out, problem(409, "SHARING_SUSPENDED", "目录已下架或挂载已暂停")
	}
	mr, e := get(tx, "mount_revision", m.ActiveID, false)
	if e != nil {
		return out, e
	}
	mv, e := decode[MountRevisionData](mr.Body)
	if e != nil {
		return out, e
	}
	if mr.State != "approved" || mv.Resource != ref || mv.DirectoryRevisionID != d.PublishedID {
		return out, problem(409, "SHARING_VERSION_CHANGED", "共享目录、挂载或资源版本已变化，请重新申请")
	}
	dr, e := get(tx, "directory_revision", d.PublishedID, false)
	if e != nil {
		return out, e
	}
	dv, e := decode[DirectoryRevisionData](dr.Body)
	if e != nil {
		return out, e
	}
	if e = validateDirectoryPublication(tx, dv.Definition, dir.DepartmentID); e != nil {
		return out, e
	}
	if dv.Definition.SharingType == "not_shared" {
		return out, problem(403, "RESOURCE_NOT_SHARED", "该资源不予共享")
	}
	if e = validateMountSchema(tx, dir.ID, mv, true); e != nil {
		return out, e
	}
	return SharingSnapshot{DirectoryID: dir.ID, DirectoryRevisionID: dr.ID, MountID: mount.ID, MountRevisionID: mr.ID}, nil
}
func requireSharingSnapshot(tx *gorm.DB, ref Resource, expected *SharingSnapshot) (SharingSnapshot, error) {
	out, e := resolveActiveSharing(tx, ref, true)
	if e != nil {
		return out, e
	}
	if expected != nil && *expected != out {
		return out, problem(409, "SHARING_VERSION_CHANGED", "授权依据的目录或挂载已变化，请重新申请")
	}
	return out, nil
}
func validateSharingFields(tx *gorm.DB, snapshot SharingSnapshot, fields []string) error {
	r, e := get(tx, "mount_revision", snapshot.MountRevisionID, false)
	if e != nil {
		return e
	}
	m, e := decode[MountRevisionData](r.Body)
	if e != nil {
		return e
	}
	allowed := []string{}
	for _, f := range m.Mapping {
		allowed = append(allowed, f)
	}
	for _, f := range fields {
		if !slices.Contains(allowed, f) {
			return problem(403, "FIELD_NOT_SHARED", "请求字段不在当前挂载的共享范围内")
		}
	}
	return nil
}
func (s *Server) registerApplicationSupplements(write *gin.RouterGroup) {
	for _, name := range []string{"request-supplement", "resubmit"} {
		action := name
		gate := s.require("exchange")
		if action == "resubmit" {
			gate = s.requireDataConsumer
		}
		write.POST("/applications/:id/"+action, gate, s.mutatePath("application_"+action, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			initial, e := get(tx, "application", id, false)
			if e != nil {
				return 0, nil, id, e
			}
			data, e := decode[ApplicationData](initial.Body)
			if e != nil {
				return 0, nil, id, e
			}
			if _, e = requireSharingSnapshot(tx, data.Request.Resource, data.Snapshot); e != nil {
				return 0, nil, id, e
			}
			r, e := get(tx, "application", id, true)
			if e != nil {
				return 0, nil, id, e
			}
			data, e = decode[ApplicationData](r.Body)
			if e != nil {
				return 0, nil, id, e
			}
			if action == "request-supplement" {
				v, err := bind[struct {
					Reason string `json:"reason"`
				}](b)
				if err != nil || !valid(v.Reason, 1000) {
					return 0, nil, id, invalid
				}
				if p.UserID == data.ApplicantID && !admin(p) {
					return 0, nil, id, denied
				}
				if e = requireResourceCapability(tx, p, data.Request.Resource, "share_approve"); e != nil {
					return 0, nil, id, e
				}
				if r.State != "submitted" {
					return 0, nil, id, conflict
				}
				r.State = "needs_supplement"
				data.DecisionReason = &v.Reason
				data.Supplements = append(data.Supplements, SupplementRecord{Action: action, ActorID: p.UserID, Reason: v.Reason, At: time.Now().UTC()})
			} else {
				v, err := bind[ApplicationMaterials](b)
				if err != nil || !validMaterials(&v) || DeliveryMethod(v.Method) != resourceMethod(data.Request.Resource) {
					return 0, nil, id, invalid
				}
				if (p.UserID != data.ApplicantID && !admin(p)) || r.State != "needs_supplement" || !data.Request.ExpiresAt.After(time.Now()) {
					return 0, nil, id, conflict
				}
				data.Request.Materials = &v
				data.Supplements = append(data.Supplements, SupplementRecord{Action: action, ActorID: p.UserID, Materials: &v, At: time.Now().UTC()})
				data.DecisionReason = nil
				r.State = "submitted"
			}
			r.Version++
			r.Body = encode(data)
			if e = save(tx, &r); e != nil {
				return 0, nil, id, e
			}
			out, e := s.view(tx.Statement.Context, r)
			return 200, out, id, e
		}))
	}
}

// Directory sensitivity can strengthen, but never weaken, the source policy.
func sharingSensitivity(tx *gorm.DB, snapshot SharingSnapshot, schema []Field) ([]Field, error) {
	dr, err := get(tx, "directory_revision", snapshot.DirectoryRevisionID, false)
	if err != nil {
		return nil, err
	}
	d, err := decode[DirectoryRevisionData](dr.Body)
	if err != nil {
		return nil, err
	}
	mr, err := get(tx, "mount_revision", snapshot.MountRevisionID, false)
	if err != nil {
		return nil, err
	}
	m, err := decode[MountRevisionData](mr.Body)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(schema)
	for _, item := range d.Definition.Items {
		if item.Sensitive {
			for i := range out {
				if out[i].Name == m.Mapping[item.Name] {
					out[i].Sensitive = true
				}
			}
		}
	}
	return out, nil
}
