package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Problem struct {
	Status        int
	Code, Message string
}

func (p *Problem) Error() string                     { return p.Code }
func problem(status int, code, message string) error { return &Problem{status, code, message} }

var invalid = &Problem{400, "INVALID_INPUT", "请检查参数、字段范围和版本"}
var denied = &Problem{403, "FORBIDDEN", "当前账号无权执行此操作"}
var conflict = &Problem{409, "STATE_CONFLICT", "当前状态不允许执行此操作，请刷新后核对"}

type Server struct {
	db      *gorm.DB
	runtime *Runtime
	work    chan struct{}
	etl     etlLimits
}

func New(db *gorm.DB) (*Server, error) {
	rt, e := newRuntime()
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = db.WithContext(ctx).Model(&Operation{}).Where("status IN ? AND NOT (response ? 'generation') AND NOT (response ? 'governed_check_id') AND NOT (response ? 'reconciliation_id')", []string{"accepted", "running"}).Select("status", "response", "updated_at").Updates(Operation{Status: "unknown", Response: encode(gin.H{"code": "SERVER_RESTARTED", "message": "服务重启，请核对实际作业；不自动重发"}), UpdatedAt: time.Now().UTC()}).Error; e != nil {
		return nil, e
	}
	limits, e := loadETLLimits()
	if e != nil {
		return nil, e
	}
	server := &Server{db: db, runtime: rt, work: make(chan struct{}, 2), etl: limits}
	if e = server.etlBackfillTargets(ctx); e != nil {
		return nil, e
	}
	if e = server.etlRecovery(ctx); e != nil {
		return nil, e
	}
	return server, nil
}
func principal(c *gin.Context) identity.Principal {
	v, _ := c.Get("principal")
	p, _ := v.(identity.Principal)
	return p
}
func admin(p identity.Principal) bool { return slices.Contains(p.Roles, "admin") }
func role(p identity.Principal, roles ...string) bool {
	if admin(p) {
		return true
	}
	for _, r := range roles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}
func platform(p identity.Principal, s string) bool {
	return admin(p) || slices.Contains(p.Platforms, s)
}
func owned(p identity.Principal, r Record) bool {
	return admin(p) || (p.DepartmentID == r.DepartmentID && p.UserID == r.OwnerID)
}
func department(p identity.Principal, r Record) bool {
	return admin(p) || p.DepartmentID == r.DepartmentID
}
func (s *Server) require(module string, roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := principal(c)
		if !platform(p, module) || (len(roles) > 0 && !role(p, roles...)) {
			s.fail(c, denied)
			return
		}
		c.Next()
	}
}
func (s *Server) audit(tx *gorm.DB, p identity.Principal, action, id, result, trace string) error {
	return tx.Create(&identity.AuditRecord{ActorID: &p.UserID, Action: action, ObjectID: id, Result: result, TraceID: trace}).Error
}
func (s *Server) fail(c *gin.Context, err error) {
	status, code, message := 503, "SERVICE_UNAVAILABLE", "服务暂不可用，未将失败视为成功"
	var p *Problem
	var databaseError *pgconn.PgError
	if errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &databaseError) && databaseError.Code == "23505") {
		err = conflict
	}
	if errors.As(err, &p) {
		status, code, message = p.Status, p.Code, p.Message
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code, message = 404, "NOT_FOUND", "对象不存在或不可见"
	}
	if principal(c).UserID != "" && status >= 400 {
		result := "failed"
		if status == 403 {
			result = "denied"
		}
		auditCtx := c.Request.Context()
		if auditCtx.Err() != nil {
			sqlState := ""
			if errors.As(err, &databaseError) {
				sqlState = databaseError.Code
			}
			slog.Warn("workflow request expired", "route", c.FullPath(), "code", code, "context_error", auditCtx.Err(), "sql_state", sqlState)
			// Failure audits remain mandatory even when a caller's work budget is
			// exhausted. This context can only record failure/denial, never resume work.
			var cancel context.CancelFunc
			auditCtx, cancel = context.WithTimeout(context.WithoutCancel(auditCtx), 3*time.Second)
			defer cancel()
		}
		if e := s.audit(s.db.WithContext(auditCtx), principal(c), c.Request.Method+" "+c.FullPath(), c.Param("id"), result, c.GetString("trace_id")); e != nil {
			sqlState := ""
			if errors.As(err, &databaseError) {
				sqlState = databaseError.Code
			}
			slog.Warn("workflow failure audit unavailable", "route", c.FullPath(), "code", code, "context_error", c.Request.Context().Err(), "sql_state", sqlState)
			status, code, message = 503, "AUDIT_UNAVAILABLE", "无法记录访问审计"
		}
	}
	identity.Error(c, status, code, message)
}
func bind[T any](data []byte) (T, error) {
	var v T
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, invalid
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		return v, invalid
	}
	return v, nil
}
func valid(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }
func intQuery(c *gin.Context, key string, defaultValue, max int) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return defaultValue, nil
	}
	v, e := strconv.Atoi(raw)
	if e != nil || v < 1 || v > max {
		return 0, invalid
	}
	return v, nil
}

