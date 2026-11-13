package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

// A processing grant authorizes computation, not disclosure of sensitive samples.
type governanceInput struct {
	WorkflowTrialArtifact bool                               `json:"workflow_trial_artifact,omitempty"`
	MasterSnapshotID      string                             `json:"master_snapshot_id,omitempty"`
	Origins               map[string][]GovernanceFieldOrigin `json:"origins,omitempty"`
	VisitorScope          string                             `json:"visitor_scope,omitempty"`
	UploadID              string                             `json:"upload_id,omitempty"`
	SourceName            string                             `json:"source_name,omitempty"`
	FieldLabels           map[string]string                  `json:"field_labels,omitempty"`
	WorkflowParent        string                             `json:"workflow_parent,omitempty"`
	Parameters            map[string]string                  `json:"parameters,omitempty"`
	Resource              Resource                           `json:"resource"`
	Fields                []Field                            `json:"fields"`
	SourceVersion         int                                `json:"source_version"`
	ResourceFingerprint   string                             `json:"resource_fingerprint"`
	SourceFingerprint     string                             `json:"source_fingerprint"`
	ProcessingFingerprint string                             `json:"processing_fingerprint"`
	DepartmentID          string                             `json:"department_id"`
	Administrator         bool                               `json:"administrator"`
}

