package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SourceType struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Category      string `json:"category"`
	Port          int    `json:"port"`
	DatabaseLabel string `json:"database_label"`
	ObjectsLabel  string `json:"objects_label"`
	HTTP          bool   `json:"http"`
	CDC           bool   `json:"cdc"`
	Write         bool   `json:"write"`
	Reader        bool   `json:"reader"`
	Status        string `json:"status"`
	MaxRows       int    `json:"max_rows"`
}

var sourceTypes = []SourceType{
	{"postgres", "PostgreSQL", "通用关系型", 5432, "数据库名称", "库表", false, true, true, false, "ready", 0},
	{"mysql", "MySQL", "通用关系型", 3306, "数据库名称", "库表", false, true, true, false, "ready", 0},
	{"oracle", "Oracle", "通用关系型", 1521, "服务名（Service Name）", "库表", false, true, false, false, "ready", 10000},
	{"sqlserver", "SQL Server", "通用关系型", 1433, "数据库名称", "库表", false, false, false, true, "reader_unavailable", 10000},
	{"dm", "达梦 DM", "国产数据库", 5236, "模式（Schema）", "库表", false, false, false, true, "reader_unavailable", 10000},
	{"kingbase", "人大金仓 KingbaseES", "国产数据库", 54321, "数据库名称", "库表", false, false, false, true, "reader_unavailable", 10000},
	{"highgo", "瀚高 HighGo", "国产数据库", 5866, "数据库名称", "库表", false, false, false, true, "reader_unavailable", 10000},
	{"vastbase", "海量 Vastbase", "国产数据库", 5432, "数据库名称", "库表", false, false, false, true, "reader_unavailable", 10000},
	{"doris", "Apache Doris", "分析型数据库", 9030, "数据库名称", "库表", false, false, false, true, "reader_unavailable", 10000},
	{"clickhouse", "ClickHouse", "分析型数据库", 8123, "数据库名称", "库表", true, false, false, true, "reader_unavailable", 10000},
	{"mongodb", "MongoDB", "非关系型", 27017, "数据库名称", "集合", false, false, false, true, "reader_unavailable", 10000},
	{"elasticsearch", "Elasticsearch", "非关系型", 9200, "索引或别名（单一名称）", "索引", true, false, false, true, "reader_unavailable", 10000},
	{"redis", "Redis", "非关系型", 6379, "逻辑库编号（0–15）", "键空间", false, false, false, true, "reader_unavailable", 10000},
	{"hbase", "HBase", "非关系型", 8080, "命名空间（Namespace）", "表（REST 接入）", true, false, false, true, "reader_unavailable", 10000},
}

func sourceType(kind string) (SourceType, bool) {
	for _, v := range sourceTypes {
		if v.Kind == kind {
			return v, true
		}
	}
	return SourceType{}, false
}
func readerSource(kind string) bool { v, ok := sourceType(kind); return ok && v.Reader }
func validSourceCredential(kind string, c Credential) bool {
	if readerSource(kind) && slices.Contains([]string{"mongodb", "redis", "hbase", "elasticsearch", "clickhouse"}, kind) {
		if kind == "mongodb" && (c.Username == "") != (c.Password == "") {
			return false
		}
		if kind == "redis" && c.Username != "" && c.Password == "" {
			return false
		}
		return len(c.Username) <= 128 && len(c.Password) <= 4096
	}
	return valid(c.Username, 128) && valid(c.Password, 4096)
}
func validSourceScheme(kind, scheme string) bool {
	if !slices.Contains([]string{"mongodb", "redis", "hbase", "elasticsearch", "clickhouse"}, kind) {
		return scheme == ""
	}
	return scheme == "" || scheme == "http" || scheme == "https"
}

type readerResponse struct {
	Items   []SourceTable            `json:"items"`
	Fields  []Field                  `json:"fields"`
	Keys    []string                 `json:"keys"`
	Records []map[string]interface{} `json:"records"`
	Total   int                      `json:"total"`
	Passed  bool                     `json:"passed"`
}

