package workflow

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) registerResourceDirectory(api, write *gin.RouterGroup) {
	api.GET("/resource-directories", s.require("exchange"), s.directoryList)
	api.GET("/resource-directories/:id", s.require("exchange"), s.directoryDetail)
	write.POST("/resource-directories", s.require("exchange"), s.mutate("create_directory", s.createDirectory))
	for _, action := range []string{"revisions", "submit", "withdraw-submission", "withdraw"} {
		a := action
		write.POST("/resource-directories/:id/"+a, s.require("exchange"), s.mutatePath("directory_"+a, s.directoryAction(a)))
	}
	api.GET("/directory-dictionaries", s.require("exchange"), func(c *gin.Context) {
		var rows []Record
		if e := s.db.WithContext(c.Request.Context()).Where("kind='directory_dictionary' AND state='active'").Order("created_at,id").Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []json.RawMessage{}
		for _, r := range rows {
			items = append(items, encode(gin.H{"id": r.ID, "version": r.Version, "definition": json.RawMessage(r.Body)}))
		}
		c.JSON(200, gin.H{"items": items})
	})
	write.POST("/directory-dictionaries", s.require("exchange"), s.mutate("configure_dictionary", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		if e := requireResourceCapability(tx, p, Resource{}, "authority_review"); e != nil {
			return 0, nil, "", e
		}
		v, e := bind[DirectoryDictionary](b)
		if e != nil {
			return 0, nil, "", e
		}
		if e = validateDictionary(v); e != nil {
			return 0, nil, "", e
		}
		if v.TestOnly && os.Getenv("DIRECTORY_TEST_MODE") != "1" {
			return 0, nil, "", problem(409, "TEST_DICTIONARY_DISABLED", "测试字典仅可在明确开启的本地验证模式中使用")
		}
		r, e := newRecord("directory_dictionary", p.DepartmentID, p.UserID, "", "active", v)
		if e != nil {
			return 0, nil, "", e
		}
		if e = tx.Create(&r).Error; e != nil {
			return 0, nil, "", e
		}
		return 201, encode(gin.H{"id": r.ID, "version": r.Version, "definition": v}), r.ID, nil
	}))
	api.GET("/directory-responsibles", s.require("exchange"), func(c *gin.Context) {
		var users []identity.User
		q := s.db.WithContext(c.Request.Context()).Where("active=true")
		if !admin(principal(c)) {
			q = q.Where("department_id=?", principal(c).DepartmentID)
		}
		if e := q.Order("username").Find(&users).Error; e != nil {
			s.fail(c, e)
			return
		}
		out := []gin.H{}
		for _, u := range users {
			out = append(out, gin.H{"user_id": u.ID, "username": u.Username, "display_name": u.DisplayName, "department_id": u.DepartmentID})
		}
		c.JSON(200, gin.H{"items": out})
	})
	s.registerDirectoryReview(api, write)
}
func newRecord(kind, dept, owner, parent, state string, body interface{}) (Record, error) {
	id, e := newID()
	return Record{Kind: kind, ID: id, DepartmentID: dept, OwnerID: owner, ParentID: parent, State: state, Version: 1, Body: encode(body)}, e
}
func (s *Server) createDirectory(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	if e := requireResourceCapability(tx, p, Resource{}, "manage_resource"); e != nil {
		return 0, nil, "", e
	}
	v, e := bind[DirectoryDefinition](b)
	if e != nil {
		return 0, nil, "", e
	}
	if e = validateDirectory(v, false); e != nil {
		return 0, nil, "", e
	}
	r, e := newRecord("directory", p.DepartmentID, p.UserID, "", "draft", DirectoryData{})
	if e != nil {
		return 0, nil, "", e
	}
	if e = fillDirectoryMetadata(tx, p.DepartmentID, r.ID, &v, true); e != nil {
		return 0, nil, "", e
	}
	rev, e := newRecord("directory_revision", p.DepartmentID, p.UserID, r.ID, "draft", DirectoryRevisionData{Definition: v})
	if e != nil {
		return 0, nil, "", e
	}
	r.Body = encode(DirectoryData{DraftID: rev.ID})
	if e = tx.Create(&r).Error; e == nil {
		e = tx.Create(&rev).Error
	}
	if e != nil {
		return 0, nil, "", e
	}
	out, e := directoryView(tx, r, p, true)
	return 201, out, r.ID, e
}
func directoryView(tx *gorm.DB, r Record, p identity.Principal, showDraft bool) (json.RawMessage, error) {
	d, e := decode[DirectoryData](r.Body)
	if e != nil {
		return nil, e
	}
	revisionID := d.PublishedID
	if revisionID == "" || (showDraft && department(p, r) && d.DraftID != "") {
		revisionID = d.DraftID
	}
	if revisionID == "" {
		return nil, denied
	}
	rev, e := get(tx, "directory_revision", revisionID, false)
	if e != nil {
		return nil, e
	}
	v, e := decode[DirectoryRevisionData](rev.Body)
	if e != nil {
		return nil, e
	}
	if !department(p, r) {
		v.Definition.DepartmentApprovalRef = ""
		v.Definition.AssessmentRef = ""
		v.Reason = ""
	}
	var mounts []Record
	if e = tx.Where("kind='mount' AND parent_id=? AND state='active'", r.ID).Order("(CASE WHEN body->>'service_variant'='true' THEN 2 WHEN body->>'governance_variant'='true' THEN 1 ELSE 0 END),created_at,id").Find(&mounts).Error; e != nil {
		return nil, e
	}
	resources := []directoryResourceView{}
	for _, mount := range mounts {
		variant, err := directoryMountedResource(tx, r, d.PublishedID, v.Definition, mount)
		if err != nil {
			return nil, err
		}
		resources = append(resources, variant)
	}
	available := false
	var ref *Resource
	sharedFields := []Field{}
	var snap *SharingSnapshot
	if len(resources) > 0 {
		primary := resources[0]
		available, ref, sharedFields, snap = primary.Available, &primary.Resource, primary.Fields, primary.SharingSnapshot
	}
	permissions := map[string]bool{}
	for _, cap := range []string{"manage_resource", "operate_resource"} {
		err := requireResourceCapability(tx, p, Resource{Type: "directory", ID: r.ID}, cap)
		if err != nil {
			if _, ok := err.(*Problem); !ok {
				return nil, err
			}
		}
		permissions[cap] = err == nil
	}
	return encode(gin.H{"directory_id": r.ID, "version": r.Version, "state": r.State, "department_id": r.DepartmentID, "revision_id": rev.ID, "revision_state": rev.State, "published_revision_id": d.PublishedID, "definition": v.Definition, "reason": v.Reason, "available": available, "sharing_snapshot": snap, "permissions": permissions, "resource": ref, "fields": sharedFields, "resources": resources}), nil
}

