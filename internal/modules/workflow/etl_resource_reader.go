package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ETLInputSnapshot struct {
	Authorization   *ETLAuthorizationBasis `json:"authorization,omitempty"`
	Kind            string                 `json:"kind"`
	ResourceID      string                 `json:"resource_id"`
	ContractVersion int                    `json:"contract_version"`
	SnapshotID      string                 `json:"snapshot_id"`
	Fields          []string               `json:"fields"`
	Partitions      []CDCPartitionRange    `json:"partitions,omitempty"`
}

type ETLAuthorizationBasis struct {
	Sharing           SharingSnapshot `json:"sharing_snapshot"`
	GrantID           string          `json:"grant_id"`
	GrantVersion      int             `json:"grant_version"`
	GrantFingerprint  string          `json:"grant_fingerprint"`
	PolicyFingerprint string          `json:"policy_fingerprint"`
}

// Resource authorization is the consumer boundary; the source credential is used only by the server.
func (s *Server) etlResourceReader(ctx context.Context, p identity.Principal, run *ETLRun) (readerOut func(func([]interface{}) error) error, closeOut func(), errOut error) {
	definition := run.Definition
	decision := SecurityDecision{}
	basis := &ETLAuthorizationBasis{}
	schema, e := s.etlResourceSchemaDecision(s.db.WithContext(ctx), p, definition, &decision, basis)
	if e != nil {
		return nil, nil, e
	}
	run.securityMaskFields = decision.MaskFields
	run.securityRowFilters = decision.RowFilters
	run.authorization = basis
	defer func() {
		if errOut == nil && run.InputSnapshot != nil {
			run.InputSnapshot.Authorization = basis
		}
	}()
	for _, field := range schema.Fields {
		if slices.Contains(run.securityMaskFields, field.Name) {
			for _, m := range definition.Mapping {
				if m.Source == field.Name && (field.PrimaryKey || slices.Contains(definition.Target.Keys, m.Target)) {
					return nil, nil, problem(409, "ETL_SECURITY_MASKED_KEY", "安全策略要求脱敏的字段不能用于目标主键")
				}
			}
		}
	}
	// Bounded compatibility readers need the same key-preservation guarantee as bulk readers.
	if !admin(p) {
		for _, field := range schema.Fields {
			if !field.Sensitive {
				continue
			}
			if field.PrimaryKey {
				return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏主键不能用于保持唯一性的交换")
			}
			for _, mapping := range definition.Mapping {
				if mapping.Source == field.Name && slices.Contains(definition.Target.Keys, mapping.Target) {
					return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏字段不能作为目标主键，避免记录覆盖")
				}
			}
		}
	}
	ref := *definition.SourceResource
	if ref.Type == "governed_dataset" {
		return s.governedResourceReader(ctx, p, run, schema)
	}
	if ref.Type == "raw_object" {
		return s.etlRawSnapshotReader(ctx, p, run, schema)
	}
	if ref.Type != "table_resource" {
		return s.etlLegacyResourceReader(ctx, p, run)
	}
	_, managed, e := managedResource(s.db.WithContext(ctx), ref, false)
	if e != nil {
		return nil, nil, e
	}
	if managed.Adapter != "database" {
		return s.etlLegacyResourceReader(ctx, p, run)
	}
	physical, e := decode[TableConfig](managed.Config)
	if e != nil {
		return nil, nil, e
	}
	source, e := get(s.db.WithContext(ctx), "source", physical.SourceID, false)
	if e != nil {
		return nil, nil, e
	}
	src, e := decode[SourceSecret](source.Body)
	if e != nil {
		return nil, nil, e
	}
	// Oracle keeps its verified small-resource reader until the consistent bulk adapter is independently verified.
	if src.Kind == "oracle" {
		return s.etlLegacyResourceReader(ctx, p, run)
	}
	db, e := s.runtime.database(ctx, s.db, src)
	if e != nil {
		return nil, nil, e
	}
	current, e := discoverTable(ctx, db, src, physical.Schema, physical.Table)
	if e != nil {
		db.Close()
		return nil, nil, e
	}
	if !reflect.DeepEqual(current.Fields, physical.Fields) || !slices.Equal(current.Keys, physical.Keys) {
		db.Close()
		return nil, nil, problem(409, "SOURCE_SCHEMA_CHANGED", "源表结构变化，请核查资源挂载后重试")
	}
	sensitive := map[string]bool{}
	for _, field := range schema.Fields {
		sensitive[field.Name] = (field.Sensitive && !admin(p)) || slices.Contains(run.securityMaskFields, field.Name)
		if sensitive[field.Name] && field.PrimaryKey {
			db.Close()
			return nil, nil, problem(409, "ETL_MASKED_KEY", "授权主键需要脱敏，无法用于保持唯一性的批量交换，请申请合适的独立资源")
		}
	}
	for _, mapping := range definition.Mapping {
		if sensitive[mapping.Source] && slices.Contains(definition.Target.Keys, mapping.Target) {
			db.Close()
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏字段不能作为目标主键，避免多条记录覆盖为同一主键")
		}
	}
	// Reuse the direct reader's repeatable-read transaction and exact numeric scanning, before applying delivery masking.
	input := *run
	input.Definition = definition
	input.Definition.SourceResource = nil
	input.Definition.Source = physical
	input.Definition.Mapping = append([]ETLMapping{}, definition.Mapping...)
	for i := range input.Definition.Mapping {
		input.Definition.Mapping[i].Rule = "copy"
	}
	reader, closeInput, e := s.etlReader(ctx, db, src, p, &input)
	if e != nil {
		db.Close()
		return nil, nil, e
	}
	run.Planned = input.Planned
	snapshotID, e := newID()
	if e != nil {
		closeInput()
		db.Close()
		return nil, nil, e
	}
	fields := []string{}
	for _, m := range definition.Mapping {
		if !slices.Contains(fields, m.Source) {
			fields = append(fields, m.Source)
		}
	}
	run.InputSnapshot = &ETLInputSnapshot{Kind: ref.Type, ResourceID: ref.ID, ContractVersion: ref.Version, SnapshotID: snapshotID, Fields: fields}
	consumeReader := func(consume func([]interface{}) error) error {
		processed := 0
		return reader(func(values []interface{}) error {
			if processed%500 == 0 {
				if err := s.etlInputIdentity(ctx, p); err != nil {
					return err
				}
				if err := s.etlPermission(s.db.WithContext(ctx), p, definition, run.authorization); err != nil {
					return err
				}
			}
			for i, m := range definition.Mapping {
				if sensitive[m.Source] && values[i] != nil {
					values[i] = "***"
				}
				var err error
				values[i], err = transformETL(values[i], m.Rule)
				if err != nil {
					return err
				}
			}
			processed++
			return consume(values)
		})
	}
	return consumeReader, func() { closeInput(); db.Close() }, nil
}