func (s *Server) governanceInputBasis(tx *gorm.DB, p identity.Principal, ref Resource, names []string, derived bool) (governanceInput, error) {
	return s.governanceInputBasisParameters(tx, p, ref, names, derived, nil)
}
func (s *Server) governanceInputBasisParameters(tx *gorm.DB, p identity.Principal, ref Resource, names []string, derived bool, parameters map[string]string) (governanceInput, error) {
	if ref.Type == "master_snapshot" {
		return s.masterSnapshotBasis(tx, p, ref, names, parameters)
	}
	if !valid(ref.ID, 128) || ref.Version < 1 {
		return governanceInput{}, invalid
	}
	out := governanceInput{Resource: ref, Fields: []Field{}, DepartmentID: p.DepartmentID, Administrator: admin(p)}
	record, target, err := metadataTarget(tx, p, ref.ID, false)
	if err != nil {
		return out, err
	}
	if target.ResourceType != ref.Type || record.Version != ref.Version {
		return out, problem(409, "GOVERNANCE_INPUT_CHANGED", "资源类型或契约版本已变化，请重新选择")
	}
	if slices.Contains([]string{"disabled", "withdrawn", "retired", "archived"}, record.State) || (record.State == "draft" && target.ResourceType != "governed_dataset") {
		return out, problem(409, "GOVERNANCE_INPUT_UNAVAILABLE", "资源已停用或尚未就绪")
	}
	if !validFields(names, target.Fields) {
		return out, invalid
	}
	// New protection requirements must never disappear through a transformation.
	// Until this execution adapter can fulfill them, retain the controlled delivery path.
	security, securityErr := s.securityPolicyCheck(tx, p, ref, "etl")
	if securityErr != nil {
		return out, securityErr
	}
	if len(security.MaskFields) > 0 || security.RequireProof || security.Visible || security.FileMark || security.Invisible || len(security.RowFilters) > 0 {
		return out, problem(409, "GOVERNANCE_SECURITY_ADAPTER_REQUIRED", "当前治理执行与预览尚未适配此资源新增的脱敏或水印要求，请使用受控交付入口或完成对应适配后再执行")
	}
	if target.ResourceType != "raw_object" && !(target.ResourceType == "table_resource" && (target.Adapter == "database" || target.Adapter == "source_reader" || target.Adapter == "excel")) && target.ResourceType != "file_resource" && target.ResourceType != "api_resource" && target.ResourceType != "governed_dataset" {
		return out, problem(409, "GOVERNANCE_ROWS_UNAVAILABLE", "此资源当前只有登记契约，尚无受控的结构化行读取能力；请先挂载数据库表或 Excel 导入表")
	}
	if err = s.governanceStructuredInputBasis(tx, p, record, target, parameters); err != nil {
		return out, err
	}
	if len(parameters) > 0 {
		out.Parameters = parameters
	}
	if err = requireProcessingAuthorization(tx, p, ref, names, "数据治理"); err != nil {
		return out, err
	}
	if admin(p) {
		out.ProcessingFingerprint = "admin:" + p.UserID
	} else {
		var grants []Record
		if err = tx.Where("kind='processing_authorization' AND state='active' AND body->>'user_id'=?", p.UserID).Order("id").Find(&grants).Error; err != nil {
			return out, err
		}
		for _, r := range grants {
			g, e := decode[ProcessingAuthorization](r.Body)
			if e != nil {
				return out, e
			}
			if g.Resource != ref || g.Purpose != "数据治理" || !g.ExpiresAt.After(time.Now()) || (derived && !g.AllowDerived) {
				continue
			}
			if slices.ContainsFunc(names, func(f string) bool { return !slices.Contains(g.Fields, f) }) {
				continue
			}
			out.ProcessingFingerprint = fingerprint(encode([]interface{}{r.ID, r.Version, g}))
			break
		}
		if out.ProcessingFingerprint == "" {
			return out, problem(403, "GOVERNANCE_DERIVED_REQUIRED", "需要覆盖所选字段且允许派生成果的有效加工授权")
		}
	}
	for _, name := range names {
		for _, f := range target.Fields {
			if f.Name == name {
				if f.LogicalType == "unsupported" {
					return out, problem(400, "GOVERNANCE_TYPE_UNSUPPORTED", "所选字段包含尚不支持的结构类型")
				}
				out.Fields = append(out.Fields, f)
			}
		}
	}
	out.ResourceFingerprint = fingerprint(record.Body)
	if target.SourceID != "" {
		source, e := get(tx, "source", target.SourceID, false)
		if e != nil {
			return out, e
		}
		out.SourceVersion = source.Version
		out.SourceFingerprint = fingerprint(source.Body)
		src, e := decode[SourceSecret](source.Body)
		if e != nil {
			return out, e
		}
		if e = s.runtime.allowSource(tx.Statement.Context, tx, SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database}); e != nil {
			return out, e
		}
	}
	return out, nil
}
func (s *Server) governanceCheckInput(ctx context.Context, p identity.Principal, basis governanceInput, derived bool) error {
	live, err := s.etlPrincipal(ctx, p.UserID)
	if err != nil {
		return denied
	}
	if !platform(live, "governance") || !role(live, "governor") {
		return denied
	}
	var current governanceInput
	if basis.MasterSnapshotID != "" {
		if basis.Resource.Type != "master_snapshot" || basis.MasterSnapshotID != basis.Resource.ID || basis.UploadID != "" || basis.WorkflowParent != "" {
			return denied
		}
		current, err = s.masterSnapshotBasis(s.db.WithContext(ctx), live, basis.Resource, fieldNames(basis.Fields), basis.Parameters)
	} else if basis.UploadID != "" {
		if basis.UploadID != basis.Resource.ID || basis.WorkflowParent != "" || len(basis.Parameters) > 0 {
			return denied
		}
		current, err = lightweightInputBasisWithScope(s.db.WithContext(ctx), live, basis.Resource, fieldNames(basis.Fields), basis.VisitorScope)
	} else if basis.WorkflowParent != "" {
		current, err = s.governanceWorkflowArtifactBasis(s.db.WithContext(ctx), live, basis.WorkflowParent, basis.Resource.ID, fieldNames(basis.Fields))
	} else {
		current, err = s.governanceInputBasisParameters(s.db.WithContext(ctx), live, basis.Resource, fieldNames(basis.Fields), derived, basis.Parameters)
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, basis) {
		return problem(403, "GOVERNANCE_AUTHORIZATION_CHANGED", "来源、账号、加工授权或敏感范围已变化，当前运行已停止")
	}
	return nil
}