type directoryResourceView struct {
	Resource        Resource              `json:"resource"`
	Fields          []Field               `json:"fields"`
	Available       bool                  `json:"available"`
	SharingSnapshot *SharingSnapshot      `json:"sharing_snapshot"`
	Name            string                `json:"name,omitempty"`
	Governance      *GovernanceProvenance `json:"governance,omitempty"`
	UpdatedAt       time.Time             `json:"updated_at"`
	DataLayer       string                `json:"data_layer"`
	QualityStatus   string                `json:"quality_status,omitempty"`
}

func directoryMountedResource(tx *gorm.DB, directory Record, publishedID string, definition DirectoryDefinition, mount Record) (directoryResourceView, error) {
	out := directoryResourceView{Fields: []Field{}}
	data, err := decode[MountData](mount.Body)
	if err != nil {
		return out, err
	}
	revision, err := get(tx, "mount_revision", data.ActiveID, false)
	if err != nil {
		return out, err
	}
	v, err := decode[MountRevisionData](revision.Body)
	if err != nil {
		return out, err
	}
	out.Resource = v.Resource
	out.Available = directory.State == "published" && revision.State == "approved" && v.DirectoryRevisionID == publishedID && definition.SharingType != "not_shared"
	record, schema, resourceErr := resource(tx, v.Resource, false)
	if resourceErr != nil {
		if _, ok := resourceErr.(*Problem); !ok {
			return out, resourceErr
		}
		out.Available = false
	} else {
		for _, f := range schema {
			for _, mapped := range v.Mapping {
				if f.Name != mapped {
					continue
				}
				for _, item := range definition.Items {
					if v.Mapping[item.Name] == f.Name && item.Sensitive {
						f.Sensitive = true
					}
				}
				out.Fields = append(out.Fields, f)
				break
			}
		}
	}
	out.Name = definition.Name
	out.UpdatedAt = record.UpdatedAt
	out.DataLayer = definition.DataLayer
	if v.Resource.Type == "api_resource" {
		layer, err := resourceLayer(tx, v.Resource)
		if err != nil {
			return out, err
		}
		out.DataLayer = layer
		managed, err := decode[ManagedResource](record.Body)
		if err != nil {
			return out, err
		}
		out.Name = managed.Name
		if _, err = serviceMountVariant(tx, directory.ID, v.Resource); err != nil {
			if _, ok := err.(*Problem); !ok {
				return out, err
			}
			out.Available = false
		}
	}
	if v.Resource.Type == "governed_dataset" && len(record.Body) > 0 {
		dataset, err := decode[DatasetData](record.Body)
		if err != nil {
			return out, err
		}
		out.Name, out.Governance = dataset.Definition.Name, dataset.Governance
		out.DataLayer = "governed_dataset"
		out.QualityStatus = dataset.QualityStatus
	}
	if out.Available {
		out.SharingSnapshot = &SharingSnapshot{DirectoryID: directory.ID, DirectoryRevisionID: publishedID, MountID: mount.ID, MountRevisionID: revision.ID}
	}
	return out, nil
}

