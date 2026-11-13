package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ETLMapping struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Rule   string `json:"rule"`
}
type ETLDefinition struct {
	Name           string       `json:"name"`
	SourceResource *Resource    `json:"source_resource,omitempty"`
	Source         TableConfig  `json:"source"`
	Target         TableConfig  `json:"target"`
	Mapping        []ETLMapping `json:"mapping"`
}
type ETLBatch struct {
	First int    `json:"first"`
	Last  int    `json:"last"`
	State string `json:"state"`
}
type ETLTimings struct {
	PrepareMS       int64 `json:"prepare_ms"`
	ReadTransformMS int64 `json:"read_transform_ms"`
	WriteMS         int64 `json:"write_ms"`
	IndexMS         int64 `json:"index_ms"`
	BeforeCheckMS   int64 `json:"before_check_ms"`
	CommitMS        int64 `json:"commit_ms"`
	TotalMS         int64 `json:"total_ms"`
}
type ETLRun struct {
	Timings            *ETLTimings `json:"timings,omitempty"`
	securityMaskFields []string
	securityRowFilters []SecurityRowFilter
	delivery           *etlDeliverySession
	authorization      *ETLAuthorizationBasis
	runID              string
	InputSnapshot      *ETLInputSnapshot `json:"input_snapshot,omitempty"`
	leaseOwner         string
	Definition         ETLDefinition  `json:"definition"`
	Read               int            `json:"read"`
	Written            int            `json:"written"`
	Planned            int            `json:"planned"`
	Bytes              int64          `json:"bytes"`
	Batches            []ETLBatch     `json:"batches"`
	Message            string         `json:"message"`
	TaskVersion        int            `json:"task_version"`
	Attempt            int            `json:"attempt"`
	RetryOf            string         `json:"retry_of,omitempty"`
	TraceID            string         `json:"trace_id"`
	ErrorCode          string         `json:"error_code,omitempty"`
	StartedAt          *time.Time     `json:"started_at,omitempty"`
	FinishedAt         *time.Time     `json:"finished_at,omitempty"`
	Resolution         *ETLResolution `json:"resolution,omitempty"`
}

