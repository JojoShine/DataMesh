package workflow

import (
	"encoding/json"
	"slices"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func hasReviewer(tx *gorm.DB, dept, cap, exclude, secondExclude, resourceID string) error {
	var n int64
	// Administrators can perform every review stage without resource assignments.
	if err := tx.Table("users u").Joins("JOIN user_roles ur ON ur.user_id=u.id").Where("u.active=true AND ur.role='admin'").Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	q := tx.Table("business_records a").Joins("JOIN users u ON u.id=a.body->>'user_id' AND u.active=true").Joins("JOIN user_roles ur ON ur.user_id=u.id").Joins("JOIN user_platform_access up ON up.user_id=u.id AND up.platform='exchange'").Where("a.kind='assignment' AND a.state='active' AND a.body->>'capability'=? AND (a.body->>'resource_id'='*' OR a.body->>'resource_id'=?) AND (a.body->>'expires_at')::timestamptz>now() AND u.id<>? AND u.id<>? AND ur.role IN ?", cap, resourceID, exclude, secondExclude, capabilityRoles(cap))
	if cap == "authority_review" {
		q = q.Where("a.department_id=? OR a.department_id='*'", dept)
	} else {
		q = q.Where("a.department_id=? AND u.department_id=?", dept, dept)
	}
	if e := q.Count(&n).Error; e != nil {
		return e
	}
	if n == 0 {
		return problem(409, "REVIEWER_REQUIRED", "请先配置另一位有效审核人，提交人与审核人必须分离")
	}
	return nil
}
func createReview(tx *gorm.DB, target, revision Record, stage, submitter string) error {
	r, e := newRecord("review", target.DepartmentID, submitter, revision.ID, "pending", ReviewData{TargetKind: target.Kind, TargetID: target.ID, RevisionID: revision.ID, Stage: stage, SubmittedBy: submitter})
	if e != nil {
		return e
	}
	return tx.Create(&r).Error
}
func cancelReviews(tx *gorm.DB, revision string) error {
	return tx.Model(&Record{}).Where("kind='review' AND parent_id=? AND state='pending'", revision).Updates(map[string]interface{}{"state": "withdrawn"}).Error
}
func reviewCapability(stage string) string {
	switch stage {
	case "department":
		return "department_review"
	case "authority":
		return "authority_review"
	case "mount":
		return "mount_review"
	}
	return ""
}
func (s *Server) registerDirectoryReview(api, write *gin.RouterGroup) {
	api.GET("/directory-reviews", s.require("exchange"), s.reviewList)
	for _, action := range []string{"approve", "reject"} {
		a := action
		write.POST("/directory-reviews/:id/"+a, s.require("exchange"), s.mutatePath("review_"+a, s.reviewAction(a)))
	}
}
func (s *Server) reviewList(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
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
	q := tx.Where("kind='review'")
	if stage := c.Query("stage"); stage != "" {
		q = q.Where("body->>'stage'=?", stage)
	}
	if state := c.DefaultQuery("state", "pending"); state != "all" {
		q = q.Where("state=?", state)
	}
	var rows []Record
	if e = q.Order("created_at DESC,id").Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []json.RawMessage{}
	for _, r := range rows {
		v, err := decode[ReviewData](r.Body)
		if err != nil {
			s.fail(c, err)
			return
		}
		err = requireResourceCapability(tx, p, Resource{Type: v.TargetKind, ID: v.TargetID}, reviewCapability(v.Stage))
		if err != nil {
			if _, ok := err.(*Problem); !ok {
				s.fail(c, err)
				return
			}
			continue
		}
		revision, err := get(tx, v.TargetKind+"_revision", v.RevisionID, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		var previous json.RawMessage = json.RawMessage("null")
		target, err := get(tx, v.TargetKind, v.TargetID, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		previousID := ""
		if v.TargetKind == "directory" {
			d, err := decode[DirectoryData](target.Body)
			if err != nil {
				s.fail(c, err)
				return
			}
			previousID = d.PublishedID
		} else {
			d, err := decode[MountData](target.Body)
			if err != nil {
				s.fail(c, err)
				return
			}
			previousID = d.ActiveID
		}
		if previousID != "" && previousID != v.RevisionID {
			old, err := get(tx, v.TargetKind+"_revision", previousID, false)
			if err != nil {
				s.fail(c, err)
				return
			}
			previous = old.Body
		}
		canReview := r.State == "pending" && (admin(p) || v.SubmittedBy != p.UserID)
		if v.Stage == "authority" {
			dv, err := decode[DirectoryRevisionData](revision.Body)
			if err != nil {
				s.fail(c, err)
				return
			}
			canReview = canReview && (admin(p) || dv.DepartmentReviewer != p.UserID)
		}
		items = append(items, encode(gin.H{"review_id": r.ID, "version": r.Version, "state": r.State, "department_id": r.DepartmentID, "definition": v, "snapshot": json.RawMessage(revision.Body), "previous": previous, "created_at": r.CreatedAt, "can_review": canReview}))
	}
	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	c.JSON(200, gin.H{"items": items[start:end], "total": total, "page": page, "page_size": size})
}
func (s *Server) reviewAction(action string) pathMutation {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		req, e := bind[VersionAction](b)
		if e != nil {
			return 0, nil, id, e
		}
		if action == "reject" && !valid(req.Reason, 1000) {
			return 0, nil, id, invalid
		}
		preliminary, e := get(tx, "review", id, false)
		if e != nil {
			return 0, nil, id, e
		}
		v, e := decode[ReviewData](preliminary.Body)
		if e != nil {
			return 0, nil, id, e
		}
		if v.TargetKind == "mount" {
			m, err := get(tx, "mount", v.TargetID, false)
			if err != nil {
				return 0, nil, id, err
			}
			md, err := decode[MountData](m.Body)
			if err != nil {
				return 0, nil, id, err
			}
			mr, err := get(tx, "mount_revision", md.DraftID, false)
			if err != nil {
				return 0, nil, id, err
			}
			mv, err := decode[MountRevisionData](mr.Body)
			if err != nil {
				return 0, nil, id, err
			}
			if _, _, err = resource(tx, mv.Resource, true); err != nil {
				return 0, nil, id, err
			}
			if _, err = get(tx, "directory", m.ParentID, true); err != nil {
				return 0, nil, id, err
			}
		}
		target, e := get(tx, v.TargetKind, v.TargetID, true)
		if e != nil {
			return 0, nil, id, e
		}
		revision, e := get(tx, v.TargetKind+"_revision", v.RevisionID, true)
		if e != nil {
			return 0, nil, id, e
		}
		r, e := get(tx, "review", id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if r.State != "pending" || r.Version != req.ExpectedVersion {
			return 0, nil, id, conflict
		}
		if v.SubmittedBy == p.UserID && !admin(p) {
			return 0, nil, id, problem(403, "SELF_APPROVAL_DENIED", "提交人不能审核自己的提交")
		}
		if e = requireResourceCapability(tx, p, Resource{Type: v.TargetKind, ID: v.TargetID}, reviewCapability(v.Stage)); e != nil {
			return 0, nil, id, e
		}
		if v.TargetKind == "directory" {
			d, err := decode[DirectoryData](target.Body)
			if err != nil {
				return 0, nil, id, err
			}
			rv, err := decode[DirectoryRevisionData](revision.Body)
			if err != nil {
				return 0, nil, id, err
			}
			if d.DraftID != revision.ID {
				return 0, nil, id, conflict
			}
			if (v.Stage == "department" && revision.State != "department_pending") || (v.Stage == "authority" && revision.State != "authority_pending") {
				return 0, nil, id, conflict
			}
			if v.Stage == "authority" && rv.DepartmentReviewer == p.UserID && !admin(p) {
				return 0, nil, id, problem(403, "REVIEW_SEPARATION_REQUIRED", "部门审核与主管审核须由不同人员执行")
			}
			if action == "reject" {
				revision.State = "rejected"
				rv.Reason = req.Reason
			} else if v.Stage == "department" {
				if e = hasReviewer(tx, target.DepartmentID, "authority_review", v.SubmittedBy, p.UserID, target.ID); e != nil {
					return 0, nil, id, e
				}
				rv.DepartmentReviewer = p.UserID
				revision.State = "authority_pending"
				if e = createReview(tx, target, revision, "authority", v.SubmittedBy); e != nil {
					return 0, nil, id, e
				}
			} else {
				if e = validateDirectoryPublication(tx, rv.Definition, target.DepartmentID); e != nil {
					return 0, nil, id, e
				}
				if d.ResourceCode != "" && d.ResourceCode != rv.Definition.ResourceCode {
					return 0, nil, id, conflict
				}
				rv.AuthorityReviewer = p.UserID
				revision.State = "approved"
				d.PublishedID = revision.ID
				d.ResourceCode = rv.Definition.ResourceCode
				target.State = "published"
				target.Body = encode(d)
			}
			revision.Body = encode(rv)
		} else if v.TargetKind == "mount" {
			d, err := decode[MountData](target.Body)
			if err != nil {
				return 0, nil, id, err
			}
			rv, err := decode[MountRevisionData](revision.Body)
			if err != nil {
				return 0, nil, id, err
			}
			if d.DraftID != revision.ID || revision.State != "pending" || v.Stage != "mount" {
				return 0, nil, id, conflict
			}
			if action == "reject" {
				revision.State = "rejected"
				rv.Reason = req.Reason
			} else {
				revision.State = "approved"
				rv.ApprovedBy = p.UserID
			}
			revision.Body = encode(rv)
		} else {
			return 0, nil, id, invalid
		}
		if !slices.Contains([]string{"approve", "reject"}, action) {
			return 0, nil, id, invalid
		}
		r.State = "approved"
		if action == "reject" {
			r.State = "rejected"
		}
		v.ReviewerID = p.UserID
		v.Reason = req.Reason
		r.Body = encode(v)
		r.Version++
		target.Version++
		revision.Version++
		for _, row := range []*Record{&target, &revision, &r} {
			if e = save(tx, row); e != nil {
				return 0, nil, id, e
			}
		}
		return 200, encode(gin.H{"review_id": id, "state": r.State, "version": r.Version}), id, nil
	}
}
