package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"gorm.io/gorm/clause"
	"reflect"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GovernanceMetadata struct {
	ObjectID      string                      `json:"object_id"`
	ObservationID string                      `json:"observation_id"`
	Fields        []GovernanceFieldAnnotation `json:"fields"`
}
type GovernanceFieldAnnotation struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Standard    *GovernanceRef `json:"standard,omitempty"`
	Term        *GovernanceRef `json:"term,omitempty"`
}
type MetadataColumn struct {
	Name       string  `json:"name"`
	NativeType string  `json:"native_type"`
	Length     *int64  `json:"length"`
	Precision  *int64  `json:"precision"`
	Scale      *int64  `json:"scale"`
	Collation  string  `json:"collation"`
	Comment    string  `json:"comment,omitempty"`
	Default    *string `json:"default"`
}
type MetadataObservation struct {
	ResourceType    string          `json:"resource_type"`
	ObservationKind string          `json:"observation_kind"`
	ID              string          `json:"id" gorm:"primaryKey"`
	ObjectID        string          `json:"object_id"`
	Version         int             `json:"version"`
	SourceID        string          `json:"source_id"`
	SourceVersion   int             `json:"source_version"`
	ObjectVersion   int             `json:"object_version"`
	Schema          string          `json:"schema"`
	Table           string          `json:"table"`
	Fields          json.RawMessage `json:"fields" gorm:"type:jsonb"`
	Columns         json.RawMessage `json:"columns" gorm:"type:jsonb"`
	Changes         json.RawMessage `json:"changes" gorm:"type:jsonb"`
	MatchesActive   bool            `json:"matches_active"`
	ObservedBy      string          `json:"observed_by"`
	CreatedAt       time.Time       `json:"created_at"`
}

func (MetadataObservation) TableName() string { return "metadata_observations" }

type MetadataResource struct {
	DataLayer       string               `json:"data_layer,omitempty"`
	FieldLabels     map[string]string    `json:"field_labels,omitempty"`
	DisplayName     string               `json:"display_name,omitempty"`
	Description     string               `json:"description,omitempty"`
	ProviderName    string               `json:"provider_name,omitempty"`
	InputParameters []APIParameter       `json:"input_parameters,omitempty"`
	StructuredInput *GovernanceFileInput `json:"structured_input,omitempty"`
	Object
	ResourceType    string                    `json:"resource_type"`
	ObservationKind string                    `json:"observation_kind"`
	Adapter         string                    `json:"adapter"`
	Collection      *MetadataCollectionPolicy `json:"collection,omitempty"`
	PendingChanges  int64                     `json:"pending_changes"`
}

