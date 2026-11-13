package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SourceTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}
type TableConfig struct {
	SourceID string   `json:"source_id"`
	Schema   string   `json:"schema"`
	Table    string   `json:"table"`
	Fields   []Field  `json:"fields"`
	Keys     []string `json:"keys"`
	BatchID  string   `json:"batch_id,omitempty"`
}
type TableResourceInput struct {
	DirectoryID     string `json:"directory_id"`
	SourceID        string `json:"source_id"`
	Schema          string `json:"schema"`
	Table           string `json:"table"`
	ExpectedVersion int    `json:"expected_directory_version"`
}

func quoteIdentifier(kind, value string) string {
	if kind == "mysql" {
		return "`" + strings.ReplaceAll(value, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
func sqlLogicalType(v string) string {
	switch strings.ToLower(v) {
	case "smallint", "integer", "bigint", "int", "tinyint", "mediumint":
		return "integer"
	case "numeric", "decimal", "real", "double precision", "double", "float":
		return "decimal"
	case "boolean", "bool":
		return "boolean"
	case "date":
		return "date"
	case "timestamp", "timestamp without time zone", "timestamp with time zone", "datetime":
		return "datetime"
	case "text", "character varying", "character", "varchar", "char", "longtext", "mediumtext", "tinytext", "uuid":
		return "string"
	}
	return "unsupported"
}
func discoverTables(ctx context.Context, db *sql.DB, src SourceSecret) ([]SourceTable, error) {
	q := "SELECT table_schema,table_name,table_type FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema,table_name LIMIT 1000"
	args := []interface{}{}
	if src.Kind == "mysql" {
		q = "SELECT table_schema,table_name,table_type FROM information_schema.tables WHERE table_schema=? ORDER BY table_name LIMIT 1000"
		args = append(args, src.Database)
	}
	if src.Kind == "oracle" {
		q = "SELECT * FROM (SELECT owner,table_name,'TABLE' FROM all_tables WHERE owner NOT IN ('SYS','SYSTEM','XDB','MDSYS','CTXSYS') UNION ALL SELECT owner,view_name,'VIEW' FROM all_views WHERE owner NOT IN ('SYS','SYSTEM','XDB','MDSYS','CTXSYS') ORDER BY 1,2) WHERE ROWNUM <= 1000"
	}
	rows, e := db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []SourceTable{}
	for rows.Next() {
		var v SourceTable
		if e = rows.Scan(&v.Schema, &v.Name, &v.Kind); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func discoverTable(ctx context.Context, db *sql.DB, src SourceSecret, schema, table string) (TableConfig, error) {
	return discoverTableFields(ctx, db, src, schema, table, 100)
}
func discoverTableFields(ctx context.Context, db *sql.DB, src SourceSecret, schema, table string, maxFields int) (TableConfig, error) {
	out := TableConfig{Schema: schema, Table: table, Fields: []Field{}, Keys: []string{}}
	// Check the requested object directly: enumerating the first 1000 tables can hide valid later tables.
	existsQuery := "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2)"
	if src.Kind == "mysql" {
		existsQuery = "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=? AND table_name=?)"
	}
	if src.Kind == "oracle" {
		existsQuery = "SELECT CASE WHEN COUNT(*)>0 THEN 1 ELSE 0 END FROM (SELECT owner,table_name FROM all_tables UNION ALL SELECT owner,view_name FROM all_views) WHERE owner=:1 AND table_name=:2"
	}
	var found bool
	if e := db.QueryRowContext(ctx, existsQuery, schema, table).Scan(&found); e != nil {
		return out, e
	}
	if !found {
		return out, problem(404, "TABLE_NOT_FOUND", "表或视图不可见，请核查源对象及读取权限")
	}
	var e error
	q := "SELECT column_name,data_type,is_nullable,numeric_precision,numeric_scale FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position"
	if src.Kind == "mysql" {
		q = strings.ReplaceAll(strings.ReplaceAll(q, "$1", "?"), "$2", "?")
	}
	if src.Kind == "oracle" {
		q = "SELECT column_name,data_type,CASE nullable WHEN 'Y' THEN 'YES' ELSE 'NO' END,data_precision,data_scale FROM all_tab_columns WHERE owner=:1 AND table_name=:2 ORDER BY column_id"
	}
	rows, e := db.QueryContext(ctx, q, schema, table)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var name, typ, nullable string
		var precision, scale sql.NullInt64
		if e = rows.Scan(&name, &typ, &nullable, &precision, &scale); e != nil {
			rows.Close()
			return out, e
		}
		f := Field{Name: name, LogicalType: sqlLogicalType(typ), Nullable: nullable == "YES"}
		if src.Kind == "oracle" {
			switch {
			case typ == "NUMBER" && scale.Valid && scale.Int64 == 0:
				f.LogicalType = "integer"
			case typ == "NUMBER" || typ == "BINARY_FLOAT" || typ == "BINARY_DOUBLE" || typ == "FLOAT":
				f.LogicalType = "decimal"
			case typ == "VARCHAR2" || typ == "NVARCHAR2" || typ == "CHAR" || typ == "NCHAR":
				f.LogicalType = "string"
			case typ == "DATE" || strings.HasPrefix(typ, "TIMESTAMP"):
				f.LogicalType = "datetime"
			}
		}
		if f.LogicalType == "decimal" && precision.Valid {
			p := int(precision.Int64)
			f.Precision = &p
			if scale.Valid {
				v := int(scale.Int64)
				f.Scale = &v
			}
		}
		out.Fields = append(out.Fields, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Fields) == 0 || len(out.Fields) > maxFields {
		return out, problem(400, "TABLE_FIELD_LIMIT", fmt.Sprintf("请选择 1–%d 个字段的表", maxFields))
	}
	q = "SELECT k.column_name FROM information_schema.table_constraints t JOIN information_schema.key_column_usage k ON t.constraint_name=k.constraint_name AND t.table_schema=k.table_schema AND t.table_name=k.table_name WHERE t.constraint_type='PRIMARY KEY' AND t.table_schema=$1 AND t.table_name=$2 ORDER BY k.ordinal_position"
	if src.Kind == "mysql" {
		q = strings.ReplaceAll(strings.ReplaceAll(q, "$1", "?"), "$2", "?")
	}
	if src.Kind == "postgres" {
		// information_schema hides constraints from SELECT-only users; pg_catalog exposes key metadata.
		q = "SELECT a.attname FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class t ON t.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,position) JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid AND a.attnum=k.attnum WHERE i.indisprimary AND n.nspname=$1 AND t.relname=$2 AND k.position<=i.indnkeyatts ORDER BY k.position"
	}
	if src.Kind == "oracle" {
		q = "SELECT k.column_name FROM all_constraints t JOIN all_cons_columns k ON t.owner=k.owner AND t.constraint_name=k.constraint_name WHERE t.constraint_type='P' AND t.owner=:1 AND t.table_name=:2 ORDER BY k.position"
	}
	keys, e := db.QueryContext(ctx, q, schema, table)
	if e != nil {
		return out, e
	}
	defer keys.Close()
	for keys.Next() {
		var name string
		if e = keys.Scan(&name); e != nil {
			return out, e
		}
		out.Keys = append(out.Keys, name)
	}
	if e = keys.Err(); e != nil {
		return out, e
	}
	for i := range out.Fields {
		out.Fields[i].PrimaryKey = slices.Contains(out.Keys, out.Fields[i].Name)
	}
	return out, nil
}
func (s *Server) sourceForRead(ctx context.Context, tx *gorm.DB, p identity.Principal, id string, allowReader ...bool) (Record, SourceSecret, *sql.DB, error) {
	r, e := get(tx, "source", id, false)
	if e != nil {
		return r, SourceSecret{}, nil, e
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "source", ID: id}, "operate_resource"); e != nil {
		return r, SourceSecret{}, nil, e
	}
	v, e := decode[SourceSecret](r.Body)
	if e != nil {
		return r, v, nil, e
	}
	if readerSource(v.Kind) && len(allowReader) > 0 && allowReader[0] {
		return r, v, nil, nil
	}
	db, e := s.runtime.database(ctx, tx, v)
	return r, v, db, e
}
func directoryForResource(tx *gorm.DB, p identity.Principal, id string) (Record, DirectoryDefinition, string, error) {
	r, e := get(tx, "directory", id, false)
	if e != nil {
		return r, DirectoryDefinition{}, "", e
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "directory", ID: id}, "operate_resource"); e != nil {
		return r, DirectoryDefinition{}, "", e
	}
	v, e := decode[DirectoryData](r.Body)
	if e != nil {
		return r, DirectoryDefinition{}, "", e
	}
	rev := v.PublishedID
	if rev == "" {
		rev = v.DraftID
	}
	rr, e := get(tx, "directory_revision", rev, false)
	if e != nil {
		return r, DirectoryDefinition{}, "", e
	}
	d, e := decode[DirectoryRevisionData](rr.Body)
	return r, d.Definition, rev, e
}
func (s *Server) registerSourceTables(api, write *gin.RouterGroup) {
	api.GET("/sources/:id/tables", s.require("exchange"), func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		var src SourceSecret
		var db *sql.DB
		var e error
		if c.Query("pdb") != "" {
			_, src, db, e = s.cdcSourceForRead(ctx, s.db.WithContext(ctx), principal(c), c.Param("id"), c.Query("pdb"))
		} else {
			_, src, db, e = s.sourceForRead(ctx, s.db.WithContext(ctx), principal(c), c.Param("id"), true)
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		defer closeSourceDB(db)
		v, e := s.sourceTables(ctx, s.db.WithContext(ctx), src, db)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": v})
	})
	api.GET("/sources/:id/table-schema", s.require("exchange"), func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		var src SourceSecret
		var db *sql.DB
		var e error
		if c.Query("pdb") != "" {
			_, src, db, e = s.cdcSourceForRead(ctx, s.db.WithContext(ctx), principal(c), c.Param("id"), c.Query("pdb"))
		} else {
			_, src, db, e = s.sourceForRead(ctx, s.db.WithContext(ctx), principal(c), c.Param("id"), true)
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		defer closeSourceDB(db)
		v, e := s.sourceTableSchema(ctx, s.db.WithContext(ctx), src, db, c.Query("schema"), c.Query("table"))
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, v)
	})
	write.POST("/table-resources", s.require("exchange"), func(c *gin.Context) {
		var input TableResourceInput
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		if e := c.ShouldBindJSON(&input); e != nil {
			s.fail(c, invalid)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		tx := s.db.WithContext(ctx)
		p := principal(c)
		dir, def, _, e := directoryForResource(tx, p, input.DirectoryID)
		if e != nil {
			s.fail(c, e)
			return
		}
		if dir.Version != input.ExpectedVersion || def.Format != "database" {
			s.fail(c, conflict)
			return
		}
		source, src, db, e := s.sourceForRead(ctx, tx, p, input.SourceID, true)
		if e != nil {
			s.fail(c, e)
			return
		}
		defer closeSourceDB(db)
		if source.DepartmentID != dir.DepartmentID {
			s.fail(c, denied)
			return
		}
		cfg, e := s.sourceTableSchema(ctx, tx, src, db, input.Schema, input.Table)
		if e != nil {
			s.fail(c, e)
			return
		}
		cfg.SourceID = source.ID
		adapter := "database"
		if readerSource(src.Kind) {
			adapter = "source_reader"
		}
		v := ManagedResource{Name: def.Name, DataLayer: def.DataLayer, Fields: cfg.Fields, Adapter: adapter, Config: encode(cfg)}
		r, e := newRecord("table_resource", dir.DepartmentID, p.UserID, dir.ID, "ready", v)
		if e == nil {
			e = tx.Transaction(func(t *gorm.DB) error {
				lockedSource, err := get(t, "source", source.ID, true)
				if err != nil {
					return err
				}
				if lockedSource.Version != source.Version {
					return conflict
				}
				if err = requireResourceCapability(t, p, Resource{Type: "source", ID: source.ID}, "operate_resource"); err != nil {
					return err
				}
				current, err := get(t, "directory", dir.ID, true)
				if err != nil {
					return err
				}
				if current.Version != input.ExpectedVersion {
					return conflict
				}
				if err = requireResourceCapability(t, p, Resource{Type: "directory", ID: dir.ID}, "operate_resource"); err != nil {
					return err
				}
				if err = t.Create(&r).Error; err != nil {
					return err
				}
				return s.audit(t, p, "register_table_resource", r.ID, "success", c.GetString("trace_id"))
			})
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(201, gin.H{"resource": Resource{Type: r.Kind, ID: r.ID, Version: r.Version}, "definition": v})
	})
	api.GET("/table-resources/:id/preview", s.require("exchange"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		r, e := get(tx, "table_resource", c.Param("id"), false)
		if e == nil {
			e = requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "operate_resource")
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		v, e := decode[ManagedResource](r.Body)
		if e != nil {
			s.fail(c, e)
			return
		}
		fields := []string{}
		for _, f := range v.Fields {
			fields = append(fields, f.Name)
		}
		ref := Resource{Type: r.Kind, ID: r.ID, Version: r.Version}
		if _, e = s.securityPreviewCheck(tx, p, ref, "query"); e != nil {
			s.fail(c, e)
			return
		}
		rows, _, e := s.readTable(c.Request.Context(), tx, v, fields, nil, 20, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if e = s.audit(tx, p, "preview_table_resource", r.ID, "success", c.GetString("trace_id")); e != nil {
			s.fail(c, e)
			return
		}
		maskRows(rows, v.Fields, p)
		decision, e := s.securityPreviewCheck(tx, p, ref, "query")
		if e != nil {
			s.fail(c, e)
			return
		}
		securityMaskRows(rows, decision.MaskFields)
		c.JSON(200, gin.H{"records": rows})
	})
}
func maskRows(rows []map[string]interface{}, fields []Field, p identity.Principal) {
	if admin(p) {
		return
	}
	for _, r := range rows {
		for _, f := range fields {
			if f.Sensitive {
				if _, ok := r[f.Name]; ok && r[f.Name] != nil {
					r[f.Name] = "***"
				}
			}
		}
	}
}
func (s *Server) readTable(ctx context.Context, tx *gorm.DB, v ManagedResource, fields []string, after []string, limit int, requireKey bool, rowFilters ...[]SecurityRowFilter) ([]map[string]interface{}, []string, error) {
	cfg, e := decode[TableConfig](v.Config)
	if e != nil {
		return nil, nil, e
	}
	filters := []SecurityRowFilter{}
	if len(rowFilters) > 0 {
		filters = rowFilters[0]
	}
	if len(filters) > 0 && v.Adapter != "database" {
		return nil, nil, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "此适配器尚不支持分页前行过滤")
	}
	if v.Adapter == "excel" {
		return readImportRows(tx, cfg, fields, after, limit)
	}
	if v.Adapter == "source_reader" {
		return s.readExternalTable(ctx, tx, cfg, fields, after, limit, requireKey)
	}
	if v.Adapter != "database" {
		return nil, nil, problem(409, "ADAPTER_UNAVAILABLE", "此资源适配器尚未就绪")
	}
	r, e := get(tx, "source", cfg.SourceID, false)
	if e != nil {
		return nil, nil, e
	}
	src, e := decode[SourceSecret](r.Body)
	if e != nil {
		return nil, nil, e
	}
	db, e := s.runtime.database(ctx, tx, src)
	if e != nil {
		return nil, nil, e
	}
	defer closeSourceDB(db)
	current, e := discoverTable(ctx, db, src, cfg.Schema, cfg.Table)
	if e != nil {
		return nil, nil, e
	}
	if !reflect.DeepEqual(current.Fields, cfg.Fields) || !slices.Equal(current.Keys, cfg.Keys) {
		return nil, nil, problem(409, "SOURCE_SCHEMA_CHANGED", "源表结构变化，请重新挂载")
	}
	if requireKey && len(cfg.Keys) == 0 {
		return nil, nil, problem(409, "STABLE_KEY_REQUIRED", "请配置源表主键后重新挂载，以保证稳定分页")
	}
	selected := slices.Clone(fields)
	for _, k := range cfg.Keys {
		if !slices.Contains(selected, k) {
			selected = append(selected, k)
		}
	}
	cols := []string{}
	for _, f := range selected {
		if !slices.ContainsFunc(cfg.Fields, func(v Field) bool { return v.Name == f }) {
			return nil, nil, invalid
		}
		column := quoteIdentifier(src.Kind, f)
		if src.Kind == "oracle" && slices.ContainsFunc(cfg.Fields, func(field Field) bool {
			return field.Name == f && (field.LogicalType == "integer" || field.LogicalType == "decimal")
		}) {
			column = "TO_CHAR(" + column + ", 'TM9', 'NLS_NUMERIC_CHARACTERS = ''.,''')"
		}
		cols = append(cols, column)
	}
	q := "SELECT " + strings.Join(cols, ",") + " FROM " + quoteIdentifier(src.Kind, cfg.Schema) + "." + quoteIdentifier(src.Kind, cfg.Table)
	args := []interface{}{}
	keys := []string{}
	for _, k := range cfg.Keys {
		keys = append(keys, quoteIdentifier(src.Kind, k))
	}
	if len(after) > 0 {
		if len(after) != len(keys) {
			return nil, nil, invalid
		}
		marks := []string{}
		for i, value := range after {
			args = append(args, value)
			if src.Kind == "postgres" {
				marks = append(marks, fmt.Sprintf("$%d", i+1))
			} else {
				marks = append(marks, "?")
			}
		}
		if src.Kind == "oracle" {
			args = nil
			clauses := []string{}
			for i := range keys {
				terms := []string{}
				for j := 0; j <= i; j++ {
					args = append(args, after[j])
					op := " = "
					if j == i {
						op = " > "
					}
					terms = append(terms, keys[j]+op+fmt.Sprintf(":%d", len(args)))
				}
				clauses = append(clauses, "("+strings.Join(terms, " AND ")+")")
			}
			q += " WHERE (" + strings.Join(clauses, " OR ") + ")"
		} else {
			q += " WHERE (" + strings.Join(keys, ",") + ") > (" + strings.Join(marks, ",") + ")"
		}
	}
	filterSQL, filterArgs, err := securityRowFilterSQL(src.Kind, filters, cfg.Fields, len(args))
	if err != nil {
		return nil, nil, err
	}
	if filterSQL != "" {
		if len(after) > 0 {
			q += " AND (" + filterSQL + ")"
		} else {
			q += " WHERE " + filterSQL
		}
		args = append(args, filterArgs...)
	}
	if len(keys) > 0 {
		q += " ORDER BY " + strings.Join(keys, ",")
	}
	if src.Kind == "oracle" {
		q = "SELECT * FROM (" + q + ") WHERE ROWNUM <= " + fmt.Sprint(limit)
	} else {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, e := db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	var last []string
	for rows.Next() {
		values := make([]interface{}, len(selected))
		ptr := make([]interface{}, len(selected))
		for i := range values {
			ptr[i] = &values[i]
		}
		if e = rows.Scan(ptr...); e != nil {
			return nil, nil, e
		}
		full := map[string]interface{}{}
		for i, name := range selected {
			value := values[i]
			switch x := value.(type) {
			case []byte:
				value = string(x)
			case int64:
				value = fmt.Sprint(x)
			case time.Time:
				value = x.Format(time.RFC3339Nano)
			}
			full[name] = value
		}
		last = nil
		for _, key := range cfg.Keys {
			if full[key] == nil {
				return nil, nil, problem(409, "STABLE_KEY_REQUIRED", "排序键不可为空")
			}
			last = append(last, fmt.Sprint(full[key]))
		}
		record := map[string]interface{}{}
		for _, name := range fields {
			record[name] = full[name]
		}
		out = append(out, record)
	}
	return out, last, rows.Err()
}