func (s *Server) directoryList(c *gin.Context) {
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
	search := c.Query("search")
	if len(search) > 200 {
		s.fail(c, invalid)
		return
	}
	q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind='directory'")
	if c.GetBool("asset_consumer") {
		if c.Query("scope") != "" {
			s.fail(c, invalid)
			return
		}
		q = q.Where(`EXISTS (SELECT 1 FROM business_records dr JOIN business_records dict ON dict.kind='directory_dictionary' AND dict.id=dr.body->'definition'->>'dictionary_id' AND dict.state='active' AND dict.body->>'verified'='true' JOIN business_records m ON m.kind='mount' AND m.parent_id=business_records.id AND m.state='active' JOIN business_records mr ON mr.kind='mount_revision' AND mr.id=m.body->>'active_id' WHERE dr.kind='directory_revision' AND dr.id=business_records.body->>'published_id' AND dr.state='approved' AND dr.body->'definition'->>'sharing_type'<>'not_shared' AND mr.state='approved' AND mr.body->>'directory_revision_id'=dr.id)`)
	}

	if c.Query("scope") == "mine" {
		if !admin(p) {
			q = q.Where("department_id=?", p.DepartmentID)
		}
	} else {
		q = q.Where("state='published'")
	}
	if resourceID := c.Query("resource_id"); resourceID != "" {
		q = q.Where("EXISTS (SELECT 1 FROM business_records m JOIN business_records mr ON mr.kind='mount_revision' AND mr.parent_id=m.id WHERE m.kind='mount' AND m.parent_id=business_records.id AND mr.body->'resource'->>'resource_id'=?)", resourceID)
	}
	if dept := c.Query("department_id"); dept != "" {
		q = q.Where("department_id=?", dept)
	}
	// Search and filters use the revision actually shown, not unpublished peer edits.
	revisionExpr := "business_records.body->>'published_id'"
	if c.Query("scope") == "mine" {
		revisionExpr = "business_records.body->>'draft_id'"
	}
	if search != "" {
		q = q.Where("EXISTS(SELECT 1 FROM business_records v WHERE v.kind='directory_revision' AND v.id=("+revisionExpr+") AND (v.body->'definition'->>'name' ILIKE ? OR v.body->'definition'->>'resource_code' ILIKE ?))", "%"+search+"%", "%"+search+"%")
	}
	for _, key := range []string{"classification", "sharing_type", "data_layer"} {
		if value := c.Query(key); value != "" {
			if key == "data_layer" && value == "governed_dataset" {
				q = q.Where("(EXISTS(SELECT 1 FROM business_records v WHERE v.kind='directory_revision' AND v.id=(" + revisionExpr + ") AND v.body->'definition'->>'data_layer'='governed_dataset') OR EXISTS(SELECT 1 FROM business_records m JOIN business_records mr ON mr.kind='mount_revision' AND mr.id=m.body->>'active_id' WHERE m.kind='mount' AND m.parent_id=business_records.id AND m.state='active' AND m.body->>'governance_variant'='true' AND mr.state='approved' AND mr.body->>'directory_revision_id'=business_records.body->>'published_id' AND mr.body->'resource'->>'resource_type'='governed_dataset'))")
			} else {
				q = q.Where("EXISTS(SELECT 1 FROM business_records v WHERE v.kind='directory_revision' AND v.id=("+revisionExpr+") AND v.body->'definition'->>?=?)", key, value)
			}
		}
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	var rows []Record
	if e = q.Order("updated_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	out := []json.RawMessage{}
	for _, r := range rows {
		v, err := directoryView(s.db.WithContext(c.Request.Context()), r, p, c.Query("scope") == "mine")
		if err != nil {
			s.fail(c, err)
			return
		}
		out = append(out, v)
	}
	c.JSON(200, gin.H{"items": out, "total": total, "page": page, "page_size": size})
}
func (s *Server) directoryDetail(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	r, e := get(tx, "directory", c.Param("id"), false)
	if e == nil && r.State != "published" && !department(p, r) {
		e = requireResourceCapability(tx, p, Resource{Type: "directory", ID: r.ID}, "authority_review")
	}
	if e != nil {
		s.fail(c, e)
		return
	}
	v, e := directoryView(tx, r, p, c.Query("scope") == "mine")
	if e != nil {
		s.fail(c, e)
		return
	}
	c.Data(200, "application/json", v)
}
func validateDirectoryPublication(tx *gorm.DB, v DirectoryDefinition, dept string) error {
	if e := validateDirectory(v, true); e != nil {
		return e
	}
	r, e := get(tx, "directory_dictionary", v.DictionaryID, false)
	if e != nil {
		return e
	}
	dict, e := decode[DirectoryDictionary](r.Body)
	if e != nil {
		return e
	}
	if r.State != "active" || !dict.Verified || (dict.TestOnly && os.Getenv("DIRECTORY_TEST_MODE") != "1") {
		return problem(409, "DICTIONARY_NOT_VERIFIED", "目录编码字典尚未核验或不适用于当前环境")
	}
	pattern, e := regexp.Compile(dict.CodePattern)
	if e != nil {
		return e
	}
	if !pattern.MatchString(v.ResourceCode) || !slices.Contains(dict.Classifications, v.ClassificationPath) {
		return problem(400, "INVALID_DIRECTORY_CODE", "目录编码或分类路径不符合选定字典")
	}
	var n int64
	if e = tx.Model(&identity.User{}).Where("id=? AND department_id=? AND active=true", v.ResponsibleUserID, dept).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return problem(400, "RESPONSIBLE_USER_REQUIRED", "业务责任人须为本部门有效账号")
	}
	return nil
}
func (s *Server) directoryAction(action string) pathMutation {
	return func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		r, e := get(tx, "directory", id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if e = requireResourceCapability(tx, p, Resource{Type: "directory", ID: id}, "manage_resource"); e != nil {
			return 0, nil, id, e
		}
		data, e := decode[DirectoryData](r.Body)
		if e != nil {
			return 0, nil, id, e
		}
		var v VersionAction
		if action == "revisions" {
			req, err := bind[struct {
				ExpectedVersion int                 `json:"expected_version"`
				Definition      DirectoryDefinition `json:"definition"`
			}](b)
			if err != nil {
				return 0, nil, id, err
			}
			v.ExpectedVersion = req.ExpectedVersion
			old, err := get(tx, "directory_revision", data.DraftID, true)
			if err != nil {
				return 0, nil, id, err
			}
			if slices.Contains([]string{"department_pending", "authority_pending"}, old.State) {
				return 0, nil, id, conflict
			}
			previous, err := decode[DirectoryRevisionData](old.Body)
			if err != nil {
				return 0, nil, id, err
			}
			if req.Definition.ResourceCode == "" && req.Definition.DictionaryID == previous.Definition.DictionaryID {
				req.Definition.ResourceCode = previous.Definition.ResourceCode
			}
			if err = fillDirectoryMetadata(tx, r.DepartmentID, id, &req.Definition, false); err != nil {
				return 0, nil, id, err
			}
			if err = validateDirectory(req.Definition, false); err != nil {
				return 0, nil, id, err
			}
			if data.ResourceCode != "" && data.ResourceCode != req.Definition.ResourceCode {
				return 0, nil, id, problem(409, "RESOURCE_CODE_IMMUTABLE", "已发布资源编码不可修改或复用")
			}
			if r.Version != v.ExpectedVersion {
				return 0, nil, id, conflict
			}
			rev, err := newRecord("directory_revision", r.DepartmentID, p.UserID, id, "draft", DirectoryRevisionData{Definition: req.Definition})
			if err != nil {
				return 0, nil, id, err
			}
			if err = tx.Create(&rev).Error; err != nil {
				return 0, nil, id, err
			}
			data.DraftID = rev.ID
		} else {
			v, e = bind[VersionAction](b)
			if e != nil {
				return 0, nil, id, e
			}
			if r.Version != v.ExpectedVersion {
				return 0, nil, id, conflict
			}
			rev, err := get(tx, "directory_revision", data.DraftID, true)
			if err != nil {
				return 0, nil, id, err
			}
			value, err := decode[DirectoryRevisionData](rev.Body)
			if err != nil {
				return 0, nil, id, err
			}
			switch action {
			case "submit":
				if rev.State != "draft" {
					return 0, nil, id, conflict
				}
				if e = validateDirectoryPublication(tx, value.Definition, r.DepartmentID); e != nil {
					return 0, nil, id, e
				}
				if e = hasReviewer(tx, r.DepartmentID, "department_review", p.UserID, "", r.ID); e != nil {
					return 0, nil, id, e
				}
				value.SubmittedBy = p.UserID
				rev.State = "department_pending"
				if e = createReview(tx, r, rev, "department", p.UserID); e != nil {
					return 0, nil, id, e
				}
			case "withdraw-submission":
				if !slices.Contains([]string{"department_pending", "authority_pending"}, rev.State) || (value.SubmittedBy != p.UserID && !admin(p)) || !valid(v.Reason, 1000) {
					return 0, nil, id, conflict
				}
				if e = cancelReviews(tx, rev.ID); e != nil {
					return 0, nil, id, e
				}
				rev.State = "withdrawn"
				value.Reason = v.Reason
			case "withdraw":
				if r.State != "published" || !valid(v.Reason, 1000) {
					return 0, nil, id, conflict
				}
				r.State = "withdrawn"
				value.Reason = v.Reason
			default:
				return 0, nil, id, invalid
			}
			rev.Body = encode(value)
			rev.Version++
			if e = save(tx, &rev); e != nil {
				return 0, nil, id, e
			}
		}
		r.Body = encode(data)
		r.Version++
		if e = save(tx, &r); e != nil {
			return 0, nil, id, e
		}
		out, e := directoryView(tx, r, p, true)
		return 200, out, id, e
	}
}
