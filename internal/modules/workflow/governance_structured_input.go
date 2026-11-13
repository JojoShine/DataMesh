package workflow

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

type GovernanceFileInput struct {
	Model     GovernanceRef `json:"model"`
	Sheet     string        `json:"sheet"`
	HeaderRow int           `json:"header_row"`
}

func (s *Server) governanceFileContract(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[struct {
		ExpectedVersion int `json:"expected_version"`
		GovernanceFileInput
	}](b)
	if e != nil {
		return 0, nil, id, e
	}
	r, _, e := metadataTarget(tx, p, id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if r.Kind != "file_resource" || r.Version != v.ExpectedVersion || slices.Contains([]string{"disabled", "archived"}, r.State) {
		return 0, nil, id, conflict
	}
	if e = requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: id}, "manage_resource"); e != nil {
		return 0, nil, id, e
	}
	if !validGovernanceRef(&v.Model) || v.HeaderRow < 1 || v.HeaderRow > 1000 || len(v.Sheet) > 128 {
		return 0, nil, id, invalid
	}
	if e = governanceLock(tx, r.DepartmentID); e != nil {
		return 0, nil, id, e
	}
	_, version, e := governanceRead(tx, p, "model", v.Model.ID, v.Model.Version)
	if e != nil {
		return 0, nil, id, e
	}
	if version.State != "published" {
		return 0, nil, id, conflict
	}
	def, e := decode[GovernanceDefinition](version.Definition)
	if e != nil {
		return 0, nil, id, e
	}
	if def.Model == nil {
		return 0, nil, id, invalid
	}
	managed, e := decode[ManagedResource](r.Body)
	if e != nil {
		return 0, nil, id, e
	}
	cfg, e := decode[FileConfig](managed.Config)
	if e != nil {
		return 0, nil, id, e
	}
	cfg.GovernanceInput = &v.GovernanceFileInput
	fields := governanceModelFields(*def.Model)
	data, e := s.readStoredFile(tx.Statement.Context, cfg, 20<<20)
	if e != nil {
		return 0, nil, id, e
	}
	if _, e = governanceFileRows(data, cfg, fields, func([]interface{}) error { return nil }); e != nil {
		return 0, nil, id, e
	}
	managed.Fields = fields
	managed.Config = encode(cfg)
	r.Body = encode(managed)
	r.Version++
	if e = tx.Save(&r).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(map[string]interface{}{"resource": Resource{Type: "file_resource", ID: id, Version: r.Version}, "fields": fields, "structured_input": cfg.GovernanceInput}), id, nil
}

func (s *Server) governanceStructuredInputBasis(tx *gorm.DB, p identity.Principal, r Record, target MetadataResource, parameters map[string]string) error {
	if target.ResourceType != "api_resource" && len(parameters) > 0 {
		return invalid
	}
	if target.ResourceType == "governed_dataset" {
		data, e := decode[DatasetData](r.Body)
		if e != nil {
			return e
		}
		if data.GovernanceRunID == "" {
			return problem(409, "GOVERNANCE_INPUT_UNAVAILABLE", "仅自动登记的可信治理快照可继续计算")
		}
		_, _, _, _, e = s.governanceOutputReady(tx.Statement.Context, tx, r)
		return e
	}
	if target.ResourceType == "file_resource" {
		managed, e := decode[ManagedResource](r.Body)
		if e != nil {
			return e
		}
		cfg, e := decode[FileConfig](managed.Config)
		if e != nil {
			return e
		}
		if cfg.GovernanceInput == nil || len(managed.Fields) == 0 {
			return problem(409, "GOVERNANCE_FILE_CONTRACT_REQUIRED", "请先在元数据详情登记CSV或XLSX结构化输入契约")
		}
		_, version, e := governanceRead(tx, p, "model", cfg.GovernanceInput.Model.ID, cfg.GovernanceInput.Model.Version)
		if e != nil {
			return e
		}
		if version.State != "published" {
			return conflict
		}
	}
	if target.ResourceType == "api_resource" {
		managed, e := decode[ManagedResource](r.Body)
		if e != nil {
			return e
		}
		cfg, e := decode[APIConfig](managed.Config)
		if e != nil {
			return e
		}
		if len(parameters) > 50 {
			return invalid
		}
		for key, value := range parameters {
			if len(value) > 4000 || !slices.ContainsFunc(cfg.Parameters, func(p APIParameter) bool { return p.Name == key }) {
				return invalid
			}
		}
		for _, parameter := range cfg.Parameters {
			value, ok := parameters[parameter.Name]
			if !ok {
				if parameter.Required {
					return problem(400, "API_PARAMETER_REQUIRED", "请填写已声明的查询参数")
				}
				continue
			}
			if _, e = parseImportValue(value, DirectoryItem{LogicalType: parameter.Type, Nullable: !parameter.Required}); e != nil {
				return invalid
			}
		}
		if cfg.Method != "GET" && (cfg.Method != "POST" || !cfg.QueryOnly) {
			return invalid
		}
		client, e := apiTransport(tx.Statement.Context, cfg.URL)
		if e != nil {
			return e
		}
		client.CloseIdleConnections()
	} else if len(parameters) > 0 {
		return invalid
	}
	return nil
}