// Return only public contracts. Resource configs can contain credentials, private keys and probe parameters.
func metadataTarget(tx *gorm.DB, p identity.Principal, id string, lock bool) (Record, MetadataResource, error) {
	var row Record
	q := tx.Where("id=? AND kind IN ?", id, []string{"master_snapshot", "object", "dataset", "table_resource", "file_resource", "api_resource"})
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Take(&row).Error; err != nil {
		return row, MetadataResource{}, err
	}
	if row.Kind == "master_snapshot" {
		_, body, e := masterSnapshotRead(tx, p, id)
		return row, MetadataResource{DataLayer: "dwd", ResourceType: "master_snapshot", Adapter: "master_confirmations", ObservationKind: "registered_contract", DisplayName: body.Name, Object: Object{ObjectID: id, QualifiedName: body.Name, SchemaVersion: row.Version, Fields: body.Fields}}, e
	}
	if !department(p, row) {
		return row, MetadataResource{}, denied
	}
	if err := requireResourceCapability(tx, p, Resource{Type: row.Kind, ID: id}, "govern_resource"); err != nil {
		return row, MetadataResource{}, err
	}
	result := MetadataResource{DataLayer: "ods", Object: Object{ObjectID: id, SchemaVersion: row.Version, Fields: []Field{}}, ResourceType: row.Kind, ObservationKind: "registered_contract"}
	switch row.Kind {
	case "object":
		obj, err := decode[Object](row.Body)
		if err != nil {
			return row, result, err
		}
		result.Object = obj
		result.ResourceType = "raw_object"
		result.ObservationKind = "live_schema"
		result.Adapter = "database"
	case "dataset":
		data, err := decode[DatasetData](row.Body)
		if err != nil {
			return row, result, err
		}
		result.DataLayer = data.DataLayer
		result.QualifiedName = data.Definition.Name
		result.Fields = data.Definition.Fields
		result.ResourceType = "governed_dataset"
		result.Adapter = "governed_result"
	default:
		managed, err := decode[ManagedResource](row.Body)
		if err != nil {
			return row, result, err
		}
		result.QualifiedName = managed.Name
		result.DisplayName = managed.Name
		// Use published public directory context; never expose draft review or connection details.
		var context struct {
			Name         string
			Description  string
			ProviderName string
			Items        json.RawMessage
		}
		if err := tx.Table("business_records AS d").
			Select("v.body->'definition'->>'name' AS name, v.body->'definition'->>'description' AS description, v.body->'definition'->>'provider_name' AS provider_name, v.body->'definition'->'items' AS items").
			Joins("JOIN business_records v ON v.kind='directory_revision' AND v.id=d.body->>'published_id' AND v.parent_id=d.id AND v.department_id=d.department_id").
			Where("d.kind='directory' AND d.id=? AND d.department_id=?", row.ParentID, row.DepartmentID).Scan(&context).Error; err != nil {
			return row, result, err
		}
		if context.Name != "" {
			result.DisplayName = context.Name
		}
		if len(context.Items) > 0 {
			items, err := decode[[]DirectoryItem](context.Items)
			if err != nil {
				return row, result, err
			}
			result.FieldLabels = map[string]string{}
			for _, item := range items {
				result.FieldLabels[item.Name] = item.BusinessName
			}
		}
		result.Description = context.Description
		result.ProviderName = context.ProviderName
		result.Fields = managed.Fields
		result.Adapter = managed.Adapter
		if row.Kind == "file_resource" {
			cfg, e := decode[FileConfig](managed.Config)
			if e != nil {
				return row, result, e
			}
			result.StructuredInput = cfg.GovernanceInput
		}
		if row.Kind == "api_resource" {
			cfg, e := decode[APIConfig](managed.Config)
			if e != nil {
				return row, result, e
			}
			result.InputParameters = cfg.Parameters
		}
		if row.Kind == "table_resource" && (managed.Adapter == "database" || managed.Adapter == "source_reader") {
			config, err := decode[TableConfig](managed.Config)
			if err != nil {
				return row, result, err
			}
			result.SourceID = config.SourceID
			result.QualifiedName = config.Schema + "." + config.Table
			result.ObservationKind = "live_schema"
		}
	}
	return row, result, nil
}
func (s *Server) metadataCatalog(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	page, err := intQuery(c, "page", 1, 100000)
	if err != nil {
		s.fail(c, err)
		return
	}
	size, err := intQuery(c, "page_size", 20, 100)
	if err != nil {
		s.fail(c, err)
		return
	}
	kinds := []string{"object", "dataset", "table_resource", "file_resource", "api_resource"}
	candidates := c.GetBool("governance_source_candidates")
	if candidates || workspaceScope(c.Request.Context()).ID != "" {
		kinds = []string{"table_resource", "file_resource", "api_resource"}
	}
	q := tx.Model(&Record{}).Where("kind IN ?", kinds)
	if candidates {
		q = q.Where("state NOT IN ?", []string{"disabled", "archived", "retired", "deleted", "suspended"})
	}
	if !admin(p) {
		q = q.Where("department_id=? AND EXISTS (SELECT 1 FROM business_records a WHERE a.kind='assignment' AND a.state='active' AND a.body->>'user_id'=? AND a.body->>'capability'='govern_resource' AND a.department_id=business_records.department_id AND (a.body->>'resource_id'='*' OR a.body->>'resource_id'=business_records.id) AND (a.body->>'expires_at')::timestamptz>now())", p.DepartmentID, p.UserID)
	}
	if !candidates && (workspaceScope(c.Request.Context()).ID != "" || c.Query("workspace_sources_only") == "true") {
		scope := workspaceScope(c.Request.Context())
		if scope.ID == "" {
			s.fail(c, invalid)
			return
		}
		q = q.Where("EXISTS(SELECT 1 FROM governance_workspace_sources ws WHERE ws.workspace_id=? AND ws.resource_id=business_records.id AND ws.resource_type=business_records.kind AND ws.active)", scope.ID)
	}
	if search := c.Query("search"); search != "" {
		q = q.Where("COALESCE(body->>'qualified_name',body->>'name',body->'definition'->>'name','') ILIKE ?", "%"+search+"%")
	}
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	var rows []Record
	if err = q.Order("created_at DESC,id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		s.fail(c, err)
		return
	}
	policies := []MetadataCollectionPolicy{}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if len(ids) > 0 {
		if err = tx.Where("object_id IN ?", ids).Find(&policies).Error; err != nil {
			s.fail(c, err)
			return
		}
	}
	byID := map[string]MetadataCollectionPolicy{}
	for _, policy := range policies {
		byID[policy.ObjectID] = policy
	}
	pending := []struct {
		ObjectID string
		Count    int64
	}{}
	if len(ids) > 0 {
		if err = tx.Model(&MetadataChangeReview{}).Select("object_id,count(*) AS count").Where("object_id IN ? AND state IN ('pending','deferred')", ids).Group("object_id").Scan(&pending).Error; err != nil {
			s.fail(c, err)
			return
		}
	}
	counts := map[string]int64{}
	for _, entry := range pending {
		counts[entry.ObjectID] = entry.Count
	}
	items := []MetadataResource{}
	for _, r := range rows {
		_, item, err := metadataTarget(tx, p, r.ID, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		if policy, ok := byID[r.ID]; ok {
			item.Collection = &policy
		}
		item.PendingChanges = counts[r.ID]
		items = append(items, item)
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func governanceObservedModel(fields []Field) GovernanceDefinition {
	model := &GovernanceModel{Layer: "logical", Fields: []GovernanceField{}}
	for _, f := range fields {
		field := GovernanceField{Name: f.Name, LogicalType: f.LogicalType, Nullable: f.Nullable, PrimaryKey: f.PrimaryKey, Sensitive: f.Sensitive}
		if f.Precision != nil {
			field.Precision = *f.Precision
		}
		if f.Scale != nil {
			field.Scale = *f.Scale
		}
		model.Fields = append(model.Fields, field)
	}
	return GovernanceDefinition{Model: model}
}
func (s *Server) registerGovernanceMetadata(api, write *gin.RouterGroup) {
	api.GET("/governance/metadata/:id/collection", s.require("governance", "governor"), s.metadataCollectionDetail)
	write.POST("/governance/metadata/:id/collection", s.require("governance", "governor"), s.mutatePath("metadata_collection_policy", s.metadataCollectionSave))
	write.POST("/governance/metadata/:id/collect", s.require("governance", "governor"), s.mutatePath("metadata_collection_queue", s.metadataCollectionQueue))
	write.POST("/governance/metadata/:id/review", s.require("governance", "governor"), s.mutatePath("metadata_change_review", s.metadataReview))
	write.POST("/governance/metadata/:id/structured-contract", s.require("governance", "governor"), s.mutatePath("governance_file_contract", s.governanceFileContract))
	api.GET("/governance/metadata", s.require("governance", "governor"), s.metadataCatalog)
	api.GET("/governance/metadata/:id/contract", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		_, target, e := metadataTarget(tx, principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if scope := workspaceScope(c.Request.Context()); scope.ID != "" {
			if e = governanceWorkspaceReference(tx, principal(c), scope.ID, Resource{Type: target.ResourceType, ID: target.ObjectID, Version: target.SchemaVersion}); e != nil {
				s.fail(c, e)
				return
			}
		}
		c.JSON(200, target)
	})
	api.GET("/governance/metadata/:id/observations/:observation", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		id := c.Param("id")
		if _, _, e := metadataTarget(tx, principal(c), id, false); e != nil {
			s.fail(c, e)
			return
		}
		var row MetadataObservation
		if e := tx.Where("object_id=? AND id=?", id, c.Param("observation")).Take(&row).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, row)
	})
	api.GET("/governance/metadata/:id/observations", s.require("governance", "governor"), s.metadataObservations)
	write.POST("/governance/metadata/:id/refresh", s.require("governance", "governor"), s.mutatePath("governance_refresh_metadata", s.metadataRefresh))
}
func (s *Server) metadataObserve(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	input, err := bind[struct {
		SourceVersion int `json:"source_version"`
		ObjectVersion int `json:"object_version"`
	}](b)
	if err != nil {
		return 0, nil, "", err
	}
	ctx, cancel := context.WithTimeout(tx.Statement.Context, 8*time.Second)
	defer cancel()
	tx = tx.WithContext(ctx)
	record, obj, err := metadataTarget(tx, p, id, true)
	if err != nil {
		return 0, nil, "", err
	}
	if !metadataResourceActive(record) {
		return 0, nil, id, problem(409, "METADATA_RESOURCE_INACTIVE", "来源资源已停用")
	}
	source := record
	if obj.SourceID != "" {
		source, err = get(tx, "source", obj.SourceID, true)
		if err != nil {
			return 0, nil, "", err
		}
	}
	if !metadataResourceActive(source) {
		return 0, nil, id, problem(409, "METADATA_SOURCE_INACTIVE", "数据源已停用")
	}
	if source.Version != input.SourceVersion || record.Version != input.ObjectVersion {
		return 0, nil, "", problem(409, "METADATA_SOURCE_CHANGED", "来源或对象版本已变化，请刷新后重试")
	}
	schema, table := "", obj.QualifiedName
	config := TableConfig{Fields: obj.Fields}
	columns := []MetadataColumn{}
	if obj.ObservationKind == "live_schema" {
		src, e := decode[SourceSecret](source.Body)
		if e != nil {
			return 0, nil, "", e
		}
		parts := strings.SplitN(obj.QualifiedName, ".", 2)
		if len(parts) != 2 {
			return 0, nil, "", invalid
		}
		schema, table = parts[0], parts[1]
		if obj.Adapter == "source_reader" {
			managed, e := decode[ManagedResource](record.Body)
			if e != nil {
				return 0, nil, "", e
			}
			physical, e := decode[TableConfig](managed.Config)
			if e != nil {
				return 0, nil, "", e
			}
			schema, table = physical.Schema, physical.Table
		}
		var bindings []CDCBinding
		if err = tx.Where("object_id=? AND source_id=?", id, source.ID).Limit(1).Find(&bindings).Error; err != nil {
			return 0, nil, "", err
		}
		if len(bindings) > 0 {
			binding := bindings[0]
			schema, table = binding.Schema, binding.Table
			if src.Kind == "oracle" {
				src, err = cdcProbeSource(src, binding.PDB)
				if err != nil {
					return 0, nil, "", err
				}
			}
		}
		var db *sql.DB
		if readerSource(src.Kind) {
			config, err = s.sourceTableSchema(ctx, tx, src, nil, schema, table)
		} else {
			db, e = s.runtime.database(ctx, tx, src)
			if e != nil {
				return 0, nil, "", e
			}
			defer db.Close()
			config, err = discoverTableFields(ctx, db, src, schema, table, 2000)
		}

		if err != nil {
			return 0, nil, "", err
		}
		for i := range config.Fields {
			for _, active := range obj.Fields {
				if active.Name == config.Fields[i].Name && active.Sensitive {
					config.Fields[i].Sensitive = true
				}
			}
		}
		if !readerSource(src.Kind) {
			columns, err = discoverMetadataColumns(ctx, db, src, schema, table)
		}
		if err != nil {
			return 0, nil, "", err
		}
	}
	var previous MetadataObservation
	if err = tx.Where("object_id=?", id).Order("version DESC").Limit(1).Find(&previous).Error; err != nil {
		return 0, nil, "", err
	}
	before := obj.Fields
	if previous.ID != "" {
		before, err = decode[[]Field](previous.Fields)
		if err != nil {
			return 0, nil, "", err
		}
	}
	observationID, err := newID()
	if err != nil {
		return 0, nil, "", err
	}
	changes := governanceChanges("model", governanceObservedModel(before), governanceObservedModel(config.Fields))
	if previous.ID != "" {
		old, err := decode[[]MetadataColumn](previous.Columns)
		if err != nil {
			return 0, nil, "", err
		}
		prior := map[string]MetadataColumn{}
		for _, c := range old {
			prior[c.Name] = c
		}
		for _, c := range columns {
			if a, ok := prior[c.Name]; ok {
				if a.Comment != c.Comment {
					changes = append(changes, GovernanceChange{"field_source_comment_changed", c.Name, false})
				}
				if !reflect.DeepEqual(a.Default, c.Default) {
					changes = append(changes, GovernanceChange{"field_default_changed", c.Name, true})
				}
				oldStructure, newStructure := a, c
				oldStructure.Comment = ""
				newStructure.Comment = ""
				oldStructure.Default = nil
				newStructure.Default = nil
				if !reflect.DeepEqual(oldStructure, newStructure) {
					changes = append(changes, GovernanceChange{"field_database_type_changed", c.Name, true})
				}
			}
		}
	}
	if previous.ID != "" && previous.SourceVersion == source.Version && previous.ObjectVersion == record.Version && metadataSnapshotEqual(previous, config.Fields, columns) {
		return 201, encode(previous), id, nil
	}
	row := MetadataObservation{ResourceType: obj.ResourceType, ObservationKind: obj.ObservationKind, ID: observationID, ObjectID: id, Version: previous.Version + 1, SourceID: source.ID, SourceVersion: source.Version, ObjectVersion: record.Version, Schema: schema, Table: table, Fields: encode(config.Fields), Columns: encode(columns), Changes: encode(changes), MatchesActive: reflect.DeepEqual(obj.Fields, config.Fields), ObservedBy: p.UserID}
	if err = tx.Create(&row).Error; err != nil {
		return 0, nil, "", err
	}
	if len(changes) > 0 || !row.MatchesActive {
		review := MetadataChangeReview{ObservationID: row.ID, ObjectID: id, State: "pending"}
		if err = tx.Create(&review).Error; err != nil {
			return 0, nil, id, err
		}
	}
	return 201, encode(row), id, nil
}
func (s *Server) metadataObservations(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	record, obj, err := metadataTarget(tx, principal(c), c.Param("id"), false)
	if err != nil {
		s.fail(c, err)
		return
	}
	source := record
	if obj.SourceID != "" {
		source, err = get(tx, "source", obj.SourceID, false)
		if err != nil {
			s.fail(c, err)
			return
		}
	}
	page, err := intQuery(c, "page", 1, 100000)
	if err != nil {
		s.fail(c, err)
		return
	}
	size, err := intQuery(c, "page_size", 20, 100)
	if err != nil {
		s.fail(c, err)
		return
	}
	q := tx.Model(&MetadataObservation{}).Where("object_id=?", record.ID)
	var total int64
	if err = q.Count(&total).Error; err != nil {
		s.fail(c, err)
		return
	}
	rows := []MetadataObservation{}
	if err = q.Order("version DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size, "object": obj, "department_id": record.DepartmentID, "source_version": source.Version, "object_version": record.Version})
}
func governanceMetadataReference(tx *gorm.DB, p identity.Principal, department string, v GovernanceMetadata) error {
	record, _, err := metadataTarget(tx, p, v.ObjectID, false)
	if err != nil {
		return err
	}
	if record.DepartmentID != department {
		return denied
	}
	var observation MetadataObservation
	if err = tx.Where("id=? AND object_id=?", v.ObservationID, v.ObjectID).Take(&observation).Error; err != nil {
		return err
	}
	fields, err := decode[[]Field](observation.Fields)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, f := range fields {
		known[f.Name] = true
	}
	for _, f := range v.Fields {
		if !known[f.Name] {
			return problem(400, "METADATA_FIELD_NOT_FOUND", "注释字段不在所选结构快照中，请刷新结构并核对字段")
		}
	}
	return nil
}

func discoverMetadataColumns(ctx context.Context, db *sql.DB, src SourceSecret, schema, table string) ([]MetadataColumn, error) {
	query := `SELECT c.column_name,pg_catalog.format_type(a.atttypid,a.atttypmod),c.character_maximum_length,c.numeric_precision,c.numeric_scale,COALESCE(c.collation_schema||'.'||c.collation_name,''),COALESCE(pg_catalog.col_description(t.oid,a.attnum),''),c.column_default FROM information_schema.columns c JOIN pg_catalog.pg_namespace n ON n.nspname=c.table_schema JOIN pg_catalog.pg_class t ON t.relnamespace=n.oid AND t.relname=c.table_name JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid AND a.attname=c.column_name AND a.attnum>0 AND NOT a.attisdropped WHERE c.table_schema=$1 AND c.table_name=$2 ORDER BY c.ordinal_position`
	if src.Kind == "mysql" {
		query = "SELECT column_name,column_type,character_maximum_length,numeric_precision,numeric_scale,COALESCE(collation_name,''),COALESCE(column_comment,''),column_default FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position"
	}
	if src.Kind == "oracle" {
		// go-ora cannot decode Oracle's descriptor for an untyped NULL column.
		query = "SELECT column_name,data_type,CASE WHEN char_length>0 THEN char_length ELSE NULL END,data_precision,data_scale,CAST(NULL AS VARCHAR2(512)),COALESCE((SELECT comments FROM all_col_comments m WHERE m.owner=c.owner AND m.table_name=c.table_name AND m.column_name=c.column_name),''),CAST(NULL AS VARCHAR2(4000)) FROM all_tab_columns c WHERE owner=:1 AND table_name=:2 ORDER BY column_id"
	}
	rows, err := db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []MetadataColumn{}
	pointer := func(v sql.NullInt64) *int64 {
		if !v.Valid {
			return nil
		}
		n := v.Int64
		return &n
	}
	for rows.Next() {
		var column MetadataColumn
		var length, precision, scale sql.NullInt64
		var collation, comment, defaultValue sql.NullString
		if err = rows.Scan(&column.Name, &column.NativeType, &length, &precision, &scale, &collation, &comment, &defaultValue); err != nil {
			return nil, err
		}
		column.Length = pointer(length)
		column.Precision = pointer(precision)
		column.Scale = pointer(scale)
		column.Collation = collation.String
		column.Comment = comment.String
		if defaultValue.Valid {
			column.Default = &defaultValue.String
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func metadataSnapshotEqual(previous MetadataObservation, fields []Field, columns []MetadataColumn) bool {
	oldFields, e := decode[[]Field](previous.Fields)
	if e != nil {
		return false
	}
	oldColumns, e := decode[[]MetadataColumn](previous.Columns)
	return e == nil && reflect.DeepEqual(oldFields, fields) && reflect.DeepEqual(oldColumns, columns)
}
