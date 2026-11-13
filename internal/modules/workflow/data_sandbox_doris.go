package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

var sandboxPhysicalName = regexp.MustCompile(`^t_[0-9a-f]{32}$`)
var sandboxFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func sandboxDorisOpen(ctx context.Context, write bool) (*sql.DB, error) {
	cfg := mysql.NewConfig()
	cfg.User = "datamesh_sandbox_reader"
	cfg.Passwd = os.Getenv("SANDBOX_DORIS_READER_PASSWORD")
	if write {
		cfg.User = "datamesh_sandbox_writer"
		cfg.Passwd = os.Getenv("SANDBOX_DORIS_WRITER_PASSWORD")
	}
	if cfg.Passwd == "" {
		return nil, problem(503, "SANDBOX_UNCONFIGURED", "数据沙箱执行身份尚未配置")
	}
	cfg.Net = "tcp"
	cfg.Addr = dorisAddress()
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 25 * time.Second
	cfg.WriteTimeout = 25 * time.Second
	cfg.Params = map[string]string{"exec_mem_limit": "134217728", "query_timeout": "20"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, problem(503, "SANDBOX_ENGINE_UNAVAILABLE", "沙箱计算暂不可用")
	}
	return db, nil
}
func sandboxColumnType(field Field) (string, error) {
	switch field.LogicalType {
	case "string":
		return "STRING", nil
	case "integer":
		return "BIGINT", nil
	case "decimal":
		if field.Precision == nil || field.Scale == nil || *field.Precision < 1 || *field.Precision > 38 || *field.Scale < 0 || *field.Scale > *field.Precision {
			return "", problem(400, "SANDBOX_RESULT_TYPE", "小数精度须为1–38位且小数位不超过总位数")
		}
		return fmt.Sprintf("DECIMAL(%d,%d)", *field.Precision, *field.Scale), nil
	case "boolean":
		return "BOOLEAN", nil
	case "date":
		return "DATE", nil
	case "datetime":
		return "DATETIME(6)", nil
	}
	return "", problem(400, "SANDBOX_RESULT_TYPE", "查询结果包含暂不支持的字段类型")
}
func sandboxQualified(physical string) (string, error) {
	if !sandboxPhysicalName.MatchString(physical) {
		return "", invalid
	}
	return dorisIdentifier(sandboxDatabase) + "." + dorisIdentifier(physical), nil
}
func sandboxDorisRead(ctx context.Context, query string, maxRows, maxBytes int) (sandboxRows, error) {
	out := sandboxRows{FieldLabels: map[string]string{}, Values: [][]interface{}{}}
	if maxRows < 1 || maxRows > lightweightMaxRows || maxBytes < 1 || maxBytes > 64<<20 {
		return out, invalid
	}
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	db, err := sandboxDorisOpen(ctx, false)
	if err != nil {
		return out, err
	}
	defer db.Close()
	// Only the AST compiler or a fixed internal statement may call this engine boundary.
	rows, err := db.QueryContext(ctx, "SELECT * FROM ("+query+") AS _dm_sandbox_result LIMIT "+strconv.Itoa(maxRows+1))
	if err != nil {
		return out, problem(400, "SANDBOX_SQL_EXECUTION", "SQL未能执行，请核对字段、类型和计算范围")
	}
	defer rows.Close()
	columns, err := rows.ColumnTypes()
	if err != nil {
		return out, err
	}
	if len(columns) == 0 || len(columns) > 100 {
		return out, problem(400, "SANDBOX_RESULT_COLUMNS", "结果须包含1–100个字段")
	}
	labels := map[string]bool{}
	reserved := map[string]bool{}
	for _, column := range columns {
		key := strings.ToLower(column.Name())
		if labels[key] {
			return out, problem(400, "SANDBOX_DUPLICATE_COLUMNS", "结果字段重名，请使用AS为字段起不同名称")
		}
		labels[key] = true
		if sandboxFieldName.MatchString(column.Name()) && !strings.EqualFold(column.Name(), "_dm_ordinal") {
			reserved[key] = true
		}
	}
	for i, column := range columns {
		name := column.Name()
		if !sandboxFieldName.MatchString(name) || strings.EqualFold(name, "_dm_ordinal") {
			name = fmt.Sprintf("column_%d", i+1)
			for reserved[strings.ToLower(name)] {
				name += "_"
			}
			reserved[strings.ToLower(name)] = true
		}
		field := Field{Name: name, LogicalType: "string", Nullable: true}
		kind := strings.ToUpper(column.DatabaseTypeName())
		switch kind {
		case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "UNSIGNED BIGINT", "UNSIGNED INT", "UNSIGNED SMALLINT", "UNSIGNED TINYINT":
			field.LogicalType = "integer"
		case "DECIMAL", "NEWDECIMAL", "DECIMAL32", "DECIMAL64", "DECIMAL128", "DECIMAL256":
			precision, scale, ok := column.DecimalSize()
			if !ok || precision < 1 || precision > 38 || scale < 0 || scale > precision {
				return out, problem(400, "SANDBOX_RESULT_TYPE", "结果小数精度超出38位，请在SQL中显式CAST")
			}
			p, s := int(precision), int(scale)
			field.LogicalType = "decimal"
			field.Precision = &p
			field.Scale = &s
		case "DATE":
			field.LogicalType = "date"
		case "DATETIME", "TIMESTAMP":
			field.LogicalType = "datetime"
		case "FLOAT", "DOUBLE":
			field.LogicalType = "string"
		case "BOOLEAN", "BOOL":
			field.LogicalType = "boolean"
		}
		out.Fields = append(out.Fields, field)
		out.FieldLabels[name] = column.Name()
	}
	used := 0
	for rows.Next() {
		if len(out.Values) == maxRows {
			out.Truncated = true
			break
		}
		raw := make([]sql.RawBytes, len(columns))
		dest := make([]interface{}, len(columns))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return out, err
		}
		value := make([]interface{}, len(raw))
		for i, cell := range raw {
			if cell == nil {
				continue
			}
			if len(cell) > 1<<20 {
				return out, problem(400, "SANDBOX_CELL_LIMIT", "单个结果字段不能超过1MiB，请在SQL中截取所需内容")
			}
			value[i] = string(cell)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return out, err
		}
		used += len(data) + 1
		if used > maxBytes {
			return out, problem(400, "SANDBOX_RESULT_LIMIT", "结果超过大小上限，请缩小查询范围")
		}
		out.Values = append(out.Values, value)
	}
	if err = rows.Err(); err != nil {
		return out, problem(503, "SANDBOX_QUERY_INTERRUPTED", "查询超时或已取消，结果未发布")
	}
	return out, nil
}
func sandboxDorisWrite(ctx context.Context, physical string, fields []Field, values [][]interface{}, label string) error {
	target, err := sandboxQualified(physical)
	if err != nil {
		return err
	}
	if len(fields) == 0 || len(fields) > 100 || len(values) > lightweightMaxRows || !regexp.MustCompile(`^[A-Za-z0-9_\-]{1,128}$`).MatchString(label) {
		return invalid
	}
	columns := []string{"`_dm_ordinal` BIGINT NOT NULL"}
	names := []string{"_dm_ordinal"}
	seen := map[string]bool{"_dm_ordinal": true}
	for _, field := range fields {
		if !sandboxFieldName.MatchString(field.Name) || seen[strings.ToLower(field.Name)] {
			return invalid
		}
		seen[strings.ToLower(field.Name)] = true
		kind, e := sandboxColumnType(field)
		if e != nil {
			return e
		}
		columns = append(columns, dorisIdentifier(field.Name)+" "+kind+" NULL")
		names = append(names, field.Name)
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for index, value := range values {
		if len(value) != len(fields) {
			return invalid
		}
		row := make([]interface{}, len(value)+1)
		row[0] = index
		for i, v := range value {
			switch v.(type) {
			case nil, string, json.Number, bool:
			default:
				return invalid
			}
			row[i+1] = v
		}
		if err = encoder.Encode(map[string]interface{}{"r": row}); err != nil {
			return err
		}
		if body.Len() > 64<<20 {
			return problem(400, "SANDBOX_RESULT_LIMIT", "库化输入展开后超过64MiB，请缩小字段或数据范围")
		}
	}
	db, err := sandboxDorisOpen(ctx, true)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+target+" ("+strings.Join(columns, ",")+") UNIQUE KEY(_dm_ordinal) DISTRIBUTED BY HASH(_dm_ordinal) BUCKETS 1 PROPERTIES ('replication_num'='1','enable_unique_key_merge_on_write'='true')"); err != nil {
		return problem(503, "SANDBOX_WRITE_UNKNOWN", "目标创建结果待核对，未重复执行写入")
	}
	if len(values) == 0 {
		return nil
	}
	origin := os.Getenv("SANDBOX_DORIS_BE_URL")
	if origin == "" {
		origin = "http://doris-be:8040"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Trim(u.Path, "/") != "" {
		return problem(503, "SANDBOX_UNCONFIGURED", "沙箱写入地址配置无效")
	}
	u.Path = "/api/" + sandboxDatabase + "/" + physical + "/_stream_load"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	req.SetBasicAuth("datamesh_sandbox_writer", os.Getenv("SANDBOX_DORIS_WRITER_PASSWORD"))
	paths := make([]string, len(names))
	for i := range paths {
		paths[i] = fmt.Sprintf("$.r[%d]", i)
	}
	jsonPaths, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	req.Header.Set("jsonpaths", string(jsonPaths))
	for k, v := range map[string]string{"label": label, "format": "json", "read_json_by_line": "true", "strict_mode": "true", "max_filter_ratio": "0", "timeout": "60", "exec_mem_limit": "134217728", "columns": strings.Join(names, ",")} {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 70 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return problem(503, "SANDBOX_WRITE_UNKNOWN", "写入结果待核对，未自动重发")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 || response.StatusCode != 200 {
		return problem(503, "SANDBOX_WRITE_UNKNOWN", "写入响应待核对，未自动重发")
	}
	var result struct {
		Status                                                                      string
		NumberTotalRows, NumberLoadedRows, NumberFilteredRows, NumberUnselectedRows int64
	}
	if json.Unmarshal(raw, &result) != nil || (result.Status != "Success" && result.Status != "Publish Timeout") || result.NumberTotalRows != int64(len(values)) || result.NumberLoadedRows != int64(len(values)) || result.NumberFilteredRows != 0 || result.NumberUnselectedRows != 0 {
		return problem(503, "SANDBOX_WRITE_UNKNOWN", "写入完整性尚未确认，未发布结果")
	}
	ok, err := sandboxDorisVerify(ctx, physical, fields, values)
	if err != nil || !ok {
		return problem(503, "SANDBOX_WRITE_UNKNOWN", "写入内容尚未确认，未发布结果")
	}
	return nil
}
func sandboxDorisVerify(ctx context.Context, physical string, fields []Field, values [][]interface{}) (bool, error) {
	target, err := sandboxQualified(physical)
	if err != nil {
		return false, err
	}
	if len(fields) == 0 || len(values) > lightweightMaxRows {
		return false, invalid
	}
	names := make([]string, len(fields))
	for i, field := range fields {
		if !sandboxFieldName.MatchString(field.Name) {
			return false, invalid
		}
		names[i] = dorisIdentifier(field.Name)
	}
	db, err := sandboxDorisOpen(ctx, false)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var count int64
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target).Scan(&count); err != nil {
		return false, err
	}
	if count != int64(len(values)) {
		return false, nil
	}
	// Verify bounded key ranges: sorting a whole wide table exceeds the same 128MiB
	// query budget that protects interactive SQL. Ordinals and total count fence gaps.
	for start := 0; start < len(values); start += 1000 {
		end := min(start+1000, len(values))
		query := "SELECT " + strings.Join(names, ",") + " FROM " + target + fmt.Sprintf(" WHERE `_dm_ordinal` >= %d AND `_dm_ordinal` < %d ORDER BY `_dm_ordinal`", start, end)
		rows, e := db.QueryContext(ctx, query)
		if e != nil {
			return false, e
		}
		index := start
		for rows.Next() {
			if index >= end || len(values[index]) != len(fields) {
				rows.Close()
				return false, nil
			}
			raw := make([]sql.RawBytes, len(fields))
			dest := make([]interface{}, len(fields))
			for i := range raw {
				dest[i] = &raw[i]
			}
			if e = rows.Scan(dest...); e != nil {
				rows.Close()
				return false, e
			}
			for i, cell := range raw {
				expected := values[index][i]
				if expected == nil {
					if cell != nil {
						rows.Close()
						return false, nil
					}
					continue
				}
				if cell == nil {
					rows.Close()
					return false, nil
				}
				var text string
				switch value := expected.(type) {
				case string:
					text = value
				case json.Number:
					text = value.String()
				case bool:
					if value {
						text = "1"
					} else {
						text = "0"
					}
				default:
					rows.Close()
					return false, invalid
				}
				if string(cell) != text {
					rows.Close()
					return false, nil
				}
			}
			index++
		}
		e = rows.Err()
		closeErr := rows.Close()
		if e != nil {
			return false, e
		}
		if closeErr != nil {
			return false, closeErr
		}
		if index != end {
			return false, nil
		}
	}
	return true, nil
}
func sandboxDorisDrop(ctx context.Context, physical string) error {
	target, err := sandboxQualified(physical)
	if err != nil {
		return err
	}
	db, err := sandboxDorisOpen(ctx, true)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+target)
	return err
}
