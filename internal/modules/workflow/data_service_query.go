package workflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// DataServiceQuery is created only by the service publisher, never by callers.
type DataServiceQuery struct {
	DatasetID      string            `json:"dataset_id"`
	Mode           string            `json:"mode"`
	PublisherID    string            `json:"publisher_id"`
	SQL            string            `json:"sql"`
	MaxPageSize    int               `json:"max_page_size"`
	SourceFields   []Field           `json:"source_fields"`
	SourceSnapshot *SharingSnapshot  `json:"source_snapshot,omitempty"`
	CatalogFields  []Field           `json:"catalog_fields,omitempty"`
	CatalogMapping map[string]string `json:"catalog_mapping,omitempty"`
}
type servicePredicate struct{ field, operator, parameter string }
type serviceSelect struct {
	fields     []string
	predicates []servicePredicate
	order      string
}

var dataServiceSlots = make(chan struct{}, 8)

var serviceSelectPattern = regexp.MustCompile(`(?is)^\s*SELECT\s+(\*|[a-z_][a-z0-9_]*(?:\s*,\s*[a-z_][a-z0-9_]*)*)\s+FROM\s+result(?:\s+WHERE\s+(.+?))?(?:\s+ORDER\s+BY\s+([a-z_][a-z0-9_]*)(?:\s+(ASC|DESC))?)?\s*$`)
var serviceAndPattern = regexp.MustCompile(`(?i)\s+AND\s+`)
var servicePredicatePattern = regexp.MustCompile(`(?i)^([a-z_][a-z0-9_]*)\s*(=|!=|<>|>=|<=|>|<)\s*:([a-z_][a-z0-9_]*)$`)