func (s *Server) etlLegacyResourceReader(ctx context.Context, p identity.Principal, run *ETLRun) (func(func([]interface{}) error) error, func(), error) {
	data, e := s.readETLResource(ctx, s.db.WithContext(ctx), p, run.Definition)
	if e != nil {
		return nil, nil, e
	}
	run.Planned = len(data)
	id, e := newID()
	if e != nil {
		return nil, nil, e
	}
	ref := *run.Definition.SourceResource
	fields := []string{}
	for _, m := range run.Definition.Mapping {
		if !slices.Contains(fields, m.Source) {
			fields = append(fields, m.Source)
		}
	}
	run.InputSnapshot = &ETLInputSnapshot{Kind: ref.Type, ResourceID: ref.ID, ContractVersion: ref.Version, SnapshotID: id, Fields: fields}
	return func(consume func([]interface{}) error) error {
		for i, row := range data {
			if i%500 == 0 {
				if e := s.etlInputIdentity(ctx, p); e != nil {
					return e
				}
				if e := s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization); e != nil {
					return e
				}
			}
			if e := consume(row); e != nil {
				return e
			}
		}
		return nil
	}, func() {}, nil
}

// A changed administrator exemption or department invalidates the masking basis of an in-flight resource delivery.
func (s *Server) etlInputIdentity(ctx context.Context, p identity.Principal) error {
	var count int64
	err := s.db.WithContext(ctx).Table("users").Where("id=? AND active=true AND department_id=?", p.UserID, p.DepartmentID).Where("EXISTS(SELECT 1 FROM user_roles WHERE user_id=users.id AND role='admin') = ?", admin(p)).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return problem(403, "ETL_AUTHORIZATION_CHANGED", "账号部门或管理员权限已变化，原脱敏依据失效，请重新建立运行")
	}
	return nil
}