type mutation func(*gorm.DB, identity.Principal, []byte) (int, json.RawMessage, string, error)

func (s *Server) mutate(action string, fn mutation) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := principal(c)
		media, _, parseErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if parseErr != nil || media != "application/json" {
			s.fail(c, problem(415, "INVALID_CONTENT_TYPE", "仅接受 JSON 请求"))
			return
		}
		key := c.GetHeader("Idempotency-Key")
		if !valid(key, 128) {
			s.fail(c, invalid)
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024))
		if e != nil {
			s.fail(c, invalid)
			return
		}
		if len(body) == 0 {
			body = []byte("{}")
		} else if !json.Valid(body) {
			s.fail(c, invalid)
			return
		}
		hashPath := c.Request.URL.Path
		if scope := workspaceScope(c.Request.Context()); scope.ID != "" {
			hashPath += "?workspace_id=" + scope.ID
		}
		hash := sha256.Sum256(append([]byte(c.Request.Method+" "+hashPath+"\n"), body...))
		hashText := hex.EncodeToString(hash[:])
		var op Operation
		e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			id, err := newID()
			if err != nil {
				return err
			}
			candidate := Operation{ID: id, ActorID: p.UserID, IdempotencyKey: key, RequestHash: hashText, Status: "accepted", Response: json.RawMessage("null")}
			if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "actor_id"}, {Name: "idempotency_key"}}, DoNothing: true}).Create(&candidate).Error; err != nil {
				return err
			}
			if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("actor_id=? AND idempotency_key=?", p.UserID, key).Take(&op).Error; err != nil {
				return err
			}
			if op.RequestHash != hashText {
				return problem(409, "IDEMPOTENCY_CONFLICT", "同一个请求编号对应了不同内容")
			}
			if op.ID != id {
				return s.authorizeOperationRead(tx, p, op)
			}
			scopedTx, payload, err := governanceWorkspaceEnvelope(tx, c.Request.URL.Path, body)
			if err != nil {
				return err
			}
			status, response, resource, err := fn(scopedTx, p, payload)
			if err != nil {
				return err
			}
			op.ResponseStatus = status
			op.Response = response
			op.ResourceID = resource
			if status != 202 {
				op.Status = "succeeded"
			}
			if err = tx.Save(&op).Error; err != nil {
				return err
			}
			return s.audit(tx, p, action, resource, "success", c.GetString("trace_id"))
		})
		if e != nil {
			s.fail(c, e)
			return
		}
		if op.ResponseStatus == 202 {
			c.JSON(202, op)
		} else {
			c.Data(op.ResponseStatus, "application/json; charset=utf-8", op.Response)
		}
	}
}
func (s *Server) operation(c *gin.Context) {
	var op Operation
	e := s.db.WithContext(c.Request.Context()).Where("id=? AND actor_id=?", c.Param("id"), principal(c).UserID).Take(&op).Error
	if e != nil {
		s.fail(c, e)
		return
	}
	if err := s.authorizeOperationRead(s.db.WithContext(c.Request.Context()), principal(c), op); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, op)
}
func (s *Server) authorizeOperationRead(tx *gorm.DB, p identity.Principal, op Operation) error {
	if strings.HasPrefix(op.IdempotencyKey, "sandbox:") && !strings.HasPrefix(op.IdempotencyKey, sandboxOperationPrefix(p)) {
		return denied
	}

	if strings.HasPrefix(op.IdempotencyKey, "lightweight:") && !strings.HasPrefix(op.IdempotencyKey, lightweightOperationPrefix(p)) {
		return denied
	}

	if op.ResourceID != "" {
		var private Record
		lookup := tx.Where("id=? AND kind IN ?", op.ResourceID, []string{lightweightUnitKind, lightweightUploadKind}).Take(&private).Error
		if lookup == nil {
			_, err := lightweightPrivateRecord(tx, p, private.Kind, private.ID, false)
			return err
		} else if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		var run GovernanceRun
		lookup = tx.Where("id=?", op.ResourceID).Take(&run).Error
		if lookup == nil && lightweightRunMarker(run) {
			if _, err := s.lightweightRunRead(tx, p, run.ID, false); err != nil {
				return err
			}
		} else if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
	}

	return nil
}
func (s *Server) list(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := principal(c)
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
		search := c.Query("search")
		if len(search) > 200 {
			s.fail(c, invalid)
			return
		}
		q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind=?", kind)
		if !admin(p) && !(kind == "grant" && platform(p, "security") && explicitRole(p, "auditor")) {
			switch kind {
			case "dataset":
				q = q.Where("state='published' OR department_id=? OR (owner_id=? AND EXISTS(SELECT 1 FROM governance_outputs o JOIN governance_runs r ON r.id=o.run_id WHERE o.dataset_id=business_records.id AND r.actor_id=?))", p.DepartmentID, p.UserID, p.UserID)
			case "application", "grant":
				q = q.Where("owner_id=? OR department_id=?", p.UserID, p.DepartmentID)
			case "object":
				if !role(p, "operator", "governor", "provider") {
					q = q.Where("state='available'")
				} else {
					q = q.Where("department_id=?", p.DepartmentID)
				}
			default:
				q = q.Where("department_id=?", p.DepartmentID)
			}
		}
		if kind == "dataset" && workspaceScope(c.Request.Context()).ID != "" {
			var err error
			q, err = s.governanceWorkspaceFilter(c, q, "COALESCE((SELECT r.workspace_id FROM governance_runs r JOIN governance_outputs o ON o.run_id=r.id WHERE o.dataset_id=business_records.id),(SELECT l.workspace_id FROM governance_workspace_legacy_datasets l WHERE l.dataset_id=business_records.id))", false)
			if err != nil {
				s.fail(c, err)
				return
			}
		}
		if c.GetBool("asset_consumer") && (kind == "application" || kind == "grant") {
			q = q.Where("owner_id=?", p.UserID)
		}
		if kind == "application" {
			switch c.Query("scope") {
			case "", "all":
			case "mine":
				q = q.Where("owner_id=?", p.UserID)
			case "pending":
				if !role(p, "provider") {
					s.fail(c, denied)
					return
				}
				q = q.Where("state='submitted'")
				if !admin(p) {
					q = q.Where("owner_id<>?", p.UserID)
				}
			case "processed":
				if !role(p, "provider") {
					s.fail(c, denied)
					return
				}
				q = q.Where("state<>'submitted'")
			default:
				s.fail(c, invalid)
				return
			}
		}
		if search != "" {
			q = q.Where("(body->>'name' ILIKE ? OR body->'definition'->>'name' ILIKE ? OR id=?)", "%"+search+"%", "%"+search+"%", search)
		}
		if parent := c.Param("id"); parent != "" {
			q = q.Where("parent_id=?", parent)
		}
		if kind == "object" {
			if parent := c.Query("source_id"); parent != "" {
				q = q.Where("parent_id=?", parent)
			}
		}
		sort := c.DefaultQuery("sort_by", kind+"_id")
		allowed := map[string]string{kind + "_id": "id", "created_at": "created_at", "state": "state", "version": "version", "name": "body->>'name'", "kind": "body->>'kind'", "approved_at": "created_at", "qualified_name": "body->>'qualified_name'"}
		order, ok := allowed[sort]
		direction := c.DefaultQuery("sort_order", "asc")
		if !ok || (direction != "asc" && direction != "desc") {
			s.fail(c, invalid)
			return
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		var rows []Record
		if e = q.Order(order + " " + direction).Order("id ASC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := make([]json.RawMessage, 0, len(rows))
		for _, r := range rows {
			v, err := s.view(c.Request.Context(), r)
			if err != nil {
				s.fail(c, err)
				return
			}
			if c.GetBool("asset_consumer") && (kind == "application" || kind == "grant") {
				v, err = assetConsumptionView(s.db.WithContext(c.Request.Context()), r, v)
				if err != nil {
					s.fail(c, err)
					return
				}
			}
			items = append(items, v)
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	}
}
func (s *Server) detail(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, e := get(s.db.WithContext(c.Request.Context()), kind, c.Param("id"), false)
		if e == nil {
			p := principal(c)
			if kind == "dataset" {
				if r.State != "published" && !department(p, r) {
					var allowed bool
					allowed, e = governanceDatasetCanRead(s.db.WithContext(c.Request.Context()), p, r)
					if e == nil && !allowed {
						e = denied
					}
				}
			} else if kind == "application" || kind == "grant" {
				if !department(p, r) && r.OwnerID != p.UserID {
					e = denied
				}
			} else if !department(p, r) {
				e = denied
			}
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		v, e := s.view(c.Request.Context(), r)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.Data(200, "application/json", v)
	}
}
func (s *Server) view(ctx context.Context, r Record) (json.RawMessage, error) {
	switch r.Kind {
	case "source":
		v, e := decode[SourceSecret](r.Body)
		if e != nil {
			return nil, e
		}
		return encode(gin.H{"source_id": r.ID, "name": v.Name, "kind": v.Kind, "scheme": v.Scheme, "auth_database": v.AuthDatabase, "owner_id": r.OwnerID, "department_id": r.DepartmentID, "source_version": r.Version, "capture_template": localSourceTemplate(SourceInput{Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database}), "configured_version": map[string]string{"postgres": "17.11", "mysql": "8.4.7", "oracle": "23.26.3"}[v.Kind], "credential_configured": v.Ciphertext != "", "connection_status": v.ConnectionStatus, "cdc_status": v.CDCStatus}), nil
	case "object":
		return r.Body, nil
	case "task":
		return s.taskView(ctx, r)
	case "rule":
		v, e := decode[RuleData](r.Body)
		return encode(gin.H{"rule_id": r.ID, "version": r.Version, "state": r.State, "definition": v.Definition, "created_at": r.CreatedAt}), e
	case "dataset":
		v, e := decode[DatasetData](r.Body)
		if e != nil {
			return nil, e
		}
		space, err := governanceDatasetWorkspace(s.db.WithContext(ctx), r.ID)
		if err != nil {
			return nil, err
		}
		if err = governanceWorkspaceMatch(ctx, space); err != nil {
			return nil, err
		}
		return encode(gin.H{"workspace_id": space, "dataset_id": r.ID, "governance_run_id": v.GovernanceRunID, "data_layer": v.DataLayer, "storage": v.Storage, "governance": v.Governance, "contract_version": r.Version, "provider_department_id": r.DepartmentID, "definition": v.Definition, "publication_status": r.State, "quality_status": v.QualityStatus, "completeness_status": v.CompletenessStatus, "latest_watermark": v.LatestWatermark}), e
	case "application":
		v, e := decode[ApplicationData](r.Body)
		return encode(gin.H{"application_id": r.ID, "applicant_id": v.ApplicantID, "state": r.State, "request": v.Request, "created_at": r.CreatedAt, "decision_reason": v.DecisionReason, "sharing_snapshot": v.Snapshot, "supplements": v.Supplements}), e
	case "grant":
		v, e := decode[GrantData](r.Body)
		state := r.State
		if state == "active" && time.Now().After(v.Scope.ExpiresAt) {
			state = "expired"
		}
		return encode(gin.H{"grant_id": r.ID, "application_id": v.ApplicationID, "grantee_user_id": v.GranteeUserID, "scope": v.Scope, "state": state, "approved_by": v.ApprovedBy, "approved_at": v.ApprovedAt, "sharing_snapshot": v.Snapshot}), e
	}
	return nil, invalid
}
func (s *Server) Register(r *gin.Engine, h *identity.Handler) {
	s.registerStreamScriptBridge(r)
	r.Use(func(c *gin.Context) {
		if _, e := url.ParseQuery(c.Request.URL.RawQuery); e != nil {
			s.fail(c, invalid)
			return
		}
		c.Next()
	})
	r.GET("/api/public/assets", s.publicAssets)
	r.GET("/api/public/assets/:id", s.publicAsset)
	api := r.Group("/api/v1", h.Authenticate, s.governanceWorkspaceContext)
	api.GET("/operations/:id", s.operation)
	api.GET("/source-types", s.require("exchange"), s.sourceTypeCatalog)
	for _, k := range []struct {
		kind, path, platform string
		roles                []string
	}{{"source", "sources", "exchange", []string{"operator"}}, {"task", "tasks", "exchange", []string{"operator"}}, {"object", "resources/raw", "exchange", nil}, {"rule", "rules", "governance", []string{"governor"}}, {"dataset", "datasets", "assets", nil}, {"application", "applications", "exchange", nil}, {"grant", "grants", "exchange", nil}} {
		g := api.Group("/"+k.path, s.require(k.platform, k.roles...))
		g.GET("", s.list(k.kind))
		if k.kind != "object" {
			g.GET("/:id", s.detail(k.kind))
		}
	}
	api.GET("/sources/:id/objects", s.require("exchange", "operator"), s.list("object"))
	write := api.Group("", h.CSRF)
	write.POST("/sources", s.require("exchange", "operator"), s.mutate("create_source", s.createSource))
	write.POST("/sources/:id/metadata", s.require("exchange", "operator"), s.mutatePath("update_source_metadata", s.updateSourceMetadata))
	write.PATCH("/sources/:id", s.require("exchange", "operator"), s.mutatePath("update_source", s.updateSource))
	// Path values are passed explicitly; no handler stores request context in shared state.
	s.registerSourceActions(write)
	s.registerSourceAllowlist(api, write)
	write.POST("/cdc-tasks/validate", s.require("exchange", "operator"), s.mutate("validate_cdc_task", s.validateCDCTask))
	write.POST("/cdc-tasks", s.require("exchange", "operator"), s.mutate("create_cdc_task", s.createCDCTask))
	write.POST("/tasks", s.require("exchange", "operator"), s.mutate("create_task", s.createTask))
	for _, action := range []string{"start", "pause", "resume", "reconcile"} {
		a := action
		write.POST("/tasks/:id/"+a, s.require("exchange", "operator"), s.taskAction(a))
	}
	write.POST("/tasks/:id/replay", s.require("exchange", "operator"), func(c *gin.Context) {
		s.fail(c, problem(409, "REPLAY_REQUIRES_VALIDATED_ORDER", "当前模板尚未验证旧版本重放保护，禁止直接重放"))
	})
	s.registerETL(api, write)
	s.registerGovernance(api, write)
	s.registerExchange(api, write)
	s.registerAssets(api, write)
	s.registerSecurity(api, write)
	s.registerResourceAccess(api, write)
	s.registerOrganization(api, write)
	s.registerDirectoryCoding(api, write)
	s.registerResourceDirectory(api, write)
	s.registerResourceMount(api, write)
	s.registerSourceTables(api, write)
	s.registerManagedResources(api)
	s.registerFileResources(api, write)
	s.registerExcelImports(api, write)
	s.registerAPIResources(api, write)
	s.registerDataServices(api, write)
	s.registerApplicationSupplements(write)
	api.GET("/audit", s.require("security", "auditor"), s.auditList)
	api.GET("/accounts", s.require("security", "admin"), s.accounts)
	api.GET("/catalog/objects", s.require("assets", "provider"), s.list("object"))
	api.GET("/metadata", s.require("governance", "governor"), s.list("object"))
	api.GET("/governance/datasets", s.require("governance", "governor"), s.list("dataset"))
	api.GET("/governance/datasets/:id", s.require("governance", "governor"), s.detail("dataset"))
	api.GET("/security/grants", s.require("security", "auditor"), s.list("grant"))
	write.POST("/accounts", s.require("security", "admin"), s.mutate("create_account", s.createAccount))
	write.POST("/accounts/:id/disable", s.require("security", "admin"), s.mutatePath("disable_account", s.disableAccount))
}

// mutatePath captures only the current request's path parameter.
func (s *Server) mutatePath(action string, fn func(*gorm.DB, identity.Principal, string, []byte) (int, json.RawMessage, string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		s.mutate(action, func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
			return fn(tx, p, id, b)
		})(c)
	}
}