func compileServiceSelect(sql string, schema []Field) (serviceSelect, []APIParameter, error) {
	out := serviceSelect{}
	if len(sql) > 8000 {
		return out, nil, invalid
	}
	sql = strings.TrimSpace(sql)
	sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	parts := serviceSelectPattern.FindStringSubmatch(sql)
	if parts == nil {
		return out, nil, problem(400, "SERVICE_SQL_INVALID", "仅支持 SELECT 字段或 * FROM result，可附加 AND 参数条件和单字段 ORDER BY；不支持函数、跨表或多语句")
	}
	fields := map[string]Field{}
	for _, f := range schema {
		fields[f.Name] = f
	}
	selected := strings.Split(parts[1], ",")
	if strings.TrimSpace(parts[1]) == "*" {
		selected = []string{}
		for _, field := range schema {
			selected = append(selected, field.Name)
		}
	}
	for _, name := range selected {
		name = strings.TrimSpace(name)
		if _, ok := fields[name]; !ok || slices.Contains(out.fields, name) {
			return out, nil, problem(400, "SERVICE_FIELD_INVALID", "查询字段不存在或重复")
		}
		out.fields = append(out.fields, name)
	}
	params := []APIParameter{}
	types := map[string]string{}
	if parts[2] != "" {
		for _, raw := range serviceAndPattern.Split(strings.TrimSpace(parts[2]), -1) {
			p := servicePredicatePattern.FindStringSubmatch(raw)
			if p == nil {
				return out, nil, problem(400, "SERVICE_SQL_INVALID", "查询条件请使用 字段 运算符 :参数，以 AND 连接")
			}
			field, ok := fields[p[1]]
			if !ok || field.Sensitive {
				return out, nil, problem(400, "SERVICE_FILTER_INVALID", "筛选字段必须存在，且不能是敏感字段")
			}
			if t, ok := types[p[3]]; ok && t != field.LogicalType {
				return out, nil, invalid
			} else if !ok {
				params = append(params, APIParameter{Name: p[3], Type: field.LogicalType, Required: true})
				types[p[3]] = field.LogicalType
			}
			out.predicates = append(out.predicates, servicePredicate{p[1], p[2], p[3]})
		}
	}
	if len(params) > 50 {
		return out, nil, invalid
	}
	if parts[3] != "" {
		f, ok := fields[parts[3]]
		if !ok || f.Sensitive {
			return out, nil, problem(400, "SERVICE_ORDER_INVALID", "排序字段必须存在，且不能是敏感字段")
		}
		out.order = dorisIdentifier(f.Name)
		if strings.EqualFold(parts[4], "DESC") {
			out.order += " DESC"
		} else {
			out.order += " ASC"
		}
		out.order += ","
	}
	out.order += "`_dm_ordinal` ASC"
	return out, params, nil
}
func (s *Server) serviceSource(ctx context.Context, tx *gorm.DB, cfg *DataServiceQuery, pinned string) (Record, DatasetData, error) {
	r, e := get(tx, "dataset", cfg.DatasetID, false)
	if e != nil {
		return r, DatasetData{}, e
	}
	if cfg.SourceSnapshot != nil {
		if _, _, _, err := serviceCatalogScope(tx, cfg.SourceSnapshot.DirectoryID, cfg); err != nil {
			return r, DatasetData{}, err
		}
	}
	if cfg.Mode != "snapshot" && cfg.Mode != "latest" {
		return r, DatasetData{}, invalid
	}
	if cfg.Mode == "latest" && pinned == "" {
		// Published results form the update boundary; a newer incompatible result stops delivery.
		var latest Record
		e = tx.Where("kind='dataset' AND parent_id=? AND department_id=? AND state='published'", r.ParentID, r.DepartmentID).Order("created_at DESC,id DESC").Take(&latest).Error
		if e == nil {
			r = latest
		}
		if e != nil {
			return r, DatasetData{}, e
		}
	} else if pinned != "" && pinned != r.ID {
		if cfg.Mode != "latest" {
			return r, DatasetData{}, invalid
		}
		base := r
		r, e = get(tx, "dataset", pinned, false)
		if e != nil {
			return r, DatasetData{}, e
		}
		if r.ParentID != base.ParentID || r.DepartmentID != base.DepartmentID {
			return r, DatasetData{}, invalid
		}
	}
	d, e := decode[DatasetData](r.Body)
	if e != nil {
		return r, d, e
	}
	if r.State != "published" || d.QualityStatus != "passed" || d.GovernanceRunID == "" || !validGovernanceStorage(d.Storage, r.ID) {
		return r, d, problem(409, "SERVICE_SOURCE_UNAVAILABLE", "接口来源必须是已核验并发布的 Doris 治理成果")
	}
	if fingerprint(encode(d.Definition.Fields)) != fingerprint(encode(cfg.SourceFields)) {
		return r, d, problem(409, "SERVICE_SCHEMA_CHANGED", "成果字段契约发生变化，请重新校验并发布接口版本")
	}
	p, e := s.etlPrincipal(ctx, cfg.PublisherID)
	if e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			e = denied
		}
		return r, d, e
	}
	if !platform(p, "exchange") {
		return r, d, denied
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "governed_dataset", ID: r.ID, Version: r.Version}, "manage_resource"); e != nil {
		return r, d, e
	}
	if e = requireDatasetProcessingBasis(tx, r, p); e != nil {
		return r, d, e
	}
	_, _, _, _, e = s.governanceOutputReady(ctx, tx, r)
	return r, d, e
}
func (s *Server) queryDataService(ctx context.Context, cfg *DataServiceQuery, fields []Field, parameters map[string]interface{}, pinned string, start, size int, rowFilters ...[]SecurityRowFilter) ([]map[string]interface{}, string, bool, error) {
	if cfg == nil || cfg.MaxPageSize < 1 || cfg.MaxPageSize > 1000 || size < 1 || size > cfg.MaxPageSize || start < 0 || start > 1000000 {
		return nil, "", false, invalid
	}
	queryFields := slices.Clone(cfg.SourceFields)
	if cfg.SourceSnapshot != nil {
		queryFields = slices.Clone(cfg.CatalogFields)
	}
	for i := range queryFields {
		for _, published := range fields {
			if published.Name == queryFields[i].Name && published.Sensitive {
				queryFields[i].Sensitive = true
			}
		}
	}
	parsed, params, e := compileServiceSelect(cfg.SQL, queryFields)
	if e != nil {
		return nil, "", false, e
	}
	if len(parameters) != len(params) {
		return nil, "", false, problem(400, "SERVICE_PARAMETERS_INVALID", "请提供全部声明参数，不接受额外参数")
	}

	for _, p := range params {
		raw, ok := parameters[p.Name].(string)
		if !ok || len(raw) > 4096 {
			return nil, "", false, invalid
		}
		_, e = parseImportValue(raw, DirectoryItem{LogicalType: p.Type, Nullable: false})
		if e != nil {
			return nil, "", false, problem(400, "SERVICE_PARAMETER_TYPE", "参数格式错误："+p.Name)
		}
	}
	select {
	case dataServiceSlots <- struct{}{}:
		defer func() { <-dataServiceSlots }()
	default:
		return nil, "", false, problem(429, "SERVICE_BUSY", "查询并发已满，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx := s.db.WithContext(ctx)
	r, d, e := s.serviceSource(ctx, tx, cfg, pinned)
	if e != nil {
		return nil, "", false, e
	}
	pairs := []string{}
	args := []interface{}{}
	for _, f := range fields {
		if !slices.Contains(parsed.fields, f.Name) {
			return nil, "", false, invalid
		}
		value := dorisIdentifier(f.Name)
		switch f.LogicalType {
		case "integer", "decimal", "date":
			value = "CAST(" + value + " AS STRING)"
		case "datetime":
			value = "CONCAT(REPLACE(CAST(" + value + " AS STRING),' ','T'),'Z')"
		}
		pairs = append(pairs, "?", value)
		args = append(args, f.Name)
	}
	where := []string{}
	for _, p := range parsed.predicates {
		var field Field
		for _, f := range cfg.SourceFields {
			if f.Name == p.field {
				field = f
				break
			}
		}
		value, err := serviceParameterValue(parameters[p.parameter].(string), field)
		if err != nil {
			return nil, "", false, problem(400, "SERVICE_PARAMETER_TYPE", "参数格式或精度不符："+p.parameter)
		}
		where = append(where, dorisIdentifier(p.field)+" "+p.operator+" CAST(? AS "+dorisFieldType(field)+")")
		args = append(args, value)
	}
	filters := []SecurityRowFilter{}
	if len(rowFilters) > 0 {
		filters = rowFilters[0]
	}
	if len(filters) > 0 && cfg.SourceSnapshot != nil {
		return nil, "", false, problem(403, "SECURITY_ROW_FILTER_UNSUPPORTED", "目录字段映射尚不支持行过滤")
	}
	filterSQL, filterArgs, err := securityRowFilterSQL("mysql", filters, cfg.SourceFields, len(args))
	if err != nil {
		return nil, "", false, err
	}
	if filterSQL != "" {
		where = append(where, "("+filterSQL+")")
		args = append(args, filterArgs...)
	}
	sql := "SELECT JSON_OBJECT(" + strings.Join(pairs, ",") + ") FROM " + dorisIdentifier(d.Storage.Database) + "." + dorisIdentifier(d.Storage.Table)
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY " + parsed.order + fmt.Sprintf(" LIMIT %d OFFSET %d", size+1, start)
	db, e := dorisOpen(ctx, false)
	if e != nil {
		return nil, "", false, e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, sql, args...)
	if e != nil {
		return nil, "", false, e
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	bytes := 0
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, "", false, e
		}
		bytes += len(raw)
		if bytes > 8<<20 {
			return nil, "", false, problem(413, "SERVICE_PAGE_TOO_LARGE", "结果超过 8 MiB，请减小每页条数")
		}
		row, err := decode[DataRow](raw)
		if err != nil {
			return nil, "", false, err
		}
		if err = normalizeSnapshotNumbers(row, Object{Fields: fields}); err != nil {
			return nil, "", false, err
		}
		item := map[string]interface{}{}
		for name, value := range row {
			item[name] = value
		}
		out = append(out, item)
	}
	if e = rows.Err(); e != nil {
		return nil, "", false, e
	}
	if _, _, e = s.serviceSource(ctx, tx, cfg, r.ID); e != nil {
		return nil, "", false, e
	}
	more := len(out) > size
	if more {
		out = out[:size]
	}
	return out, r.ID, more, nil
}

func serviceParameterValue(raw string, field Field) (interface{}, error) {
	value, e := parseImportValue(raw, DirectoryItem{LogicalType: field.LogicalType, Precision: field.Precision, Scale: field.Scale, Nullable: false})
	if e != nil {
		return nil, e
	}
	if field.LogicalType == "datetime" {
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006/1/2 15:04:05"} {
			if date, err := time.Parse(layout, raw); err == nil {
				return date.UTC().Format("2006-01-02 15:04:05.999999"), nil
			}
		}
		return nil, invalid
	}
	return value, nil
}
