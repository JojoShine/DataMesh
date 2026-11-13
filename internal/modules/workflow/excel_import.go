package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ImportMapping struct {
	Sheet           string         `json:"sheet"`
	HeaderRow       int            `json:"header_row"`
	Mapping         map[string]int `json:"mapping"`
	ExpectedVersion int            `json:"expected_directory_version"`
}
type ImportRow struct {
	BatchID   string          `gorm:"primaryKey"`
	RowNumber int             `gorm:"primaryKey"`
	Data      json.RawMessage `gorm:"type:jsonb"`
}

func (ImportRow) TableName() string { return "resource_import_rows" }

type ImportIssue struct {
	Row     int    `json:"row"`
	Column  string `json:"column"`
	Message string `json:"message"`
}
type ImportPreview struct {
	Headers    []string                 `json:"headers"`
	Mapping    map[string]int           `json:"mapping"`
	Records    []map[string]interface{} `json:"records"`
	Errors     []ImportIssue            `json:"errors"`
	ErrorCount int                      `json:"error_count"`
	RowCount   int                      `json:"row_count"`
	Ignored    []string                 `json:"ignored_columns"`
}

func openExcel(data []byte) (*excelize.File, error) {
	if e := validateOffice(data, ".xlsx"); e != nil {
		return nil, e
	}
	f, e := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: 64 << 20, UnzipXMLSizeLimit: 8 << 20, RawCellValue: true})
	if e != nil {
		return nil, problem(400, "EXCEL_INVALID", "无法读取 Excel 文件，请检查格式")
	}
	return f, nil
}
func excelSheets(data []byte) ([]string, error) {
	f, e := openExcel(data)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 || len(sheets) > 100 {
		return nil, problem(400, "EXCEL_SHEET_LIMIT", "工作表数量须为 1–100")
	}
	return sheets, nil
}

var integerValue = regexp.MustCompile(`^[+-]?[0-9]+$`)
var decimalValue = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

