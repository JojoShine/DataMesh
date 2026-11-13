package workflow

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"os"
)

// An internal publication is not an approval to expose metadata anonymously.
const publicAssetPolicy = `EXISTS (SELECT 1 FROM business_records m JOIN business_records mr ON mr.kind='mount_revision' AND mr.id=m.body->>'active_id' JOIN business_records d ON d.kind='directory' AND d.id=m.parent_id JOIN business_records dr ON dr.kind='directory_revision' AND dr.id=d.body->>'published_id' JOIN business_records dict ON dict.kind='directory_dictionary' AND dict.id=dr.body->'definition'->>'dictionary_id' WHERE dict.state='active' AND dict.body->>'verified'='true' AND (dict.body->>'test_only'<>'true' OR ?) AND m.kind='mount' AND m.state='active' AND d.state='published' AND mr.body->'resource'->>'resource_type'='governed_dataset' AND mr.body->'resource'->>'resource_id'=business_records.id AND (mr.body->'resource'->>'contract_version')::int=business_records.version AND mr.body->>'directory_revision_id'=dr.id AND dr.state='approved' AND dr.body->'definition'->>'open_to_public'='true' AND dr.body->'definition'->>'sharing_type'<>'not_shared')`

func publicView(tx *gorm.DB, r Record, detail bool) (json.RawMessage, error) {
	// Expose the reviewed directory text, never unreviewed internal asset notes.
	var revision Record
	e := tx.Table("business_records dr").Select("dr.*").
		Joins("JOIN business_records d ON d.kind='directory' AND d.body->>'published_id'=dr.id").
		Joins("JOIN business_records m ON m.kind='mount' AND m.parent_id=d.id AND m.state='active'").
		Joins("JOIN business_records mr ON mr.kind='mount_revision' AND mr.id=m.body->>'active_id' AND mr.body->>'directory_revision_id'=dr.id").
		Joins("JOIN business_records dict ON dict.kind='directory_dictionary' AND dict.id=dr.body->'definition'->>'dictionary_id'").
		Where("dr.kind='directory_revision' AND dr.state='approved' AND d.state='published' AND dr.body->'definition'->>'open_to_public'='true' AND dr.body->'definition'->>'sharing_type'<>'not_shared' AND dict.state='active' AND dict.body->>'verified'='true' AND (dict.body->>'test_only'<>'true' OR ?) AND mr.body->'resource'->>'resource_type'='governed_dataset' AND mr.body->'resource'->>'resource_id'=? AND (mr.body->'resource'->>'contract_version')::int=?", os.Getenv("DIRECTORY_TEST_MODE") == "1", r.ID, r.Version).Take(&revision).Error
	if e != nil {
		return nil, e
	}
	data, e := decode[DirectoryRevisionData](revision.Body)
	if e != nil {
		return nil, e
	}
	view := gin.H{"dataset_id": r.ID, "name": data.Definition.Name, "description": data.Definition.Description, "provider_department_id": r.DepartmentID, "field_count": len(data.Definition.Items), "usage_restrictions": data.Definition.UsageRestrictions, "contract_version": r.Version, "updated_at": r.UpdatedAt, "resource_type": "governed_dataset"}
	if detail {
		d := data.Definition
		// Only the approved public directory is a source for anonymous details.
		view["details"] = gin.H{"resource_code": d.ResourceCode, "provider_name": d.ProviderName, "classification": d.Classification, "classification_path": d.ClassificationPath, "update_frequency": d.UpdateFrequency, "format": d.Format, "data_layer": d.DataLayer, "items": d.Items, "sharing_type": d.SharingType, "sharing_method": d.SharingMethod, "sharing_conditions": d.SharingConditions, "open_conditions": d.OpenConditions, "security_classification": d.SecurityClassification, "legal_basis": d.LegalBasis}
	}
	return encode(view), nil
}
func (s *Server) publicAssets(c *gin.Context) {
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 12, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind='dataset' AND state='published'").Where(publicAssetPolicy, os.Getenv("DIRECTORY_TEST_MODE") == "1")
	if search := c.Query("search"); search != "" {
		if len(search) > 200 {
			s.fail(c, invalid)
			return
		}
		q = q.Where("(body->'definition'->>'name' ILIKE ? OR body->'definition'->>'description' ILIKE ?)", "%"+search+"%", "%"+search+"%")
	}
	if department := c.Query("department_id"); department != "" {
		if len(department) > 128 {
			s.fail(c, invalid)
			return
		}
		q = q.Where("department_id=?", department)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	var rows []Record
	if e = q.Order("updated_at DESC").Order("id ASC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []json.RawMessage{}
	for _, r := range rows {
		v, err := publicView(s.db.WithContext(c.Request.Context()), r, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		items = append(items, v)
	}
	var departments []string
	if e = s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind='dataset' AND state='published'").Where(publicAssetPolicy, os.Getenv("DIRECTORY_TEST_MODE") == "1").Distinct().Order("department_id").Pluck("department_id", &departments).Error; e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size, "departments": departments})
}
func (s *Server) publicAsset(c *gin.Context) {
	var r Record
	e := s.db.WithContext(c.Request.Context()).Where("kind='dataset' AND id=? AND state='published'", c.Param("id")).Where(publicAssetPolicy, os.Getenv("DIRECTORY_TEST_MODE") == "1").Take(&r).Error
	if e != nil || r.State != "published" {
		identityNotFound(c)
		return
	}
	v, e := publicView(s.db.WithContext(c.Request.Context()), r, true)
	if e != nil {
		s.fail(c, e)
		return
	}
	c.Data(200, "application/json", v)
}
func identityNotFound(c *gin.Context) {
	c.AbortWithStatusJSON(404, gin.H{"code": "NOT_FOUND", "message": "资产不存在或未发布", "trace_id": c.GetString("trace_id")})
}