func transformETL(value interface{}, rule string) (interface{}, error) {
	if !slices.Contains([]string{"copy", "trim", "upper", "lower"}, rule) {
		return nil, invalid
	}
	if value == nil {
		return nil, nil
	}
	if rule == "copy" {
		if raw, ok := value.([]byte); ok {
			return string(raw), nil
		}
		return value, nil
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return nil, invalid
	}
	switch rule {
	case "trim":
		return strings.TrimSpace(text), nil
	case "upper":
		return strings.ToUpper(text), nil
	default:
		return strings.ToLower(text), nil
	}
}
func validateETLMapping(mapping []ETLMapping, source, target TableConfig) error {
	if len(source.Keys) == 0 {
		return problem(400, "ETL_SOURCE_KEY_REQUIRED", "来源表必须具有主键以保证稳定的读取顺序")
	}
	if len(mapping) == 0 || len(mapping) > 100 || len(target.Keys) == 0 {
		return problem(400, "ETL_MAPPING_INVALID", "请选择字段映射，目标表必须具有主键")
	}
	seen := map[string]bool{}
	for _, m := range mapping {
		a := slices.IndexFunc(source.Fields, func(f Field) bool { return f.Name == m.Source })
		b := slices.IndexFunc(target.Fields, func(f Field) bool { return f.Name == m.Target })
		if a < 0 || b < 0 || seen[m.Target] || !slices.Contains([]string{"copy", "trim", "upper", "lower"}, m.Rule) {
			return invalid
		}
		from, to := source.Fields[a], target.Fields[b]
		if from.LogicalType == "unsupported" || from.LogicalType != to.LogicalType || (m.Rule != "copy" && from.LogicalType != "string") {
			return problem(400, "ETL_TYPE_MISMATCH", "映射字段类型必须一致，文本转换仅适用于字符串")
		}
		if from.LogicalType == "decimal" && !etlDecimalFits(from, to) {
			return problem(400, "ETL_PRECISION_LOSS", "目标小数精度或范围不足，请选择无损目标字段")
		}
		seen[m.Target] = true
	}
	for _, key := range target.Keys {
		if !seen[key] {
			return problem(400, "ETL_KEY_REQUIRED", "必须映射目标表全部主键字段")
		}
	}
	return nil
}
func etlDecimalFits(from, to Field) bool {
	if to.Precision == nil && to.Scale == nil {
		return true
	}
	if to.Scale == nil {
		return from.Scale == nil && from.Precision != nil && to.Precision != nil && *from.Precision <= *to.Precision
	}
	if from.Scale == nil || from.Precision == nil || to.Precision == nil {
		return false
	}
	return *to.Scale >= *from.Scale && *to.Precision-*to.Scale >= *from.Precision-*from.Scale
}
func etlInsert(kind string, target TableConfig, mapping []ETLMapping) string {
	return etlBatchInsert(kind, target, mapping, 1)
}
func etlBatchInsert(kind string, target TableConfig, mapping []ETLMapping, count int) string {
	if count < 1 || count > 500 || len(mapping) == 0 || count*len(mapping) > 50000 {
		return ""
	}
	columns, values, updates, keys := []string{}, []string{}, []string{}, []string{}
	for _, m := range mapping {
		col := quoteIdentifier(kind, m.Target)
		columns = append(columns, col)
		if !slices.Contains(target.Keys, m.Target) {
			if kind == "mysql" {
				updates = append(updates, col+"=VALUES("+col+")")
			} else {
				updates = append(updates, col+"=EXCLUDED."+col)
			}
		}
	}
	groups := []string{}
	for row := 0; row < count; row++ {
		values = nil
		for col := range mapping {
			if kind == "mysql" {
				values = append(values, "?")
			} else {
				values = append(values, fmt.Sprintf("$%d", row*len(mapping)+col+1))
			}
		}
		groups = append(groups, "("+strings.Join(values, ",")+")")
	}
	q := "INSERT INTO " + quoteIdentifier(kind, target.Schema) + "." + quoteIdentifier(kind, target.Table) + " (" + strings.Join(columns, ",") + ") VALUES " + strings.Join(groups, ",")
	if kind == "mysql" {
		if len(updates) == 0 {
			col := quoteIdentifier(kind, target.Keys[0])
			updates = append(updates, col+"="+col)
		}
		return q + " ON DUPLICATE KEY UPDATE " + strings.Join(updates, ",")
	}
	for _, key := range target.Keys {
		keys = append(keys, quoteIdentifier(kind, key))
	}
	q += " ON CONFLICT (" + strings.Join(keys, ",") + ") DO "
	if len(updates) == 0 {
		return q + "NOTHING"
	}
	return q + "UPDATE SET " + strings.Join(updates, ",")
}
func etlView(r Record) json.RawMessage {
	return encode(gin.H{"id": r.ID, "version": r.Version, "task_id": r.ParentID, "state": r.State, "created_at": r.CreatedAt, "updated_at": r.UpdatedAt, "data": json.RawMessage(r.Body)})
}
func (s *Server) etlPermission(tx *gorm.DB, p identity.Principal, d ETLDefinition, basis ...*ETLAuthorizationBasis) error {
	var account identity.User
	if err := tx.Select("id", "department_id").Where("id=? AND active=true", p.UserID).Take(&account).Error; err != nil {
		return denied
	}
	p.DepartmentID = account.DepartmentID
	if err := tx.Model(&identity.UserRole{}).Where("user_id=?", p.UserID).Pluck("role", &p.Roles).Error; err != nil {
		return err
	}
	if err := tx.Model(&identity.PlatformAccess{}).Where("user_id=?", p.UserID).Pluck("platform", &p.Platforms).Error; err != nil {
		return err
	}
	if !platform(p, "exchange") || !explicitRole(p, "operator") {
		return denied
	}
	if d.SourceResource != nil {
		if err := requireResourceCapability(tx, p, Resource{Type: "source", ID: d.Target.SourceID}, "operate_resource"); err != nil {
			return err
		}
		_, err := s.etlResourceSchema(tx, p, d, basis...)
		return err
	}
	for _, id := range []string{d.Source.SourceID, d.Target.SourceID} {
		if err := requireResourceCapability(tx, p, Resource{Type: "source", ID: id}, "operate_resource"); err != nil {
			return err
		}
	}
	if admin(p) {
		return nil
	}
	// Keep derived data in the source department, as dataset creation does.
	sourceRecord, err := get(tx, "source", d.Source.SourceID, false)
	if err != nil {
		return err
	}
	targetRecord, err := get(tx, "source", d.Target.SourceID, false)
	if err != nil {
		return err
	}
	if sourceRecord.DepartmentID != targetRecord.DepartmentID {
		return problem(403, "ETL_CROSS_DEPARTMENT", "普通账号 ETL 目标须与来源属于同一部门；跨部门交付请走数据使用申请审批")
	}
	// Derived data requires the same explicit processing authorization as governance.
	var object Record
	if err := tx.Where("kind='object' AND parent_id=? AND body->>'qualified_name'=?", d.Source.SourceID, d.Source.Schema+"."+d.Source.Table).Take(&object).Error; err != nil {
		return problem(403, "ETL_PROCESSING_REQUIRED", "请先登记来源对象并授予允许派生成果的加工授权")
	}
	ref := Resource{Type: "raw_object", ID: object.ID, Version: object.Version}
	fields := []string{}
	for _, m := range d.Mapping {
		fields = append(fields, m.Source)
	}
	if err := requireProcessingAuthorization(tx, p, ref, fields, "数据治理"); err != nil {
		return err
	}
	var grants []Record
	if err := tx.Where("kind='processing_authorization' AND state='active' AND body->>'user_id'=? AND body->'resource'->>'resource_id'=?", p.UserID, object.ID).Find(&grants).Error; err != nil {
		return err
	}
	for _, row := range grants {
		g, err := decode[ProcessingAuthorization](row.Body)
		if err != nil {
			return err
		}
		covered := true
		for _, f := range fields {
			covered = covered && slices.Contains(g.Fields, f)
		}
		if g.Resource == ref && g.Purpose == "数据治理" && g.AllowDerived && time.Now().Before(g.ExpiresAt) && covered {
			return nil
		}
	}
	return problem(403, "ETL_DERIVED_REQUIRED", "加工授权必须允许形成派生成果并覆盖全部映射字段")
}
func (s *Server) prepareETL(ctx context.Context, tx *gorm.DB, p identity.Principal, d ETLDefinition) (ETLDefinition, SourceSecret, SourceSecret, *sql.DB, *sql.DB, error) {
	if !valid(d.Name, 200) || (d.SourceResource == nil && (!valid(d.Source.SourceID, 128) || !valid(d.Source.Schema, 128) || !valid(d.Source.Table, 128))) || !valid(d.Target.SourceID, 128) || !valid(d.Target.Schema, 128) || !valid(d.Target.Table, 128) {
		return d, SourceSecret{}, SourceSecret{}, nil, nil, invalid
	}
	if err := s.etlPermission(tx, p, d); err != nil {
		return d, SourceSecret{}, SourceSecret{}, nil, nil, err
	}
	var src SourceSecret
	var from *sql.DB
	var err error
	if d.SourceResource == nil {
		_, src, from, err = s.sourceForRead(ctx, tx, p, d.Source.SourceID)
	}
	if err != nil {
		return d, src, SourceSecret{}, nil, nil, err
	}
	_, dst, to, err := s.sourceForRead(ctx, tx, p, d.Target.SourceID)
	if err != nil {
		if from != nil {
			from.Close()
		}
		return d, src, dst, nil, nil, err
	}
	fail := func(e error) (ETLDefinition, SourceSecret, SourceSecret, *sql.DB, *sql.DB, error) {
		if from != nil {
			from.Close()
		}
		to.Close()
		return d, src, dst, nil, nil, e
	}
	if src.Kind == dst.Kind && src.Host == dst.Host && src.Port == dst.Port && src.Database == dst.Database && d.Source.Schema == d.Target.Schema && d.Source.Table == d.Target.Table {
		return fail(problem(400, "ETL_SAME_TABLE", "来源表与目标表不能相同"))
	}
	var source TableConfig
	var e error
	if d.SourceResource != nil {
		source, e = s.etlResourceSchema(tx, p, d)
	} else {
		source, e = discoverTable(ctx, from, src, d.Source.Schema, d.Source.Table)
	}
	if e != nil {
		return fail(e)
	}
	if d.SourceResource != nil && d.SourceResource.Type == "table_resource" {
		_, managed, err := managedResource(tx, *d.SourceResource, false)
		if err != nil {
			return fail(err)
		}
		if managed.Adapter == "database" {
			cfg, err := decode[TableConfig](managed.Config)
			if err != nil {
				return fail(err)
			}
			r, err := get(tx, "source", cfg.SourceID, false)
			if err != nil {
				return fail(err)
			}
			physical, err := decode[SourceSecret](r.Body)
			if err != nil {
				return fail(err)
			}
			if physical.Kind == dst.Kind && physical.Host == dst.Host && physical.Port == dst.Port && physical.Database == dst.Database && cfg.Schema == d.Target.Schema && cfg.Table == d.Target.Table {
				return fail(problem(400, "ETL_SAME_TABLE", "来源资源与目标表不能指向同一张表"))
			}
		}
	}
	target, e := discoverTable(ctx, to, dst, d.Target.Schema, d.Target.Table)
	if e != nil {
		return fail(e)
	}
	if dst.Kind == "mysql" {
		var engine string
		if e = to.QueryRowContext(ctx, "SELECT COALESCE(engine,'') FROM information_schema.tables WHERE table_schema=? AND table_name=?", target.Schema, target.Table).Scan(&engine); e != nil {
			return fail(e)
		}
		var extraUnique int
		if e = to.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=? AND table_name=? AND non_unique=0 AND index_name<>'PRIMARY'", target.Schema, target.Table).Scan(&extraUnique); e != nil {
			return fail(e)
		}
		if extraUnique > 0 {
			return fail(problem(400, "ETL_UNIQUE_TARGET", "MySQL 目标暂不支持主键以外的唯一索引，避免按其他唯一键覆盖记录"))
		}
		if engine != "InnoDB" {
			return fail(problem(400, "ETL_TRANSACTION_REQUIRED", "MySQL 目标必须为 InnoDB 事务表"))
		}
	}
	if e = validateETLMapping(d.Mapping, source, target); e != nil {
		return fail(e)
	}
	if src.Kind == "oracle" || dst.Kind == "oracle" {
		return fail(problem(400, "ETL_DATABASE_UNSUPPORTED", "直接库表交换与目标写入支持 PostgreSQL / MySQL"))
	}
	if d.SourceResource == nil {
		source.SourceID = d.Source.SourceID
	}
	target.SourceID = d.Target.SourceID
	d.Source = source
	d.Target = target
	return d, src, dst, from, to, nil
}
func (s *Server) registerETL(api, write *gin.RouterGroup) {
	s.registerETLOperations(api, write)
	api.GET("/etl-capabilities", s.require("exchange", "operator"), s.etlCapabilities)
	api.GET("/etl-tasks", s.require("exchange", "operator"), s.etlList("etl_task"))
	api.GET("/etl-tasks/:id/runs", s.require("exchange", "operator"), s.etlList("etl_run"))
	write.POST("/etl-tasks", s.require("exchange", "operator"), s.mutate("create_etl_task", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		input, err := bind[etlTaskWrite](b)
		d := input.ETLDefinition
		if err != nil {
			return 0, nil, "", err
		}
		// Serialize configuration binding with connection edits.
		sourceIDs := []string{d.Target.SourceID}
		if d.SourceResource == nil {
			sourceIDs = append(sourceIDs, d.Source.SourceID)
		}
		slices.Sort(sourceIDs)
		for _, sourceID := range slices.Compact(sourceIDs) {
			if _, err = get(tx, "source", sourceID, true); err != nil {
				return 0, nil, "", err
			}
		}
		ctx, cancel := context.WithTimeout(tx.Statement.Context, 20*time.Second)
		defer cancel()
		d, _, _, from, to, err := s.prepareETL(ctx, tx, p, d)
		if err != nil {
			return 0, nil, "", err
		}
		if from != nil {
			from.Close()
		}
		to.Close()
		id, err := newID()
		if err != nil {
			return 0, nil, "", err
		}
		state := "ready"
		if input.Draft {
			state = "draft"
		}
		r := Record{Kind: "etl_task", ID: id, DepartmentID: p.DepartmentID, OwnerID: p.UserID, State: state, Version: 1, Body: encode(d)}
		err = tx.Create(&r).Error
		return 201, etlView(r), id, err
	}))
	write.POST("/etl-tasks/:id/run", s.require("exchange", "operator"), func(c *gin.Context) {
		s.mutatePath("run_etl_task", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
			if _, e := bind[struct{}](b); e != nil {
				return 0, nil, id, e
			}
			task, e := get(tx, "etl_task", id, true)
			if e != nil {
				return 0, nil, id, e
			}
			if !admin(p) && task.OwnerID != p.UserID {
				return 0, nil, id, denied
			}
			d, e := decode[ETLDefinition](task.Body)
			if e != nil {
				return 0, nil, id, e
			}
			r, e := s.enqueueETL(tx, p, task, d, "", 1, c.GetString("trace_id"))
			if e != nil {
				return 0, nil, id, e
			}
			return 201, etlView(r), r.ID, nil
		})(c)
	})
}
func (s *Server) etlList(kind string) gin.HandlerFunc {
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
		q := s.db.WithContext(c.Request.Context()).Model(&Record{}).Where("kind=?", kind)
		if !admin(p) {
			q = q.Where("owner_id=?", p.UserID)
		}
		if kind == "etl_run" {
			q = q.Where("parent_id=?", c.Param("id"))
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		var rows []Record
		if e = q.Order("created_at DESC, id DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		items := []json.RawMessage{}
		for _, r := range rows {
			items = append(items, etlView(r))
		}
		c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
	}
}
func (s *Server) executeETL(parent context.Context, row Record, token string) {
	id := row.ID
	workerStarted := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.etl.Timeout)
	defer cancel()
	defer s.etlFinishLease(id, token)
	p, principalErr := s.etlPrincipal(ctx, row.OwnerID)
	done := make(chan struct{})
	go s.etlMonitor(ctx, cancel, id, token, done)
	defer func() {
		cancel()
		<-done

	}()
	run, err := decode[ETLRun](row.Body)
	if err != nil {
		slog.Error("ETL run definition invalid", "run_id", id)
		return
	}
	run.Timings = &ETLTimings{}
	run.leaseOwner = token
	run.runID = id
	persist := func(state, message string) error {
		run.Timings.TotalMS = time.Since(workerStarted).Milliseconds()
		return s.etlProgress(&row, &run, p, state, message, "RUN_"+strings.ToUpper(state))
	}
	var targetTx *sql.Tx
	var releaseTarget func()
	var targetDB *sql.DB
	var targetKind string
	fail := func(message string) {
		run.Timings.TotalMS = time.Since(workerStarted).Milliseconds()
		if targetTx != nil {
			rollbackErr := targetTx.Rollback()
			if (rollbackErr != nil && rollbackErr != sql.ErrTxDone) || ctx.Err() != nil {
				// Before any Commit call, reacquiring the target lock proves the aborted transaction has ended.
				confirmed := false
				if releaseTarget != nil && targetDB != nil {
					releaseTarget()
					probeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
					_, unlock, e := etlTargetTransaction(probeCtx, targetDB, targetKind, run.Definition.Target)
					if e == nil {
						unlock()
						confirmed = true
					}
					stop()
				}
				if !confirmed {
					if e := persist("unknown", "事务回滚结果待核实，请联系运维检查目标数据库"); e != nil {
						slog.Error("ETL rollback status persistence failed", "run_id", id)
					}
					return
				}
			}
		}

		run.Written = 0
		for i := range run.Batches {
			run.Batches[i].State = "rolled_back"
		}
		state, code := "failed", etlFailureCode(err)
		if code == "RUN_CANCELLED" {
			code = "RUN_CONTROL_LOST"
		}
		if ctx.Err() == context.DeadlineExceeded {
			code = "RUN_TIMEOUT"
			message = "执行超过配置时限，已终止并回滚"
		} else {
			var ctl ETLControl
			if e := s.db.Where("run_id=?", id).Take(&ctl).Error; e == nil && ctl.CancelRequested {
				state = "cancelled"
				code = "RUN_CANCELLED"
				message = "运行已取消，目标事务已回滚"
			}
		}
		if e := s.etlProgress(&row, &run, p, state, message, code); e != nil {
			slog.Error("ETL status persistence failed", "run_id", id)
		}
	}

	if principalErr != nil {
		err = principalErr
		fail("执行账号已失效，目标未写入")
		return
	}

	if err = persist("extracting", "正在校验权限、连接并提取来源"); err != nil {
		fail("提取前运行状态保存失败，目标未写入")
		return
	}

	prepareStarted := time.Now()
	d, src, dst, from, to, err := s.prepareETL(ctx, s.db.WithContext(ctx), p, run.Definition)
	if err != nil {
		fail("连接、权限或字段结构校验失败；请核对数据源、加工授权与目标表")
		return
	}
	if from != nil {
		defer from.Close()
	}
	defer to.Close()
	run.Definition = d
	reader, closeReader, err := s.etlReader(ctx, from, src, p, &run)
	if err != nil {
		fail("来源校验或读取失败，请检查权限、数据范围和事务引擎")
		return
	}
	defer closeReader()
	if err = persist("loading", "正在分批写入目标事务，尚未提交"); err != nil {
		fail("写入前运行状态保存失败，目标未写入")
		return
	}
	tx, stopTarget, err := etlTargetTransaction(ctx, to, dst.Kind, d.Target)
	if err != nil {
		fail("无法启动目标事务")
		return
	}
	targetTx = tx
	releaseTarget = stopTarget
	targetDB = to
	targetKind = dst.Kind
	defer stopTarget()
	if err = s.initETLDelivery(ctx, id, &run, tx, dst.Kind); err != nil {
		fail("交付期望索引初始化失败，目标未提交")
		return
	}
	run.Timings.PrepareMS = time.Since(prepareStarted).Milliseconds()
	var writeElapsed, indexElapsed, readElapsed time.Duration
	batch := make([][]interface{}, 0, 500)
	batchBytes := int64(0)
	lastProgress := time.Now()
	lastRows := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		writeStarted := time.Now()
		for _, values := range batch {
			if e := etlValidateTargetValues(run.delivery.Columns, values); e != nil {
				return e
			}
		}
		if e := etlWriteBatch(ctx, tx, dst.Kind, d.Target, d.Mapping, batch); e != nil {
			return e
		}
		writeElapsed += time.Since(writeStarted)
		run.Timings.WriteMS = writeElapsed.Milliseconds()
		indexStarted := time.Now()
		indexErr := s.indexETLBatch(ctx, id, &run, tx, dst.Kind, batch)
		indexElapsed += time.Since(indexStarted)
		run.Timings.IndexMS = indexElapsed.Milliseconds()
		if e := indexErr; e != nil {
			return e
		}
		end := run.Read
		run.Batches = append(run.Batches, ETLBatch{First: end - len(batch) + 1, Last: end, State: "pending_commit"})
		batch = batch[:0]
		batchBytes = 0
		if run.Read-lastRows >= 5000 || time.Since(lastProgress) >= time.Second {
			if e := persist("loading", "正在分批写入目标事务，尚未提交"); e != nil {
				return e
			}
			lastProgress = time.Now()
			lastRows = run.Read
		}
		return nil
	}
	readClock := time.Now()
	err = reader(func(values []interface{}) error {
		readElapsed += time.Since(readClock)
		defer func() { readClock = time.Now() }()
		if run.Read >= s.etl.MaxRows {
			return problem(409, "ETL_LIMIT", "读取行数超过配置上限，未提交目标事务")
		}
		size := etlValueBytes(values)
		if size > 4*1024*1024 || run.Bytes+size > s.etl.MaxBytes {
			return problem(409, "ETL_LIMIT", "交换数据超过配置上限")
		}
		if len(batch) > 0 && batchBytes+size > 4*1024*1024 {
			if e := flush(); e != nil {
				return e
			}
		}
		run.Read++
		run.Bytes += size
		batch = append(batch, values)
		batchBytes += size
		if len(batch) == 500 {
			return flush()
		}
		return nil
	})
	readElapsed += time.Since(readClock)
	run.Timings.ReadTransformMS = readElapsed.Milliseconds()
	if err == nil {
		err = flush()
	}
	if err != nil {
		fail("提取、转换、批量写入或进度保存失败，本次目标事务已回滚")
		return
	}
	if err = persist("loading", "来源读取完成，正在校验提交条件"); err != nil {
		fail("进度保存失败，本次目标事务已回滚")
		return
	}
	beforeStarted := time.Now()
	if dst.Kind == "postgres" {
		if _, err = tx.ExecContext(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			fail("目标延迟约束校验失败，已回滚")
			return
		}
	}
	if err = s.sealETLDelivery(ctx, id, &run); err != nil {
		fail("交付期望索引保存失败，已回滚")
		return
	}
	checkUpdated := time.Time{}
	checked, checkErr := s.checkETLDelivery(ctx, id, tx, func(report ETLReconciliation) error {
		if !checkUpdated.IsZero() && time.Since(checkUpdated) < time.Second {
			return nil
		}
		checkUpdated = time.Now()
		run.Timings.BeforeCheckMS = time.Since(beforeStarted).Milliseconds()
		return persist("loading", fmt.Sprintf("正在提交前核对：已读回 %d / %d 个交付主键", report.CheckedRows, report.KeyCount))
	})
	if checkErr != nil {
		err = checkErr
		fail("目标事务内核对失败，已回滚")
		return
	}
	run.Timings.BeforeCheckMS = time.Since(beforeStarted).Milliseconds()
	if checked.State != "passed" {
		err = problem(409, "RECONCILIATION_MISMATCH", "提交前目标值与本次交付期望不一致")
		fail("目标事务内核对不一致，已回滚")
		return
	}
	if run.InputSnapshot != nil {
		if err = s.etlInputIdentity(ctx, p); err != nil {
			fail("提交前账号范围或脱敏依据已变化，本次事务已回滚")
			return
		}
	}
	if err = s.etlPermission(s.db.WithContext(ctx), p, d, run.authorization); err != nil {
		fail("提交前权限已失效，本次事务已回滚")
		return
	}
	if err = persist("committing", "正在提交目标事务"); err != nil {
		fail("提交前控制检查失败，已回滚")
		return
	}
	securityProof, proofErr := s.securityETLProof(ctx, p, &run, id)
	if proofErr != nil {
		err = proofErr
		fail("无法生成所需安全交付凭据，目标事务已回滚")
		return
	}
	commitStarted := time.Now()
	err = tx.Commit()
	run.Timings.CommitMS = time.Since(commitStarted).Milliseconds()
	if securityProof != nil {
		state := "completed"
		if err != nil {
			state = "unknown"
		}
		if e := s.db.WithContext(context.WithoutCancel(ctx)).Model(&SecurityDeliveryProof{}).Where("id=?", securityProof.ID).Updates(map[string]interface{}{"state": state, "updated_at": time.Now().UTC()}).Error; e != nil {
			slog.Error("Security ETL proof completion unconfirmed", "run_id", id)
		}
	}
	if err != nil {
		if e := persist("unknown", "提交结果未知，请核实目标数据库后再处理，禁止自动重跑"); e != nil {
			slog.Error("ETL status persistence failed", "run_id", id)
		}
		return
	}
	run.Written = run.Read
	for i := range run.Batches {
		run.Batches[i].State = "committed"
	}
	if err = persist("succeeded", "目标事务已提交"); err != nil {
		slog.Error("ETL commit status persistence failed", "run_id", id)
		if e := s.etlProgress(&row, &run, p, "unknown", "目标提交已返回，但运行结果持久化失败；请核实目标事务", "COMMIT_STATUS_LOST"); e != nil {
			slog.Error("ETL unknown result persistence failed", "run_id", id)
		}
	}
}