func (s *Server) etlRawSnapshotReader(ctx context.Context, p identity.Principal, run *ETLRun, schema TableConfig) (func(func([]interface{}) error) error, func(), error) {
	ref := *run.Definition.SourceResource
	var task Record
	if e := s.db.WithContext(ctx).Where("kind='task' AND body->>'object_id'=?", ref.ID).Take(&task).Error; e != nil {
		return nil, nil, problem(409, "RAW_NOT_ARCHIVED", "原始对象尚未归档")
	}
	data, e := decode[TaskData](task.Body)
	if e != nil {
		return nil, nil, e
	}
	archive, e := get(s.db.WithContext(ctx), "archive", data.ArchiveID, false)
	if e != nil {
		return nil, nil, e
	}
	meta, e := decode[ArchiveData](archive.Body)
	if e != nil {
		return nil, nil, e
	}
	sensitive := map[string]bool{}
	fields := []string{}
	for _, field := range schema.Fields {
		fields = append(fields, field.Name)
		sensitive[field.Name] = (field.Sensitive && !admin(p)) || slices.Contains(run.securityMaskFields, field.Name)
		if sensitive[field.Name] && field.PrimaryKey {
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏主键不能用于批量交换")
		}
	}
	for _, mapping := range run.Definition.Mapping {
		if sensitive[mapping.Source] && slices.Contains(run.Definition.Target.Keys, mapping.Target) {
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏字段不能作为目标主键")
		}
	}
	authorize := func() error {
		if err := s.etlInputIdentity(ctx, p); err != nil {
			return err
		}
		return s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization)
	}
	lastProgress := time.Time{}
	progress := func(events int64) error {
		if !lastProgress.IsZero() && time.Since(lastProgress) < time.Second && events < int64(meta.Count) {
			return nil
		}
		lastProgress = time.Now()
		record, err := get(s.db.WithContext(ctx), "etl_run", run.runID, false)
		if err != nil {
			return err
		}
		return s.etlProgress(&record, run, p, "extracting", fmt.Sprintf("正在准备原始快照：已校验 %d / %d 条归档事件", events, meta.Count), "RAW_SNAPSHOT_PREPARING")
	}
	id, e := s.buildRawSnapshot(ctx, task, meta, authorize, progress)
	if e != nil {
		return nil, nil, e
	}
	return s.etlSnapshotReader(ctx, p, run, schema, id, meta.Boundary.Partitions, authorize)
}
func (s *Server) etlSnapshotReader(ctx context.Context, p identity.Principal, run *ETLRun, schema TableConfig, id string, partitions []CDCPartitionRange, authorize func() error) (func(func([]interface{}) error) error, func(), error) {
	ref := *run.Definition.SourceResource
	fields := fieldNames(schema.Fields)
	sensitive := map[string]bool{}
	for _, field := range schema.Fields {
		sensitive[field.Name] = (field.Sensitive && !admin(p)) || slices.Contains(run.securityMaskFields, field.Name)
		if sensitive[field.Name] && field.PrimaryKey {
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏主键不能用于批量交换")
		}
	}
	for _, m := range run.Definition.Mapping {
		if sensitive[m.Source] && slices.Contains(run.Definition.Target.Keys, m.Target) {
			return nil, nil, problem(409, "ETL_MASKED_KEY", "脱敏字段不能作为目标主键")
		}
	}
	var snapshot ResourceSnapshot
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND state='ready'", id).Take(&snapshot).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO resource_snapshot_refs(snapshot_id,run_id) VALUES(?,?) ON CONFLICT DO NOTHING", id, run.runID).Error
	})
	if e != nil {
		return nil, nil, e
	}
	run.Planned = int(snapshot.RowCount)
	run.InputSnapshot = &ETLInputSnapshot{Kind: ref.Type, ResourceID: ref.ID, ContractVersion: ref.Version, SnapshotID: id, Fields: fields, Partitions: partitions}
	reader := func(consume func([]interface{}) error) error {
		count := 0
		return s.rawSnapshotRows(ctx, id, func(row DataRow) error {
			if count%500 == 0 {
				if err := authorize(); err != nil {
					return err
				}
			}
			projected := rawProjection(row, fields, sensitive)
			values := make([]interface{}, len(run.Definition.Mapping))
			for i, m := range run.Definition.Mapping {
				decoder := json.NewDecoder(bytes.NewReader(projected[m.Source]))
				decoder.UseNumber()
				if err := decoder.Decode(&values[i]); err != nil {
					return err
				}
				if number, ok := values[i].(json.Number); ok {
					values[i] = number.String()
				}
				var err error
				values[i], err = transformETL(values[i], m.Rule)
				if err != nil {
					return err
				}
			}
			count++
			return consume(values)
		})
	}
	return reader, func() {}, nil
}

func (s *Server) governedResourceReader(ctx context.Context, p identity.Principal, run *ETLRun, schema TableConfig) (func(func([]interface{}) error) error, func(), error) {
	ref := *run.Definition.SourceResource
	dataset, e := get(s.db.WithContext(ctx), "dataset", ref.ID, false)
	if e != nil {
		return nil, nil, e
	}
	legacy, err := decode[DatasetData](dataset.Body)
	if err != nil {
		return nil, nil, err
	}
	if legacy.GovernanceRunID != "" {
		return s.governanceOutputResourceReader(ctx, p, run, schema, dataset)
	}
	if legacy.ResultBinding == nil {
		return s.etlLegacyResourceReader(ctx, p, run)
	}
	data, _, id, e := s.governedReady(ctx, s.db.WithContext(ctx), dataset)
	if e != nil {
		return nil, nil, e
	}
	archive, e := get(s.db.WithContext(ctx), "archive", data.ResultBinding.InputArchiveID, false)
	if e != nil {
		return nil, nil, e
	}
	meta, e := decode[ArchiveData](archive.Body)
	if e != nil {
		return nil, nil, e
	}
	authorize := func() error {
		if e := s.etlInputIdentity(ctx, p); e != nil {
			return e
		}
		return s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization)
	}
	return s.etlSnapshotReader(ctx, p, run, schema, id, meta.Boundary.Partitions, authorize)
}