func governanceFileRows(data []byte, cfg FileConfig, fields []Field, consume func([]interface{}) error) (int64, error) {
	input := cfg.GovernanceInput
	if input == nil || input.HeaderRow < 1 || input.HeaderRow > 1000 {
		return 0, invalid
	}
	var next func() ([]string, error)
	var closeFile func() error
	switch strings.ToLower(filepath.Ext(cfg.Name)) {
	case ".csv":
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return 0, problem(400, "GOVERNANCE_FILE_ENCODING", "CSV须为UTF-8文本")
		}
		reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
		reader.FieldsPerRecord = -1
		next = reader.Read
	case ".xlsx":
		file, e := openExcel(data)
		if e != nil {
			return 0, e
		}
		defer file.Close()
		if !slices.Contains(file.GetSheetList(), input.Sheet) {
			return 0, problem(400, "EXCEL_SHEET_REQUIRED", "请选择文件中实际存在的工作表")
		}
		rows, e := file.Rows(input.Sheet)
		if e != nil {
			return 0, e
		}
		closeFile = rows.Close
		next = func() ([]string, error) {
			if !rows.Next() {
				if e := rows.Error(); e != nil {
					return nil, e
				}
				return nil, io.EOF
			}
			return rows.Columns()
		}
	default:
		return 0, problem(400, "GOVERNANCE_FILE_FORMAT", "结构化输入支持CSV或XLSX文件")
	}
	if closeFile != nil {
		defer closeFile()
	}
	var header []string
	for row := 0; row < input.HeaderRow; row++ {
		var e error
		header, e = next()
		if e != nil {
			return 0, problem(400, "GOVERNANCE_FILE_HEADER", "无法读取指定表头行")
		}
	}
	indexes := map[string]int{}
	for i, name := range header {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := indexes[name]; exists {
			return 0, problem(400, "GOVERNANCE_FILE_HEADER", "表头字段名称重复")
		}
		indexes[name] = i
	}
	for _, field := range fields {
		if _, ok := indexes[field.Name]; !ok {
			return 0, problem(400, "GOVERNANCE_FILE_HEADER", "表头缺少契约字段："+field.Name)
		}
	}
	var total int64
	for {
		row, e := next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return total, problem(400, "GOVERNANCE_FILE_INVALID", "文件行格式不正确")
		}
		if slices.IndexFunc(row, func(value string) bool { return strings.TrimSpace(value) != "" }) < 0 {
			continue
		}
		total++
		if total > 10000 {
			return total, problem(409, "GOVERNANCE_LIMIT", "结构化文件输入最多10000行，请拆分文件或导入数据库")
		}
		values := make([]interface{}, len(fields))
		for i, field := range fields {
			raw := ""
			if at := indexes[field.Name]; at < len(row) {
				raw = row[at]
			}
			value, e := parseImportValue(raw, DirectoryItem{LogicalType: field.LogicalType, Nullable: field.Nullable, Precision: field.Precision, Scale: field.Scale})
			if e != nil {
				return total, problem(400, "GOVERNANCE_FILE_TYPE", "文件字段不符合输入契约："+field.Name)
			}
			values[i] = value
		}
		if e = consume(values); e != nil {
			return total, e
		}
	}
	return total, nil
}

func (s *Server) governanceReadStructuredInput(ctx context.Context, r Record, basis governanceInput, limit int, consume func([]interface{}) error) (int64, error) {
	if basis.Resource.Type == "governed_dataset" {
		_, _, _, report, e := s.governanceOutputReady(ctx, s.db.WithContext(ctx), r)
		if e != nil {
			return 0, e
		}
		count := min(int(report.OutputRows), limit)
		for start := 0; start < count; start += 250 {
			rows, e := s.governanceOutputPage(ctx, s.db.WithContext(ctx), r, start, min(250, count-start))
			if e != nil {
				return 0, e
			}
			for _, row := range rows {
				values := make([]interface{}, len(basis.Fields))
				for i, field := range basis.Fields {
					decoder := json.NewDecoder(bytes.NewReader(row[field.Name]))
					decoder.UseNumber()
					if e = decoder.Decode(&values[i]); e != nil {
						return 0, e
					}
				}
				if e = consume(values); e != nil {
					return 0, e
				}
			}
		}
		return report.OutputRows, nil
	}
	managed, e := decode[ManagedResource](r.Body)
	if e != nil {
		return 0, e
	}
	if basis.Resource.Type == "file_resource" {
		cfg, e := decode[FileConfig](managed.Config)
		if e != nil {
			return 0, e
		}
		data, e := s.readStoredFile(ctx, cfg, 20<<20)
		if e != nil {
			return 0, e
		}
		seen := 0
		return governanceFileRows(data, cfg, basis.Fields, func(values []interface{}) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			seen++
			if seen > limit {
				return nil
			}
			return consume(values)
		})
	}
	cfg, e := decode[APIConfig](managed.Config)
	if e != nil {
		return 0, e
	}
	parameters := map[string]interface{}{}
	for name, value := range basis.Parameters {
		parameters[name] = value
	}
	rows, e := s.callAPI(ctx, cfg, basis.Fields, parameters)
	if e != nil {
		return 0, e
	}
	if len(rows) > 10000 {
		return 0, problem(409, "GOVERNANCE_LIMIT", "API单次响应最多10000行")
	}
	for index, row := range rows {
		if index >= limit {
			break
		}
		values := make([]interface{}, len(basis.Fields))
		for i, field := range basis.Fields {
			values[i] = row[field.Name]
		}
		if e = consume(values); e != nil {
			return 0, e
		}
	}
	return int64(len(rows)), nil
}
