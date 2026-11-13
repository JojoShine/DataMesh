package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) createSource(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	if e := requireResourceCapability(tx, p, Resource{}, "operate_resource"); e != nil {
		return 0, nil, "", e
	}
	v, e := bind[SourceInput](b)
	if e != nil {
		return 0, nil, "", e
	}
	if !valid(v.Name, 200) || !validSourceCredential(v.Kind, v.Credentials) || !validSourceSettings(v) {
		return 0, nil, "", problem(400, "SOURCE_TEMPLATE_REQUIRED", "请填写管理员已配置允许范围的数据库类型、地址、端口和数据库或服务名")
	}
	if err := s.runtime.allowSource(tx.Statement.Context, tx, v); err != nil {
		return 0, nil, "", err
	}
	if v.OwnerID != p.UserID {
		return 0, nil, "", denied
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	encrypted, e := s.runtime.seal(v.Credentials)
	if e != nil {
		return 0, nil, "", e
	}
	r := Record{Kind: "source", ID: id, DepartmentID: p.DepartmentID, OwnerID: p.UserID, State: "registered", Version: 1, Body: encode(SourceSecret{Name: v.Name, Scheme: v.Scheme, AuthDatabase: v.AuthDatabase, Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database, Ciphertext: encrypted, ConnectionStatus: "untested", CDCStatus: "untested"})}
	if e = tx.Create(&r).Error; e != nil {
		return 0, nil, "", e
	}
	view, e := s.view(tx.Statement.Context, r)
	return 201, view, r.ID, e
}
func (s *Server) updateSource(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	r, e := get(tx, "source", id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "operate_resource"); err != nil {
		return 0, nil, id, err
	}
	var count int64
	if e = tx.Model(&Record{}).Where("(kind='task' AND parent_id=?) OR (kind='table_resource' AND body->'config'->>'source_id'=?) OR (kind='etl_task' AND (body->'source'->>'source_id'=? OR body->'target'->>'source_id'=?))", id, id, id, id).Count(&count).Error; e != nil {
		return 0, nil, id, e
	}
	if count > 0 {
		return 0, nil, id, problem(409, "SOURCE_IN_USE", "来源已关联任务或库表资源，请登记新连接，避免改变已授权的数据范围")
	}
	v, e := bind[SourceInput](b)
	if e != nil {
		return 0, nil, id, e
	}
	if !valid(v.Name, 200) || v.OwnerID != r.OwnerID || !validSourceCredential(v.Kind, v.Credentials) || !validSourceSettings(v) {
		return 0, nil, id, invalid
	}
	if err := s.runtime.allowSource(tx.Statement.Context, tx, v); err != nil {
		return 0, nil, id, err
	}
	encrypted, e := s.runtime.seal(v.Credentials)
	if e != nil {
		return 0, nil, id, e
	}
	r.Body = encode(SourceSecret{Name: v.Name, Scheme: v.Scheme, AuthDatabase: v.AuthDatabase, Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database, Ciphertext: encrypted, ConnectionStatus: "untested", CDCStatus: "untested"})
	r.Version++
	if e = save(tx, &r); e != nil {
		return 0, nil, id, e
	}
	view, e := s.view(tx.Statement.Context, r)
	return 200, view, id, e
}

type preflightCheck struct {
	Check      string   `json:"check"`
	Passed     bool     `json:"passed"`
	Reason     *string  `json:"reason"`
	Suggestion *string  `json:"suggestion"`
	Label      string   `json:"label,omitempty"`
	Status     string   `json:"status,omitempty"`
	Owner      string   `json:"owner,omitempty"`
	Actual     string   `json:"actual,omitempty"`
	Expected   string   `json:"expected,omitempty"`
	Steps      []string `json:"steps,omitempty"`
	VerifySQL  string   `json:"verify_sql,omitempty"`
	query      string
	args       []interface{}
}

func sourceCheck(name string, passed bool, message string) preflightCheck {
	var suggestion *string
	if !passed {
		v := "请核实数据库网络连通性、访问凭据、所需权限及日志条件"
		suggestion = &v
	}
	return preflightCheck{Check: name, Passed: passed, Reason: &message, Suggestion: suggestion}
}
func (s *Server) registerSourceActions(g *gin.RouterGroup) {
	g.POST("/sources/test-connection", s.require("exchange", "operator"), func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		if e := requireResourceCapability(s.db.WithContext(ctx), principal(c), Resource{}, "operate_resource"); e != nil {
			s.fail(c, e)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		var v SourceInput
		if e := c.ShouldBindJSON(&v); e != nil {
			s.fail(c, invalid)
			return
		}
		if err := s.runtime.allowSource(ctx, s.db, v); err != nil {
			s.fail(c, err)
			return
		}
		if !validSourceCredential(v.Kind, v.Credentials) || !validSourceSettings(v) {
			s.fail(c, invalid)
			return
		}
		ciphertext, e := s.runtime.seal(v.Credentials)
		if e != nil {
			s.fail(c, e)
			return
		}
		db, e := s.runtime.testSourceConnection(ctx, s.db, SourceSecret{Kind: v.Kind, Scheme: v.Scheme, AuthDatabase: v.AuthDatabase, Host: v.Host, Port: v.Port, Database: v.Database, Ciphertext: ciphertext})
		passed := e == nil
		reason := "数据库连接成功，尚未保存数据源"
		if passed {
			defer closeSourceDB(db)
		} else {
			reason = "数据库连接失败，请检查网络、地址、端口、数据库或服务名及访问凭据"
			if failure, ok := e.(*Problem); ok && readerSource(v.Kind) {
				reason = failure.Message
			}
		}
		c.JSON(200, gin.H{"source_id": "", "kind": "connection", "passed": passed, "checks": []preflightCheck{sourceCheck("authentication", passed, reason)}, "checked_at": time.Now().UTC()})
	})

	for _, action := range []string{"test-connection", "preflight-cdc"} {
		a := action
		g.POST("/sources/:id/"+a, s.require("exchange", "operator"), s.mutatePath(a, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			scope, err := bind[cdcScope](b)
			if err != nil || !scope.valid() || (a != "preflight-cdc" && (scope.Schema != "" || scope.Table != "")) {
				return 0, nil, id, invalid
			}
			r, e := get(tx, "source", id, true)
			if e != nil {
				return 0, nil, id, e
			}
			if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "operate_resource"); err != nil {
				return 0, nil, id, err
			}
			v, e := decode[SourceSecret](r.Body)
			if e != nil {
				return 0, nil, id, e
			}
			checks := []preflightCheck{}
			passed := true
			if a == "preflight-cdc" {
				ctx, cancel := context.WithTimeout(tx.Statement.Context, 8*time.Second)
				defer cancel()
				checks = s.cdcPreflight(ctx, tx, v, scope)
				for _, check := range checks {
					passed = passed && check.Passed
				}
			} else {
				db, err := s.runtime.testSourceConnection(tx.Statement.Context, tx, v)
				passed = err == nil
				if passed {
					defer closeSourceDB(db)
					checks = append(checks, sourceCheck("authentication", true, "数据库使用所登记凭据连接成功"))
				} else {
					reason := "数据库连接失败，请检查网络、地址、端口、数据库或服务名及访问凭据"
					if failure, ok := err.(*Problem); ok && readerSource(v.Kind) {
						reason = failure.Message
					}
					checks = append(checks, sourceCheck("authentication", false, reason))
				}
			}
			if a == "test-connection" {
				v.ConnectionStatus = statusText(passed)
			} else if scope.Table == "" || !localSourceTemplate(SourceInput{Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database}) || scope.Schema+"."+scope.Table == qualified(v.Kind) {
				v.CDCStatus = statusText(passed)
			}
			r.Body = encode(v)
			if e = save(tx, &r); e != nil {
				return 0, nil, id, e
			}
			if passed && a == "preflight-cdc" {
				object := Object{ObjectID: r.ID + "-customers", SourceID: r.ID, QualifiedName: qualified(v.Kind), SchemaVersion: 1, Fields: sampleFields()}
				obj := Record{Kind: "object", ID: object.ObjectID, DepartmentID: r.DepartmentID, OwnerID: r.OwnerID, ParentID: r.ID, State: "available", Version: 1, Body: encode(object)}
				var exists int64
				if e = tx.Model(&Record{}).Where("kind='object' AND id=?", obj.ID).Count(&exists).Error; e != nil {
					return 0, nil, id, e
				}
				if exists == 0 {
					if e = tx.Create(&obj).Error; e != nil {
						return 0, nil, id, e
					}
				}
			}
			return 200, encode(gin.H{"source_id": id, "kind": map[string]string{"test-connection": "connection", "preflight-cdc": "cdc"}[a], "passed": passed, "checks": checks, "checked_at": time.Now().UTC()}), id, nil
		}))
	}
}
func statusText(passed bool) string {
	if passed {
		return "passed"
	}
	return "failed"
}
func qualified(kind string) string {
	switch kind {
	case "postgres":
		return "public.customers"
	case "mysql":
		return "sample.customers"
	default:
		return "DATAMESH_SOURCE.CUSTOMERS"
	}
}
func sampleFields() []Field {
	precision, scale := 20, 6
	return []Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Nullable: true, Sensitive: true}, {Name: "amount", LogicalType: "decimal", Nullable: true, Precision: &precision, Scale: &scale}}
}
func (s *Server) auditList(c *gin.Context) {
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	q := s.db.WithContext(c.Request.Context()).Table("audit_records")
	for _, key := range []string{"actor_id", "object_id", "result", "action"} {
		if v := c.Query(key); v != "" {
			if len(v) > 128 {
				s.fail(c, invalid)
				return
			}
			q = q.Where(key+"=?", v)
		}
	}
	for _, key := range []string{"from", "to"} {
		if v := c.Query(key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				s.fail(c, invalid)
				return
			}
			op := ">="
			if key == "to" {
				op = "<="
			}
			q = q.Where("created_at "+op+" ?", t)
		}
	}
	if v := c.Query("search"); v != "" {
		if len(v) > 200 {
			s.fail(c, invalid)
			return
		}
		q = q.Where("(trace_id=? OR object_id=? OR action ILIKE ?)", v, v, "%"+v+"%")
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	type row struct {
		ID                                int64
		ActorID                           *string
		Action, ObjectID, Result, TraceID string
		CreatedAt                         time.Time
	}
	var rows []row
	sort := c.DefaultQuery("sort_by", "created_at")
	columns := map[string]string{"audit_id": "id", "created_at": "created_at", "action": "action"}
	column, ok := columns[sort]
	direction := c.DefaultQuery("sort_order", "desc")
	if !ok || (direction != "asc" && direction != "desc") {
		s.fail(c, invalid)
		return
	}
	if e = q.Order(column + " " + direction).Order("id DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []gin.H{}
	for _, r := range rows {
		items = append(items, gin.H{"audit_id": fmt.Sprint(r.ID), "actor_id": r.ActorID, "action": r.Action, "object_id": r.ObjectID, "result": r.Result, "trace_id": r.TraceID, "created_at": r.CreatedAt})
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) accounts(c *gin.Context) {
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	q := s.db.WithContext(c.Request.Context()).Model(&identity.User{})
	if search := c.Query("search"); search != "" {
		if len(search) > 200 {
			s.fail(c, invalid)
			return
		}
		q = q.Where("username ILIKE ? OR display_name ILIKE ?", "%"+search+"%", "%"+search+"%")
	}
	if dept := c.Query("department_id"); dept != "" {
		q = q.Where("department_id=?", dept)
	}
	var total int64
	if e = q.Count(&total).Error; e != nil {
		s.fail(c, e)
		return
	}
	var users []identity.User
	if e = q.Order("username").Limit(size).Offset((page - 1) * size).Find(&users).Error; e != nil {
		s.fail(c, e)
		return
	}
	ids := []string{}
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	var accesses []identity.PlatformAccess
	if len(ids) > 0 {
		if e = s.db.WithContext(c.Request.Context()).Where("user_id IN ?", ids).Order("platform").Find(&accesses).Error; e != nil {
			s.fail(c, e)
			return
		}
	}
	platformMap := map[string][]string{}
	for _, a := range accesses {
		platformMap[a.UserID] = append(platformMap[a.UserID], a.Platform)
	}
	var assignments []identity.UserRole
	if len(ids) > 0 {
		if e = s.db.WithContext(c.Request.Context()).Where("user_id IN ?", ids).Order("role").Find(&assignments).Error; e != nil {
			s.fail(c, e)
			return
		}
	}
	roleMap := map[string][]string{}
	for _, assignment := range assignments {
		roleMap[assignment.UserID] = append(roleMap[assignment.UserID], assignment.Role)
	}
	items := []gin.H{}
	for _, u := range users {
		roles := roleMap[u.ID]
		if roles == nil {
			roles = []string{}
		}
		entries := platformMap[u.ID]
		if entries == nil {
			entries = []string{}
		}
		items = append(items, gin.H{"user_id": u.ID, "username": u.Username, "display_name": u.DisplayName, "version": u.Version, "platforms": entries, "department_id": u.DepartmentID, "active": u.Active, "roles": roles})
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func (s *Server) sourceForObject(ctx context.Context, id string) (Record, SourceSecret, error) {
	obj, e := get(s.db.WithContext(ctx), "object", id, false)
	if e != nil {
		return Record{}, SourceSecret{}, e
	}
	r, e := get(s.db.WithContext(ctx), "source", obj.ParentID, false)
	if e != nil {
		return r, SourceSecret{}, e
	}
	v, e := decode[SourceSecret](r.Body)
	return r, v, e
}

func (s *Server) createAccount(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
	v, e := bind[identity.NewUser](b)
	if e != nil {
		return 0, nil, "", e
	}
	if e = tx.Exec("SELECT pg_advisory_xact_lock(39001)").Error; e != nil {
		return 0, nil, "", e
	}
	var existing identity.OrganizationUnit
	if e = tx.Where("id=?", v.DepartmentID).Take(&existing).Error; e == nil {
		unit, err := identity.ReadAffiliation(tx, v.DepartmentID)
		if err != nil {
			return 0, nil, "", err
		}
		if !unit.Active {
			return 0, nil, "", problem(409, "UNIT_INACTIVE", "请选择在用的组织或部门")
		}
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return 0, nil, "", e
	}
	svc, e := identity.NewService(identity.NewRepository(tx), s.runtime.key)
	if e != nil {
		return 0, nil, "", e
	}
	id, e := svc.CreateUser(tx.Statement.Context, v)
	if e != nil {
		return 0, nil, "", problem(400, "ACCOUNT_INVALID", "账号参数不合法或账号已存在")
	}
	return 201, encode(gin.H{"user_id": id, "username": v.Username, "department_id": v.DepartmentID, "roles": v.Roles, "active": true}), id, nil
}
func (s *Server) disableAccount(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	if id == p.UserID {
		return 0, nil, id, problem(409, "SELF_DISABLE_DENIED", "不能在当前会话禁用自己")
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(39001)").Error; err != nil {
		return 0, nil, id, err
	}
	if err := retainAdministrator(tx, id); err != nil {
		return 0, nil, id, err
	}
	var user identity.User
	if e := tx.Where("id=?", id).Take(&user).Error; e != nil {
		return 0, nil, id, e
	}
	if e := identity.NewRepository(tx).DisableUser(tx.Statement.Context, user.Username); e != nil {
		return 0, nil, id, e
	}
	if err := tx.Model(&identity.User{}).Where("id=?", id).UpdateColumn("version", gorm.Expr("version+1")).Error; err != nil {
		return 0, nil, id, err
	}
	return 200, encode(gin.H{"user_id": id, "active": false}), id, nil
}

// Display metadata may change without replacing a source's in-use connection or credentials.
func (s *Server) updateSourceMetadata(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	v, err := bind[struct {
		Name            string `json:"name"`
		ExpectedVersion int    `json:"expected_version"`
		Reason          string `json:"reason"`
	}](b)
	if err != nil {
		return 0, nil, id, err
	}
	if !valid(v.Name, 200) || !valid(v.Reason, 1000) {
		return 0, nil, id, invalid
	}
	row, err := get(tx, "source", id, true)
	if err != nil {
		return 0, nil, id, err
	}
	if err = requireResourceCapability(tx, p, Resource{Type: "source", ID: id}, "operate_resource"); err != nil {
		return 0, nil, id, err
	}
	if row.Version != v.ExpectedVersion {
		return 0, nil, id, conflict
	}
	value, err := decode[SourceSecret](row.Body)
	if err != nil {
		return 0, nil, id, err
	}
	value.Name = v.Name
	row.Body = encode(value)
	row.Version++
	if err = save(tx, &row); err != nil {
		return 0, nil, id, err
	}
	out, err := s.view(tx.Statement.Context, row)
	return 200, out, id, err
}