func readerSettings() (string, string, error) {
	origin := os.Getenv("SOURCE_READER_ORIGIN")
	if origin == "" {
		origin = "http://source-reader:8093"
	}
	u, e := url.Parse(origin)
	token := os.Getenv("SOURCE_READER_TOKEN")
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(token) < 32 {
		return "", "", problem(503, "SOURCE_READER_UNAVAILABLE", "扩展数据源读取服务尚未配置")
	}
	return origin, token, nil
}
func readerRequest(ctx context.Context, path string, input interface{}, out interface{}) error {
	origin, token, e := readerSettings()
	if e != nil {
		return e
	}
	var raw []byte
	if input != nil {
		raw, e = json.Marshal(input)
		if e != nil {
			return e
		}
	}
	method := "GET"
	if input != nil {
		method = "POST"
	}
	req, e := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return problem(503, "SOURCE_READER_UNAVAILABLE", "扩展数据源读取服务暂不可用")
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 20*1024*1024+1))
	if e != nil {
		return problem(503, "SOURCE_READER_UNAVAILABLE", "读取服务响应中断")
	}
	if len(body) > 20*1024*1024 {
		return problem(409, "SOURCE_READ_LIMIT", "数据超过单次20MiB读取上限，请缩小来源范围")
	}
	if response.StatusCode != 200 {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &failure)
		messages := map[string]string{
			"SOURCE_DRIVER_INVALID":   "厂商驱动或依赖不完整，请由管理员安装匹配版本",
			"SOURCE_DRIVER_MISSING":   "未安装此数据库对应的厂商驱动，请由管理员安装后再试",
			"SOURCE_READ_LIMIT":       "来源超过10000条或20MiB读取上限，请缩小来源范围后重试",
			"SOURCE_OBJECT_LIMIT":     "来源对象超过1000个，请缩小连接所对应的库或命名空间",
			"SOURCE_AUTH_FAILED":      "数据源认证失败，请核对账号、口令及读取权限",
			"SOURCE_CHANGED":          "来源在读取期间发生变化，请重试",
			"SOURCE_TYPE_UNSUPPORTED": "该值类型暂不支持完整读取，请使用已支持的值类型",
			"TABLE_NOT_FOUND":         "来源对象不可见，请核对对象名称及读取权限",
			"TABLE_FIELD_LIMIT":       "来源字段数超出当前读取范围（1–100）",
			"SOURCE_SCOPE_INVALID":    "所选对象不在当前连接的库、索引或命名空间范围内",
		}
		if m, ok := messages[failure.Code]; ok {
			return problem(409, failure.Code, m)
		}
		return problem(503, "SOURCE_UNAVAILABLE", "来源读取失败，请核对网络、连接参数、驱动版本和读取权限")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if e = decoder.Decode(out); e != nil {
		return problem(502, "SOURCE_READER_RESPONSE_INVALID", "读取服务响应格式不正确")
	}
	var trailing interface{}
	if decoder.Decode(&trailing) != io.EOF {
		return problem(502, "SOURCE_READER_RESPONSE_INVALID", "读取服务响应包含多余内容")
	}
	return nil
}
func (rt *Runtime) sourceReader(ctx context.Context, tx *gorm.DB, src SourceSecret, action, schema, table string) (readerResponse, error) {
	out := readerResponse{}
	if !readerSource(src.Kind) || !validSourceScheme(src.Kind, src.Scheme) {
		return out, invalid
	}
	if e := rt.allowSource(ctx, tx, SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database}); e != nil {
		return out, e
	}
	credentials, e := rt.open(src.Ciphertext)
	if e != nil {
		return out, e
	}
	input := map[string]interface{}{"action": action, "kind": src.Kind, "host": src.Host, "port": src.Port, "database": src.Database, "scheme": src.Scheme, "credentials": credentials, "auth_database": src.AuthDatabase, "schema": schema, "table": table, "max_rows": 10000}
	e = readerRequest(ctx, "/v1/source", input, &out)
	return out, e
}
func closeSourceDB(db *sql.DB) {
	if db != nil {
		_ = db.Close()
	}
}
func (s *Server) sourceTables(ctx context.Context, tx *gorm.DB, src SourceSecret, db *sql.DB) ([]SourceTable, error) {
	if !readerSource(src.Kind) {
		return discoverTables(ctx, db, src)
	}
	out, e := s.runtime.sourceReader(ctx, tx, src, "objects", "", "")
	return out.Items, e
}
func (s *Server) sourceTableSchema(ctx context.Context, tx *gorm.DB, src SourceSecret, db *sql.DB, schema, table string) (TableConfig, error) {
	if !readerSource(src.Kind) {
		return discoverTable(ctx, db, src, schema, table)
	}
	out, e := s.runtime.sourceReader(ctx, tx, src, "schema", schema, table)
	cfg := TableConfig{Schema: schema, Table: table, Fields: out.Fields, Keys: out.Keys}
	if e == nil {
		if len(cfg.Fields) < 1 || len(cfg.Fields) > 100 {
			return cfg, invalid
		}
		for _, f := range cfg.Fields {
			if !valid(f.Name, 128) {
				return cfg, invalid
			}
		}
	}
	return cfg, e
}
func (s *Server) externalSourceData(ctx context.Context, tx *gorm.DB, cfg TableConfig) ([]map[string]interface{}, error) {
	row, e := get(tx, "source", cfg.SourceID, false)
	if e != nil {
		return nil, e
	}
	src, e := decode[SourceSecret](row.Body)
	if e != nil {
		return nil, e
	}
	schema, e := s.sourceTableSchema(ctx, tx, src, nil, cfg.Schema, cfg.Table)
	if e != nil {
		return nil, e
	}
	if !reflect.DeepEqual(schema.Fields, cfg.Fields) || !slices.Equal(schema.Keys, cfg.Keys) {
		return nil, problem(409, "SOURCE_SCHEMA_CHANGED", "来源结构已变化，请重新挂载")
	}
	data, e := s.runtime.sourceReader(ctx, tx, src, "read", cfg.Schema, cfg.Table)
	if e != nil {
		return nil, e
	}
	if len(data.Records) != data.Total || data.Total > 10000 {
		return nil, problem(502, "SOURCE_READER_RESPONSE_INVALID", "来源完整行数无法核实")
	}
	for _, record := range data.Records {
		for _, f := range cfg.Fields {
			if _, ok := record[f.Name]; !ok {
				return nil, problem(409, "SOURCE_SCHEMA_CHANGED", "来源记录与结构不一致")
			}
		}
	}
	return data.Records, nil
}
func (s *Server) readExternalTable(ctx context.Context, tx *gorm.DB, cfg TableConfig, fields, after []string, limit int, requireKey bool) ([]map[string]interface{}, []string, error) {
	if limit < 1 || limit > 10001 {
		return nil, nil, invalid
	}
	if requireKey && len(cfg.Keys) == 0 {
		return nil, nil, problem(409, "STABLE_KEY_REQUIRED", "该来源未提供唯一主键；请先生成并发布治理成果，再对外共享")
	}
	if len(after) > 0 && len(after) != len(cfg.Keys) {
		return nil, nil, invalid
	}
	for _, name := range fields {
		if !slices.ContainsFunc(cfg.Fields, func(f Field) bool { return f.Name == name }) {
			return nil, nil, invalid
		}
	}
	records, e := s.externalSourceData(ctx, tx, cfg)
	if e != nil {
		return nil, nil, e
	}
	keys := func(record map[string]interface{}) ([]string, error) {
		out := []string{}
		for _, name := range cfg.Keys {
			v, ok := record[name]
			if !ok || v == nil {
				return nil, problem(409, "STABLE_KEY_REQUIRED", "来源主键不可为空")
			}
			out = append(out, sourceReaderKeyValue(v))
		}
		return out, nil
	}
	indexed := make([]struct {
		record map[string]interface{}
		keys   []string
	}, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		k, err := keys(record)
		if err != nil {
			return nil, nil, err
		}
		if requireKey {
			hash := string(encode(k))
			if seen[hash] {
				return nil, nil, problem(409, "SOURCE_KEY_NOT_UNIQUE", "来源声明的主键不唯一，请先治理后共享")
			}
			seen[hash] = true
		}
		indexed = append(indexed, struct {
			record map[string]interface{}
			keys   []string
		}{record, k})
	}
	sort.SliceStable(indexed, func(i, j int) bool { return slices.Compare(indexed[i].keys, indexed[j].keys) < 0 })
	out := []map[string]interface{}{}
	var last []string
	for _, item := range indexed {
		if len(after) > 0 && slices.Compare(item.keys, after) <= 0 {
			continue
		}
		projected := map[string]interface{}{}
		for _, f := range fields {
			projected[f] = item.record[f]
		}
		out = append(out, projected)
		last = item.keys
		if len(out) == limit {
			break
		}
	}
	return out, last, nil
}
func sourceReaderKeyValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return strings.TrimSpace(string(encode(v)))
}
func (s *Server) sourceTypeCatalog(c *gin.Context) {
	items := slices.Clone(sourceTypes)
	var state struct {
		Items []struct {
			Kind   string `json:"kind"`
			Ready  bool   `json:"ready"`
			Status string `json:"status"`
		} `json:"items"`
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if e := readerRequest(ctx, "/v1/capabilities", nil, &state); e == nil {
		for i := range items {
			for _, v := range state.Items {
				if items[i].Reader && items[i].Kind == v.Kind {
					items[i].Status = v.Status
				}
			}
		}
	}
	c.JSON(200, gin.H{"items": items})
}

func (rt *Runtime) testSourceConnection(ctx context.Context, tx *gorm.DB, src SourceSecret) (*sql.DB, error) {
	if !readerSource(src.Kind) {
		return rt.database(ctx, tx, src)
	}
	out, e := rt.sourceReader(ctx, tx, src, "probe", "", "")
	if e == nil && !out.Passed {
		return nil, problem(503, "SOURCE_UNAVAILABLE", "数据源连接未通过检查")
	}
	return nil, e
}

func validSourceSettings(v SourceInput) bool {
	if !validSourceScheme(v.Kind, v.Scheme) {
		return false
	}
	if v.AuthDatabase != "" && (v.Kind != "mongodb" || !valid(v.AuthDatabase, 128) || strings.TrimSpace(v.AuthDatabase) != v.AuthDatabase || strings.ContainsAny(v.AuthDatabase, "/\\\x00\r\n")) {
		return false
	}
	return true
}
func validReaderDatabase(kind, database string) bool {
	switch kind {
	case "redis":
		n, e := strconv.Atoi(database)
		return e == nil && n >= 0 && n <= 15 && strconv.Itoa(n) == database
	case "hbase":
		return regexp.MustCompile("^[A-Za-z0-9_]+$").MatchString(database)
	case "elasticsearch":
		return database != "." && database != ".." && regexp.MustCompile("^[a-z0-9.][a-z0-9._+-]*$").MatchString(database)
	}
	return true
}