// Native SQL uses one statement snapshot; external readers retain their protocol consistency limits.
// No source credentials or arbitrary user SQL enter a definition or log.
func (s *Server) governanceReadInput(ctx context.Context, basis governanceInput, limit int, consume func([]interface{}) error) (int64, error) {
	if basis.WorkflowTrialArtifact {
		return s.readWorkflowTrialArtifact(ctx, basis, limit, consume)
	}
	if basis.MasterSnapshotID != "" {
		return s.readMasterSnapshot(ctx, basis, limit, consume)
	}
	if basis.UploadID != "" {
		return s.readLightweightInput(ctx, basis, limit, consume)
	}
	tx := s.db.WithContext(ctx)
	record, target, err := metadataTarget(tx, identity.Principal{UserID: "internal", Roles: []string{"admin"}}, basis.Resource.ID, false)
	if err != nil {
		return 0, err
	}
	if record.Version != basis.Resource.Version || fingerprint(record.Body) != basis.ResourceFingerprint {
		return 0, conflict
	}
	names := fieldNames(basis.Fields)
	if target.Adapter == "source_reader" {
		source, e := get(tx, "source", target.SourceID, false)
		if e != nil {
			return 0, e
		}
		if source.Version != basis.SourceVersion || fingerprint(source.Body) != basis.SourceFingerprint {
			return 0, conflict
		}
		managed, e := decode[ManagedResource](record.Body)
		if e != nil {
			return 0, e
		}
		cfg, e := decode[TableConfig](managed.Config)
		if e != nil {
			return 0, e
		}
		rows, e := s.externalSourceData(ctx, tx, cfg)
		if e != nil {
			return 0, e
		}
		for _, r := range rows[:min(len(rows), limit)] {
			values := make([]interface{}, len(names))
			for i, name := range names {
				v, ok := r[name]
				if !ok {
					return 0, invalid
				}
				values[i] = v
			}
			if e = consume(values); e != nil {
				return int64(len(rows)), e
			}
		}
		return int64(len(rows)), nil
	}
	if target.ResourceType == "file_resource" || target.ResourceType == "api_resource" || target.ResourceType == "governed_dataset" {
		return s.governanceReadStructuredInput(ctx, record, basis, limit, consume)
	}
	if target.Adapter == "excel" {
		managed, e := decode[ManagedResource](record.Body)
		if e != nil {
			return 0, e
		}
		cfg, e := decode[TableConfig](managed.Config)
		if e != nil {
			return 0, e
		}
		readTx := tx.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if readTx.Error != nil {
			return 0, readTx.Error
		}
		defer readTx.Rollback()
		var total int64
		if e = readTx.Model(&ImportRow{}).Where("batch_id=?", cfg.BatchID).Count(&total).Error; e != nil {
			return 0, e
		}
		if total > int64(s.etl.MaxRows) && limit > s.etl.MaxRows {
			return total, problem(409, "GOVERNANCE_LIMIT", "来源行数超过治理上限，请缩小输入范围或使用采样剖析")
		}
		after := 0
		for seen := 0; seen < limit; {
			batch := min(500, limit-seen)
			rows := []ImportRow{}
			if e = readTx.Where("batch_id=? AND row_number>?", cfg.BatchID, after).Order("row_number").Limit(batch).Find(&rows).Error; e != nil {
				return total, e
			}
			for _, row := range rows {
				valuesByName := map[string]interface{}{}
				decoder := json.NewDecoder(bytes.NewReader(row.Data))
				decoder.UseNumber()
				if e = decoder.Decode(&valuesByName); e != nil {
					return total, e
				}
				values := make([]interface{}, len(names))
				for i, name := range names {
					value, ok := valuesByName[name]
					if !ok {
						return total, invalid
					}
					values[i] = value
				}
				if e = consume(values); e != nil {
					return total, e
				}
				seen++
				after = row.RowNumber
			}
			if len(rows) < batch {
				break
			}
		}
		return total, nil
	}
	source, err := get(tx, "source", target.SourceID, false)
	if err != nil {
		return 0, err
	}
	if source.Version != basis.SourceVersion || fingerprint(source.Body) != basis.SourceFingerprint {
		return 0, conflict
	}
	src, err := decode[SourceSecret](source.Body)
	if err != nil {
		return 0, err
	}
	parts := strings.SplitN(target.QualifiedName, ".", 2)
	if len(parts) != 2 {
		return 0, invalid
	}
	schema, table := parts[0], parts[1]
	var bindings []CDCBinding
	if err = tx.Where("object_id=? AND source_id=?", record.ID, source.ID).Limit(1).Find(&bindings).Error; err != nil {
		return 0, err
	}
	if len(bindings) > 0 {
		schema, table = bindings[0].Schema, bindings[0].Table
		if src.Kind == "oracle" {
			src, err = cdcProbeSource(src, bindings[0].PDB)
			if err != nil {
				return 0, err
			}
		}
	}
	db, err := s.runtime.database(ctx, tx, src)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	physical, err := discoverTable(ctx, db, src, schema, table)
	if err != nil {
		return 0, err
	}
	expected := target.Fields
	if record.Kind == "table_resource" {
		managed, e := decode[ManagedResource](record.Body)
		if e != nil {
			return 0, e
		}
		cfg, e := decode[TableConfig](managed.Config)
		if e != nil {
			return 0, e
		}
		expected = cfg.Fields
	}
	// The live structure cannot silently widen a pinned mount contract or erase sensitivity policy.
	physicalFields := slices.Clone(physical.Fields)
	expectedFields := slices.Clone(expected)
	for i := range physicalFields {
		physicalFields[i].Sensitive = false
	}
	for i := range expectedFields {
		expectedFields[i].Sensitive = false
	}
	if !reflect.DeepEqual(physicalFields, expectedFields) {
		return 0, problem(409, "SOURCE_SCHEMA_CHANGED", "实际表结构与资源契约不同，请刷新元数据并修订挂载范围")
	}
	if src.Kind == "mysql" {
		var engine string
		if err = db.QueryRowContext(ctx, "SELECT COALESCE(engine,'') FROM information_schema.tables WHERE table_schema=? AND table_name=?", schema, table).Scan(&engine); err != nil {
			return 0, err
		}
		if engine != "InnoDB" {
			return 0, problem(409, "GOVERNANCE_TRANSACTION_REQUIRED", "MySQL 来源须使用 InnoDB，以保证一致的语句读取")
		}
	}
	columns := []string{}
	order := []string{}
	for _, f := range basis.Fields {
		column := quoteIdentifier(src.Kind, f.Name)
		if src.Kind == "oracle" && (f.LogicalType == "integer" || f.LogicalType == "decimal") {
			column = "TO_CHAR(" + column + ", 'TM9', 'NLS_NUMERIC_CHARACTERS = ''.,''')"
		}
		columns = append(columns, column)
	}
	for _, k := range physical.Keys {
		order = append(order, quoteIdentifier(src.Kind, k))
	}
	query := "SELECT " + strings.Join(columns, ",") + ",COUNT(*) OVER() FROM " + quoteIdentifier(src.Kind, schema) + "." + quoteIdentifier(src.Kind, table)
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ",")
	}
	if src.Kind == "oracle" {
		query += " FETCH FIRST " + strconv.Itoa(limit) + " ROWS ONLY"
	} else {
		query += " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		values := make([]interface{}, len(names))
		ptrs := make([]interface{}, len(names)+1)
		for i := range values {
			ptrs[i] = &values[i]
		}
		ptrs[len(names)] = &total
		if err = rows.Scan(ptrs...); err != nil {
			return total, err
		}
		if limit > s.etl.MaxRows && total > int64(s.etl.MaxRows) {
			return total, problem(409, "GOVERNANCE_LIMIT", "来源行数超过治理上限，请缩小输入范围或使用采样剖析")
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
			if stamp, ok := v.(time.Time); ok {
				if basis.Fields[i].LogicalType == "date" {
					values[i] = stamp.Format("2006-01-02")
				} else {
					values[i] = stamp.UTC().Format(time.RFC3339Nano)
				}
			}
		}
		if err = consume(values); err != nil {
			return total, err
		}
	}
	return total, rows.Err()
}

