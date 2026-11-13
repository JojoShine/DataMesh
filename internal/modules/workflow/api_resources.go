package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type APIParameter struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}
type APIConfig struct {
	DataService    *DataServiceQuery      `json:"data_service,omitempty"`
	URL            string                 `json:"url"`
	Method         string                 `json:"method"`
	QueryOnly      bool                   `json:"query_only"`
	Parameters     []APIParameter         `json:"parameters"`
	AuthType       string                 `json:"auth_type"`
	AuthHeader     string                 `json:"auth_header"`
	Ciphertext     string                 `json:"ciphertext,omitempty"`
	DataPath       string                 `json:"data_path"`
	ResponseFields map[string]string      `json:"response_fields"`
	TestParameters map[string]interface{} `json:"test_parameters"`
}
type APIInput struct {
	DirectoryID     string    `json:"directory_id"`
	ExpectedVersion int       `json:"expected_directory_version"`
	Config          APIConfig `json:"config"`
	Secret          string    `json:"secret"`
}
type APITarget struct {
	Origin    string   `json:"origin"`
	Addresses []string `json:"addresses"`
}

func apiTransport(ctx context.Context, raw string) (*http.Client, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, invalid
	}
	var targets []APITarget
	if e = json.Unmarshal([]byte(os.Getenv("API_ALLOWED_TARGETS")), &targets); e != nil {
		return nil, problem(403, "API_TARGET_DENIED", "请由管理员配置允许接入的 API 目标")
	}
	var target *APITarget
	for i := range targets {
		if targets[i].Origin == u.Scheme+"://"+u.Host {
			target = &targets[i]
			break
		}
	}
	if target == nil || len(target.Addresses) == 0 {
		return nil, problem(403, "API_TARGET_DENIED", "API 目标或服务器地址未获准接入")
	}
	addresses, e := net.DefaultResolver.LookupHost(ctx, u.Hostname())
	if e != nil {
		return nil, e
	}
	if len(addresses) == 0 {
		return nil, invalid
	}
	for _, a := range addresses {
		if u.Scheme == "http" {
			ip := net.ParseIP(a)
			if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
				return nil, problem(403, "API_HTTPS_REQUIRED", "非本地 API 必须使用 HTTPS")
			}
		}
		if !slices.Contains(target.Addresses, a) {
			return nil, problem(403, "API_ADDRESS_DENIED", "API 解析地址与批准地址不符")
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	pinned := net.JoinHostPort(addresses[0], port)
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, pinned)
	}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
	return &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func jsonPath(value interface{}, path string) (interface{}, bool) {
	if path == "" {
		return value, true
	}
	for _, key := range strings.Split(path, ".") {
		obj, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		value, ok = obj[key]
		if !ok {
			return nil, false
		}
	}
	return value, true
}
func (s *Server) callAPI(ctx context.Context, cfg APIConfig, fields []Field, parameters map[string]interface{}) ([]map[string]interface{}, error) {
	if cfg.DataService != nil {
		rows, _, _, err := s.queryDataService(ctx, cfg.DataService, fields, parameters, "", 0, min(50, cfg.DataService.MaxPageSize))
		return rows, err
	}
	if cfg.Method != "GET" && (cfg.Method != "POST" || !cfg.QueryOnly) {
		return nil, problem(400, "QUERY_API_REQUIRED", "仅允许 GET 或明确声明为查询的 POST")
	}
	values := url.Values{}
	for name := range parameters {
		if !slices.ContainsFunc(cfg.Parameters, func(p APIParameter) bool { return p.Name == name }) {
			return nil, problem(400, "API_PARAMETER_INVALID", "存在未声明的请求参数")
		}
	}
	for _, p := range cfg.Parameters {
		value, exists := parameters[p.Name]
		if !exists {
			if p.Required {
				return nil, problem(400, "API_PARAMETER_REQUIRED", "请填写参数："+p.Name)
			}
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return nil, problem(400, "API_PARAMETER_INVALID", "参数须以文本值填写")
		}
		if _, e := parseImportValue(raw, DirectoryItem{LogicalType: p.Type, Nullable: !p.Required}); e != nil {
			return nil, problem(400, "API_PARAMETER_INVALID", "参数类型不符："+p.Name)
		}
		values.Set(p.Name, raw)
	}
	client, e := apiTransport(ctx, cfg.URL)
	if e != nil {
		return nil, e
	}
	defer client.CloseIdleConnections()
	target := cfg.URL
	var body io.Reader
	if cfg.Method == "GET" {
		if len(values) > 0 {
			target += "?" + values.Encode()
		}
	} else {
		body = bytes.NewReader(encode(parameters))
	}
	req, e := http.NewRequestWithContext(ctx, cfg.Method, target, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/json")
	if cfg.Method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.AuthType != "none" {
		secret, err := s.runtime.open(cfg.Ciphertext)
		if err != nil {
			return nil, err
		}
		if cfg.AuthType == "bearer" {
			req.Header.Set("Authorization", "Bearer "+secret.Password)
		} else if cfg.AuthType == "api_key" {
			req.Header.Set(cfg.AuthHeader, secret.Password)
		} else {
			return nil, invalid
		}
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, problem(502, "API_UNAVAILABLE", "API 请求失败或超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, problem(502, "API_UPSTREAM_FAILED", "上游 API 返回非成功状态")
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(data) > 2<<20 {
		return nil, problem(502, "API_RESPONSE_LIMIT", "API 响应超过 2 MiB")
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if e = decoder.Decode(&value); e != nil {
		return nil, problem(502, "API_RESPONSE_INVALID", "API 未返回有效 JSON")
	}
	if decoder.Decode(new(interface{})) != io.EOF {
		return nil, invalid
	}
	value, ok := jsonPath(value, cfg.DataPath)
	if !ok {
		return nil, problem(502, "API_SCHEMA_CHANGED", "API 数据路径不存在")
	}
	records, ok := value.([]interface{})
	if !ok {
		if _, ok = value.(map[string]interface{}); !ok {
			return nil, problem(502, "API_SCHEMA_CHANGED", "API 数据须为对象或对象数组")
		}
		records = []interface{}{value}
	}
	out := []map[string]interface{}{}
	for _, row := range records {
		record := map[string]interface{}{}
		for _, f := range fields {
			path, ok := cfg.ResponseFields[f.Name]
			if !ok {
				return nil, invalid
			}
			v, ok := jsonPath(row, path)
			if !ok || v == nil {
				if !f.Nullable {
					return nil, problem(502, "API_SCHEMA_CHANGED", "API 必填字段缺失："+f.Name)
				}
				record[f.Name] = nil
				continue
			}
			var raw string
			switch x := v.(type) {
			case string:
				raw = x
			case json.Number:
				raw = x.String()
			case bool:
				raw = strconv.FormatBool(x)
			default:
				return nil, problem(502, "API_SCHEMA_CHANGED", "映射字段必须是标量")
			}
			converted, err := parseImportValue(raw, DirectoryItem{LogicalType: f.LogicalType, Nullable: f.Nullable, Precision: f.Precision, Scale: f.Scale})
			if err != nil {
				return nil, problem(502, "API_SCHEMA_CHANGED", "API 字段类型不符："+f.Name)
			}
			record[f.Name] = converted
		}
		out = append(out, record)
	}
	return out, nil
}

var apiLimiter = struct {
	sync.Mutex
	Entries map[string]struct {
		Start time.Time
		Count int
	}
}{Entries: map[string]struct {
	Start time.Time
	Count int
}{}}

func allowAPICall(key string) bool {
	apiLimiter.Lock()
	defer apiLimiter.Unlock()
	now := time.Now()
	for k, v := range apiLimiter.Entries {
		if now.Sub(v.Start) > time.Minute {
			delete(apiLimiter.Entries, k)
		}
	}
	v := apiLimiter.Entries[key]
	if v.Start.IsZero() {
		v.Start = now
	}
	if v.Count >= 60 {
		return false
	}
	v.Count++
	apiLimiter.Entries[key] = v
	return true
}
func (s *Server) registerAPIResources(api, write *gin.RouterGroup) {
	write.POST("/api-resources", s.require("exchange"), func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		raw, e := io.ReadAll(c.Request.Body)
		if e != nil {
			s.fail(c, invalid)
			return
		}
		input, e := bind[APIInput](raw)
		if e != nil {
			s.fail(c, e)
			return
		}
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		dir, def, _, e := directoryForResource(tx, p, input.DirectoryID)
		if e != nil {
			s.fail(c, e)
			return
		}
		if dir.Version != input.ExpectedVersion || def.Format != "api" || len(def.Items) == 0 {
			s.fail(c, conflict)
			return
		}
		cfg := input.Config
		if cfg.DataService != nil || cfg.Ciphertext != "" || len(cfg.Parameters) > 50 || len(cfg.ResponseFields) != len(def.Items) || !slices.Contains([]string{"none", "bearer", "api_key"}, cfg.AuthType) {
			s.fail(c, invalid)
			return
		}
		seen := map[string]bool{}
		for _, param := range cfg.Parameters {
			if !valid(param.Name, 100) || seen[param.Name] || !slices.Contains([]string{"string", "integer", "decimal", "boolean", "date", "datetime"}, param.Type) {
				s.fail(c, invalid)
				return
			}
			seen[param.Name] = true
		}
		for _, f := range def.Items {
			if !valid(cfg.ResponseFields[f.Name], 300) {
				s.fail(c, invalid)
				return
			}
		}
		if cfg.AuthType == "api_key" {
			h := strings.ToLower(cfg.AuthHeader)
			if !strings.HasPrefix(h, "x-") || !regexp.MustCompile("^[a-z0-9!#$%&'*+.^_`|~-]+$").MatchString(h) {
				s.fail(c, problem(400, "API_AUTH_HEADER_INVALID", "API Key 请求头请使用 X- 开头的自定义头"))
				return
			}
		}
		if cfg.AuthType != "none" {
			if !valid(input.Secret, 4096) || strings.ContainsAny(input.Secret, "\r\n") {
				s.fail(c, invalid)
				return
			}
			cfg.Ciphertext, e = s.runtime.seal(Credential{Password: input.Secret})
			if e != nil {
				s.fail(c, e)
				return
			}
		}
		fields := directoryFields(def)
		if !allowAPICall(p.UserID + ":" + dir.ID) {
			s.fail(c, problem(429, "API_RATE_LIMIT", "调用频率超过限制，请稍后重试"))
			return
		}
		_, e = s.callAPI(c.Request.Context(), cfg, fields, cfg.TestParameters)
		if e != nil {
			s.fail(c, e)
			return
		}
		r, e := newRecord("api_resource", dir.DepartmentID, p.UserID, dir.ID, "ready", ManagedResource{Name: def.Name, DataLayer: def.DataLayer, Fields: fields, Adapter: "api", Config: encode(cfg)})
		if e == nil {
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
				if err = t.Create(&r).Error; err != nil {
					return err
				}
				return s.audit(t, p, "register_api_resource", r.ID, "success", c.GetString("trace_id"))
			})
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(201, gin.H{"resource": Resource{Type: r.Kind, ID: r.ID, Version: 1}, "credential_configured": cfg.Ciphertext != ""})
	})
	write.POST("/api-resources/:id/test", s.require("exchange"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		r, e := get(tx, "api_resource", c.Param("id"), false)
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
		cfg, e := decode[APIConfig](v.Config)
		if e != nil {
			s.fail(c, e)
			return
		}
		if !allowAPICall(p.UserID + ":" + r.ID) {
			s.fail(c, problem(429, "API_RATE_LIMIT", "调用频率超过限制"))
			return
		}
		ref := Resource{Type: r.Kind, ID: r.ID, Version: r.Version}
		if _, e = s.securityPreviewCheck(tx, p, ref, "invoke"); e != nil {
			s.fail(c, e)
			return
		}
		rows, e := s.callAPI(c.Request.Context(), cfg, v.Fields, cfg.TestParameters)
		if e != nil {
			s.fail(c, e)
			return
		}
		maskRows(rows, v.Fields, p)
		decision, e := s.securityPreviewCheck(tx, p, ref, "invoke")
		if e != nil {
			s.fail(c, e)
			return
		}
		securityMaskRows(rows, decision.MaskFields)
		if e = s.audit(tx, p, "test_api_resource", r.ID, "success", c.GetString("trace_id")); e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"records": rows[:min(20, len(rows))]})
	})
}
func (s *Server) invokeAPI(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		s.fail(c, invalid)
		return
	}
	v, e := bind[struct {
		PageSize   int                    `json:"page_size"`
		Cursor     string                 `json:"cursor"`
		Version    int                    `json:"contract_version"`
		Parameters map[string]interface{} `json:"parameters"`
		Fields     []string               `json:"fields"`
	}](raw)
	if e != nil {
		s.fail(c, e)
		return
	}
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	ref := Resource{Type: "api_resource", ID: c.Param("id"), Version: v.Version}
	var grantID string
	var fields []string
	var schema []Field
	check := func() error {
		return tx.Transaction(func(t *gorm.DB) error {
			var err error
			var grant Record
			grant, _, schema, fields, err = s.authorizeDelivery(t, p, ref, DeliveryInvoke, v.Fields)
			grantID = grant.ID
			if err == nil && c.GetString("service_key_id") != "" {
				if grantID != c.GetString("service_key_grant") {
					return denied
				}
				var count int64
				err = t.Model(&DataServiceKey{}).Where("id=? AND NOT revoked AND expires_at>now()", c.GetString("service_key_id")).Count(&count).Error
				if err == nil && count != 1 {
					err = denied
				}
			}
			return err
		})
	}
	if e = check(); e != nil {
		s.fail(c, e)
		return
	}
	authorizedGrant, authorizedFields := grantID, fingerprint(encode(fields))
	if !allowAPICall(p.UserID + ":" + ref.ID) {
		c.Header("Retry-After", "60")
		s.fail(c, problem(429, "API_RATE_LIMIT", "每分钟最多调用 60 次，请稍后重试"))
		return
	}
	_, r, e := managedResource(tx, ref, false)
	if e != nil {
		s.fail(c, e)
		return
	}
	cfg, e := decode[APIConfig](r.Config)
	if e != nil {
		s.fail(c, e)
		return
	}
	decision, err := s.securityPolicyCheck(tx, p, ref, "invoke")
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Set("security_row_filter_basis", fingerprint(encode(decision.RowFilters)))
	var rows []map[string]interface{}
	nextCursor, actual := "", ""
	if cfg.DataService != nil {
		size := v.PageSize
		if size == 0 {
			size = min(50, cfg.DataService.MaxPageSize)
		}
		offset, pinned := 0, ""
		scope := fingerprint(encode(gin.H{"fields": fields, "parameters": v.Parameters, "user": p.UserID, "row_filters": decision.RowFilters}))
		if v.Cursor != "" {
			cur, err := s.parseCursor(v.Cursor)
			if err != nil || cur.Grant != grantID || cur.Resource != ref || cur.Fields != scope || len(cur.Keys) != 1 {
				s.fail(c, invalid)
				return
			}
			offset = cur.Offset
			pinned = cur.Keys[0]
		}
		var more bool
		rows, actual, more, e = s.queryDataService(c.Request.Context(), cfg.DataService, r.Fields, v.Parameters, pinned, offset, size, decision.RowFilters)
		if e == nil && more {
			nextCursor, e = s.signCursor(Cursor{Grant: grantID, Resource: ref, Fields: scope, Keys: []string{actual}, Offset: offset + len(rows)})
		}
	} else {
		if v.Cursor != "" || v.PageSize != 0 {
			s.fail(c, invalid)
			return
		}
		rows, e = s.callAPI(c.Request.Context(), cfg, r.Fields, v.Parameters)
	}
	if e != nil {
		s.fail(c, e)
		return
	}
	for i, row := range rows {
		filtered := map[string]interface{}{}
		for _, field := range fields {
			filtered[field] = row[field]
		}
		rows[i] = filtered
	}
	maskRows(rows, schema, p)
	if e = check(); e != nil {
		s.fail(c, e)
		return
	}
	if grantID != authorizedGrant || fingerprint(encode(fields)) != authorizedFields {
		s.fail(c, problem(409, "AUTHORIZATION_CHANGED", "查询期间授权发生变化，请重新请求"))
		return
	}
	finalDecision, err := s.securityPolicyCheck(tx, p, ref, "invoke")
	if err != nil {
		s.fail(c, err)
		return
	}
	if fingerprint(encode(finalDecision.RowFilters)) != fingerprint(encode(decision.RowFilters)) {
		s.fail(c, problem(409, "SECURITY_FILTER_CHANGED", "查询期间行过滤条件已变化"))
		return
	}
	decision = finalDecision
	if cfg.DataService != nil && actual != "" {
		sourceRecord, err := get(tx, "dataset", actual, false)
		if err != nil {
			s.fail(c, err)
			return
		}
		sourceDecision, err := s.securityPolicyCheck(tx, p, Resource{Type: "governed_dataset", ID: actual, Version: sourceRecord.Version}, "invoke")
		if err != nil {
			s.fail(c, err)
			return
		}
		for _, control := range sourceDecision.Controls {
			if !slices.Contains(decision.Controls, control) {
				s.fail(c, problem(409, "SECURITY_SERVICE_SOURCE_CHANGED", "分页绑定的来源保护已变化，请重新核对接口与来源策略"))
				return
			}
		}
		securityMaskRows(rows, sourceDecision.MaskFields)
	}
	securityMaskRows(rows, decision.MaskFields)
	payload := encode(gin.H{"resource": ref, "records": rows})
	if cfg.DataService != nil {
		payload = encode(gin.H{"resource": ref, "records": rows, "next_cursor": nextCursor, "dataset_id": actual})
	}
	if err = s.securitySend(c, p, ref, "invoke", grantID, fields, payload, "application/json"); err != nil && !c.Writer.Written() {
		s.fail(c, err)
	}
}

func (s *Server) apiDeliverySchema(c *gin.Context) {
	version, err := strconv.Atoi(c.Query("contract_version"))
	if err != nil || version < 1 {
		s.fail(c, invalid)
		return
	}
	ref := Resource{Type: "api_resource", ID: c.Param("id"), Version: version}
	var params []APIParameter
	var service *DataServiceQuery
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if _, _, _, _, e := s.authorizeDelivery(tx, principal(c), ref, DeliveryInvoke, nil); e != nil {
			return e
		}
		_, resource, e := managedResource(tx, ref, false)
		if e != nil {
			return e
		}
		cfg, e := decode[APIConfig](resource.Config)
		params = cfg.Parameters
		service = cfg.DataService
		return e
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	out := gin.H{"parameters": params, "data_service": service != nil}
	if service != nil {
		out["max_page_size"] = service.MaxPageSize
		out["mode"] = service.Mode
	}
	c.JSON(200, out)
}
