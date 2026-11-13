package workflow

import (
	"net/http"
	"strconv"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type etlResourceCapacity struct {
	MaxRows        int    `json:"max_rows"`
	MaxBytes       int64  `json:"max_bytes"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	BatchRows      int    `json:"batch_rows"`
	Adapter        string `json:"adapter"`
}

func (s *Server) resourceCapacity(adapter string, bulk bool) etlResourceCapacity {
	capacity := etlResourceCapacity{MaxRows: s.etl.MaxRows, MaxBytes: s.etl.MaxBytes, TimeoutSeconds: int(s.etl.Timeout.Seconds()), BatchRows: 500, Adapter: adapter}
	if !bulk {
		capacity.MaxRows = min(capacity.MaxRows, 10000)
		capacity.MaxBytes = min(capacity.MaxBytes, 20*1024*1024)
	}
	return capacity
}
func (s *Server) selectedResourceCapacity(tx *gorm.DB, p identity.Principal, ref Resource) (etlResourceCapacity, error) {
	if _, e := s.etlResourceSchema(tx, p, ETLDefinition{SourceResource: &ref, Mapping: []ETLMapping{}}); e != nil {
		return etlResourceCapacity{}, e
	}
	switch ref.Type {
	case "table_resource":
		_, managed, e := managedResource(tx, ref, false)
		if e != nil {
			return etlResourceCapacity{}, e
		}
		if managed.Adapter == "database" {
			physical, e := decode[TableConfig](managed.Config)
			if e != nil {
				return etlResourceCapacity{}, e
			}
			source, e := get(tx, "source", physical.SourceID, false)
			if e != nil {
				return etlResourceCapacity{}, e
			}
			src, e := decode[SourceSecret](source.Body)
			if e != nil {
				return etlResourceCapacity{}, e
			}
			return s.resourceCapacity("database_"+src.Kind, src.Kind == "postgres" || src.Kind == "mysql"), nil
		}
		return s.resourceCapacity("legacy_resource", false), nil
	case "raw_object":
		var task Record
		if e := tx.Where("kind='task' AND body->>'object_id'=?", ref.ID).Take(&task).Error; e != nil {
			return etlResourceCapacity{}, e
		}
		data, e := decode[TaskData](task.Body)
		if e != nil {
			return etlResourceCapacity{}, e
		}
		archive, e := get(tx, "archive", data.ArchiveID, false)
		if e != nil {
			return etlResourceCapacity{}, e
		}
		meta, e := decode[ArchiveData](archive.Body)
		if e != nil {
			return etlResourceCapacity{}, e
		}
		if meta.ManifestVersion == 2 {
			return s.resourceCapacity("raw_snapshot", true), nil
		}
		return s.resourceCapacity("legacy_archive", false), nil
	case "governed_dataset":
		record, e := get(tx, "dataset", ref.ID, false)
		if e != nil {
			return etlResourceCapacity{}, e
		}
		data, e := decode[DatasetData](record.Body)
		if e != nil {
			return etlResourceCapacity{}, e
		}
		if data.GovernanceRunID != "" {
			return s.resourceCapacity("governance_dataflow", true), nil
		}
		if data.ResultBinding != nil {
			return s.resourceCapacity("governed_snapshot", true), nil
		}
		return s.resourceCapacity("legacy_governed", false), nil
	}
	return etlResourceCapacity{}, invalid
}
func (s *Server) etlCapabilities(c *gin.Context) {
	resource := s.resourceCapacity("legacy_resource", false)
	if c.Query("resource_type") != "" || c.Query("resource_id") != "" || c.Query("contract_version") != "" {
		version, e := strconv.Atoi(c.Query("contract_version"))
		if e != nil || version < 1 {
			s.fail(c, invalid)
			return
		}
		ref := Resource{Type: c.Query("resource_type"), ID: c.Query("resource_id"), Version: version}
		if !valid(ref.ID, 128) {
			s.fail(c, invalid)
			return
		}
		e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var err error
			resource, err = s.selectedResourceCapacity(tx, principal(c), ref)
			return err
		})
		if e != nil {
			s.fail(c, e)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"direct": s.resourceCapacity("database", true), "platform_resource": resource, "concurrency": 2, "resource_adapters": gin.H{"database_postgres": s.resourceCapacity("database_postgres", true), "database_mysql": s.resourceCapacity("database_mysql", true), "database_oracle": s.resourceCapacity("database_oracle", false), "raw_snapshot": s.resourceCapacity("raw_snapshot", true), "governed_snapshot": s.resourceCapacity("governed_snapshot", true), "legacy": s.resourceCapacity("legacy_resource", false)}})
}