// TEMP relations are owned by a single transaction/session. Rollback destroys them on success, error and cancellation.
type governanceStage struct {
	tx    *sql.Tx
	count int64
	bytes int64
}

func (s *Server) governanceStageInput(ctx context.Context, p identity.Principal, input governanceInput, limit int, guard func() error, progress func(int64) error) (*governanceStage, int64, error) {
	db, err := s.db.DB()
	if err != nil {
		return nil, 0, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, 0, err
	}
	stage := &governanceStage{tx: tx}
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE governance_input_stage (ordinal bigint PRIMARY KEY, payload jsonb NOT NULL) ON COMMIT DROP"); err != nil {
		_ = tx.Rollback()
		return nil, 0, err
	}
	batch := []interface{}{}
	marks := []string{}
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO governance_input_stage(ordinal,payload) VALUES "+strings.Join(marks, ","), batch...)
		batch = nil
		marks = nil
		batchBytes = 0
		if e == nil && progress != nil {
			e = progress(stage.count)
		}
		return e
	}
	total, err := s.governanceReadInput(ctx, input, limit, func(values []interface{}) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stage.count%500 == 0 {
			if e := guard(); e != nil {
				return e
			}
		}
		stage.count++
		stage.bytes += etlValueBytes(values)
		if stage.count > int64(s.etl.MaxRows) || stage.bytes > s.etl.MaxBytes {
			return problem(409, "GOVERNANCE_LIMIT", "治理数据量超过配置的行数或字节上限")
		}
		row := map[string]interface{}{}
		for i, f := range input.Fields {
			v := values[i]
			if f.Sensitive && v != nil {
				v = "***"
			}
			row[f.Name] = v
		}
		offset := len(batch)
		encoded := string(encode(row))
		batchBytes += len(encoded)
		batch = append(batch, stage.count, encoded)
		marks = append(marks, fmt.Sprintf("($%d,$%d::jsonb)", offset+1, offset+2))
		if len(marks) == 250 || batchBytes >= 2*1024*1024 {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, total, err
	}
	return stage, total, nil
}