func parseImportValue(raw string, f DirectoryItem) (interface{}, error) {
	if strings.TrimSpace(raw) == "" {
		if f.Nullable {
			return nil, nil
		}
		return nil, fmt.Errorf("必填值为空")
	}
	switch f.LogicalType {
	case "string":
		if f.Length != nil && utf8.RuneCountInString(raw) > *f.Length {
			return nil, fmt.Errorf("超过长度限制")
		}
		return raw, nil
	case "integer":
		if !integerValue.MatchString(raw) {
			return nil, fmt.Errorf("须为整数，请不要使用科学计数法")
		}
		return raw, nil
	case "decimal":
		if !decimalValue.MatchString(raw) {
			return nil, fmt.Errorf("须为十进制数值")
		}
		digits := strings.TrimLeft(raw, "+-")
		parts := strings.Split(digits, ".")
		scale := 0
		if len(parts) == 2 {
			scale = len(parts[1])
		}
		precision := len(strings.TrimLeft(parts[0], "0")) + scale
		if precision == 0 {
			precision = 1
		}
		if f.Precision != nil && precision > *f.Precision {
			return nil, fmt.Errorf("超过数值精度")
		}
		if f.Scale != nil && scale > *f.Scale {
			return nil, fmt.Errorf("超过小数位数")
		}
		return raw, nil
	case "boolean":
		switch strings.ToLower(raw) {
		case "true", "1", "是":
			return true, nil
		case "false", "0", "否":
			return false, nil
		}
		return nil, fmt.Errorf("须为 true/false、1/0 或 是/否")
	case "date":
		for _, layout := range []string{"2006-01-02", "2006/1/2", "1/2/2006", "01-02-06"} {
			if v, e := time.Parse(layout, raw); e == nil {
				return v.Format("2006-01-02"), nil
			}
		}
		return nil, fmt.Errorf("日期须为 YYYY-MM-DD")
	case "datetime":
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006/1/2 15:04:05"} {
			if _, e := time.Parse(layout, raw); e == nil {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("日期时间须为 YYYY-MM-DD HH:mm:ss 或含时区 ISO 格式")
	}
	return nil, fmt.Errorf("不支持的字段类型")
}
func parseExcel(data []byte, def DirectoryDefinition, input ImportMapping) (ImportPreview, []ImportRow, error) {
	out := ImportPreview{Headers: []string{}, Mapping: input.Mapping, Records: []map[string]interface{}{}, Errors: []ImportIssue{}, Ignored: []string{}}
	all := []ImportRow{}
	if input.HeaderRow < 1 || input.HeaderRow > 100 || len(def.Items) == 0 {
		return out, nil, invalid
	}
	book, e := openExcel(data)
	if e != nil {
		return out, nil, e
	}
	defer book.Close()
	props, e := book.GetWorkbookProps()
	if e != nil {
		return out, nil, e
	}
	rows, e := book.Rows(input.Sheet)
	if e != nil {
		return out, nil, problem(400, "EXCEL_SHEET_INVALID", "请选择有效工作表")
	}
	defer rows.Close()
	n := 0
	totalBytes := 0
	issue := func(row int, col, msg string) {
		out.ErrorCount++
		if len(out.Errors) < 100 {
			out.Errors = append(out.Errors, ImportIssue{row, col, msg})
		}
	}
	for rows.Next() {
		n++
		if n > input.HeaderRow+50000 {
			return out, nil, problem(400, "EXCEL_ROW_LIMIT", "每次最多导入 5 万行")
		}
		cells, err := rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return out, nil, err
		}
		if len(cells) > 100 {
			return out, nil, problem(400, "EXCEL_COLUMN_LIMIT", "每次最多导入 100 列")
		}
		if n < input.HeaderRow {
			continue
		}
		if n == input.HeaderRow {
			out.Headers = cells
			if len(cells) == 0 {
				return out, nil, problem(400, "EXCEL_HEADER_REQUIRED", "表头行为空")
			}
			if out.Mapping == nil {
				out.Mapping = map[string]int{}
				for _, f := range def.Items {
					match := -1
					for i, h := range cells {
						if strings.TrimSpace(h) == f.Name || strings.TrimSpace(h) == f.BusinessName {
							if match >= 0 {
								match = -2
								break
							}
							match = i
						}
					}
					if match >= 0 {
						out.Mapping[f.Name] = match
					}
				}
			}
			used := map[int]bool{}
			for _, f := range def.Items {
				index, ok := out.Mapping[f.Name]
				if !ok {
					if !f.Nullable {
						issue(n, f.BusinessName, "缺少必填列，请指定对应表头")
					}
					continue
				}
				if index < 0 || index >= len(cells) || used[index] {
					issue(n, f.BusinessName, "映射列不存在或已被其他字段使用")
					continue
				}
				used[index] = true
			}
			for i, h := range cells {
				if !used[i] {
					out.Ignored = append(out.Ignored, h)
				}
			}
			for name := range out.Mapping {
				exists := false
				for _, f := range def.Items {
					exists = exists || f.Name == name
				}
				if !exists {
					issue(n, name, "目录中不存在此字段")
				}
			}
			continue
		}
		blank := true
		for _, cell := range cells {
			blank = blank && strings.TrimSpace(cell) == ""
		}
		if blank {
			continue
		}
		out.RowCount++
		record := map[string]interface{}{}
		for _, f := range def.Items {
			index, mapped := out.Mapping[f.Name]
			raw := ""
			if mapped && index >= 0 && index < len(cells) {
				raw = cells[index]
			}
			if mapped && index >= 0 && index < 100 {
				axis, _ := excelize.CoordinatesToCellName(index+1, n)
				formula, err := book.GetCellFormula(input.Sheet, axis)
				if err != nil {
					return out, nil, err
				}
				if formula != "" {
					issue(n, f.BusinessName, "公式单元格请先转为值")
					continue
				}
				if f.LogicalType == "date" || f.LogicalType == "datetime" {
					formatted, err := book.GetCellValue(input.Sheet, axis, excelize.Options{RawCellValue: false})
					if err != nil {
						return out, nil, err
					}
					if serial, err := strconv.ParseFloat(raw, 64); err == nil && raw != "" {
						date, err := excelize.ExcelDateToTime(serial, props.Date1904 != nil && *props.Date1904)
						if err != nil {
							issue(n, f.BusinessName, "日期序列无效")
							continue
						}
						if f.LogicalType == "date" {
							raw = date.Format("2006-01-02")
						} else {
							raw = date.Format("2006-01-02 15:04:05")
						}
					} else {
						raw = formatted
					}
				}
			}
			value, err := parseImportValue(raw, f)
			if err != nil {
				issue(n, f.BusinessName, err.Error())
				continue
			}
			record[f.Name] = value
		}
		encoded := encode(record)
		totalBytes += len(encoded)
		if totalBytes > 64<<20 {
			return out, nil, problem(400, "EXCEL_DATA_LIMIT", "解析后数据超过 64 MiB，请拆分导入")
		}
		all = append(all, ImportRow{RowNumber: n, Data: encoded})
		if len(out.Records) < 20 {
			out.Records = append(out.Records, record)
		}
	}
	if e = rows.Error(); e != nil {
		return out, nil, e
	}
	if n < input.HeaderRow {
		return out, nil, problem(400, "EXCEL_HEADER_REQUIRED", "表头行不存在")
	}
	if out.RowCount == 0 {
		issue(input.HeaderRow, "", "没有可导入的数据行")
	}
	return out, all, nil
}
func (s *Server) registerExcelImports(api, write *gin.RouterGroup) {
	api.GET("/resource-imports/:id", s.require("exchange"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		r, e := get(tx, "resource_import", c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if r.OwnerID != p.UserID && !admin(p) {
			s.fail(c, denied)
			return
		}
		if _, _, _, e = directoryForResource(tx, p, r.ParentID); e != nil {
			s.fail(c, e)
			return
		}
		u, e := decode[UploadData](r.Body)
		if e != nil {
			s.fail(c, e)
			return
		}
		state := r.State
		if state == "uploaded" && time.Since(r.CreatedAt) > 24*time.Hour {
			state = "expired"
		}
		var ref *Resource
		if u.ResourceID != "" {
			ref = &Resource{Type: "table_resource", ID: u.ResourceID, Version: 1}
		}
		c.JSON(200, gin.H{"batch_id": r.ID, "state": state, "resource": ref, "name": u.File.Name})
	})
	write.POST("/resource-imports", s.require("exchange"), s.uploadResource(true))
	for _, action := range []string{"preview", "confirm"} {
		action := action
		write.POST("/resource-imports/:id/"+action, s.require("exchange"), func(c *gin.Context) {
			if !fileCapacity(c) {
				return
			}
			defer func() { <-fileWork }()
			tx := s.db.WithContext(c.Request.Context())
			p := principal(c)
			up, e := get(tx, "resource_import", c.Param("id"), false)
			if e != nil {
				s.fail(c, e)
				return
			}
			if up.OwnerID != p.UserID && !admin(p) {
				s.fail(c, denied)
				return
			}
			u, e := decode[UploadData](up.Body)
			if e != nil {
				s.fail(c, e)
				return
			}
			dir, def, rev, e := directoryForResource(tx, p, up.ParentID)
			if e != nil {
				s.fail(c, e)
				return
			}
			if rev != u.DirectoryRevision {
				s.fail(c, problem(409, "DIRECTORY_CHANGED", "目录定义已变化，请重新上传"))
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
			raw, e := io.ReadAll(c.Request.Body)
			if e != nil {
				s.fail(c, invalid)
				return
			}
			input, e := bind[ImportMapping](raw)
			if e != nil {
				s.fail(c, e)
				return
			}
			hash := fingerprint(encode(input))
			if action == "confirm" && up.State == "ready" {
				if hash != u.RequestHash {
					s.fail(c, conflict)
					return
				}
				c.JSON(200, gin.H{"resource": Resource{Type: "table_resource", ID: u.ResourceID, Version: 1}})
				return
			}
			if up.State != "uploaded" || time.Since(up.CreatedAt) > 24*time.Hour {
				s.fail(c, conflict)
				return
			}
			data, e := s.readStoredFile(c.Request.Context(), u.File, 20<<20)
			if e != nil {
				s.fail(c, e)
				return
			}
			preview, rows, e := parseExcel(data, def, input)
			if e != nil {
				s.fail(c, e)
				return
			}
			if action == "preview" {
				c.JSON(200, preview)
				return
			}
			if dir.Version != input.ExpectedVersion {
				s.fail(c, conflict)
				return
			}
			if preview.ErrorCount > 0 {
				c.JSON(400, gin.H{"code": "IMPORT_VALIDATION_FAILED", "message": "存在无效数据，请修正后重新导入", "errors": preview.Errors, "error_count": preview.ErrorCount})
				return
			}
			var ref Resource
			e = tx.Transaction(func(t *gorm.DB) error {
				d, err := get(t, "directory", dir.ID, true)
				if err != nil {
					return err
				}
				if d.Version != input.ExpectedVersion {
					return conflict
				}
				if err = requireResourceCapability(t, p, Resource{Type: "directory", ID: dir.ID}, "operate_resource"); err != nil {
					return err
				}
				locked, err := get(t, "resource_import", up.ID, true)
				if err != nil {
					return err
				}
				prior, err := decode[UploadData](locked.Body)
				if err != nil {
					return err
				}
				if locked.State == "ready" {
					if prior.RequestHash != hash {
						return conflict
					}
					ref = Resource{Type: "table_resource", ID: prior.ResourceID, Version: 1}
					return nil
				}
				if locked.State != "uploaded" {
					return conflict
				}
				fields := directoryFields(def)
				r, err := newRecord("table_resource", dir.DepartmentID, p.UserID, dir.ID, "ready", ManagedResource{Name: def.Name, DataLayer: def.DataLayer, Fields: fields, Adapter: "excel", Config: encode(TableConfig{BatchID: up.ID, Fields: fields, Keys: []string{"row_number"}})})
				if err != nil {
					return err
				}
				for i := range rows {
					rows[i].BatchID = up.ID
				}
				if err = t.CreateInBatches(rows, 200).Error; err != nil {
					return err
				}
				if err = t.Create(&r).Error; err != nil {
					return err
				}
				locked.State = "ready"
				prior.ResourceID = r.ID
				prior.RequestHash = hash
				locked.Body = encode(prior)
				if err = save(t, &locked); err != nil {
					return err
				}
				ref = Resource{Type: r.Kind, ID: r.ID, Version: 1}
				return s.audit(t, p, "confirm_excel_import", r.ID, "success", c.GetString("trace_id"))
			})
			if e != nil {
				s.fail(c, e)
				return
			}
			c.JSON(201, gin.H{"resource": ref, "row_count": preview.RowCount})
		})
	}
}
func directoryFields(def DirectoryDefinition) []Field {
	fields := []Field{}
	for _, f := range def.Items {
		fields = append(fields, Field{Name: f.Name, LogicalType: f.LogicalType, Nullable: f.Nullable, Sensitive: f.Sensitive, Precision: f.Precision, Scale: f.Scale})
	}
	return fields
}
func readImportRows(tx *gorm.DB, cfg TableConfig, fields []string, after []string, limit int) ([]map[string]interface{}, []string, error) {
	offset := 0
	if len(after) > 0 {
		if len(after) != 1 {
			return nil, nil, invalid
		}
		var e error
		offset, e = strconv.Atoi(after[0])
		if e != nil || offset < 0 {
			return nil, nil, invalid
		}
	}
	var rows []ImportRow
	if e := tx.Where("batch_id=? AND row_number>?", cfg.BatchID, offset).Order("row_number").Limit(limit).Find(&rows).Error; e != nil {
		return nil, nil, e
	}
	out := []map[string]interface{}{}
	var last []string
	for _, row := range rows {
		v, e := decode[map[string]interface{}](row.Data)
		if e != nil {
			return nil, nil, e
		}
		record := map[string]interface{}{}
		for _, f := range fields {
			value, ok := v[f]
			if !ok {
				return nil, nil, invalid
			}
			record[f] = value
		}
		out = append(out, record)
		last = []string{strconv.Itoa(row.RowNumber)}
	}
	return out, last, nil
}
