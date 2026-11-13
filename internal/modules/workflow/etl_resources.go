package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

// Resource exchange always uses the consumer's delivery scope, never source credentials.
func (s *Server) etlResourceSchema(tx *gorm.DB, p identity.Principal, d ETLDefinition, bases ...*ETLAuthorizationBasis) (TableConfig, error) {
	return s.etlResourceSchemaDecision(tx, p, d, nil, bases...)
}

// Capture the enforcement decision and authorization fingerprint from the same policy read.
func (s *Server) etlResourceSchemaDecision(tx *gorm.DB, p identity.Principal, d ETLDefinition, decisionOut *SecurityDecision, bases ...*ETLAuthorizationBasis) (TableConfig, error) {
	ref := *d.SourceResource
	if ref.Type != "raw_object" && ref.Type != "governed_dataset" && ref.Type != "table_resource" {
		return TableConfig{}, invalid
	}
	decision, securityErr := s.securityPolicyCheck(tx, p, ref, "etl")
	if securityErr != nil {
		return TableConfig{}, securityErr
	}
	if decisionOut != nil {
		*decisionOut = decision
	}
	var basis *ETLAuthorizationBasis
	var expected *SharingSnapshot
	if len(bases) > 0 && bases[0] != nil {
		basis = bases[0]
		if basis.GrantID != "" {
			expected = &basis.Sharing
		}
	}
	sharing, e := requireSharingSnapshot(tx, ref, expected)
	if e != nil {
		return TableConfig{}, e
	}
	fields := []string{}
	for _, m := range d.Mapping {
		if !slices.Contains(fields, m.Source) {
			fields = append(fields, m.Source)
		}
	}
	grant, grantData, schema, allowed, e := s.authorize(tx, p, ref, fields)
	if e != nil {
		return TableConfig{}, e
	}
	for i := range schema {
		if slices.Contains(decision.MaskFields, schema[i].Name) {
			schema[i].Sensitive = true
		}
	}
	if basis != nil {
		policyFingerprint := fingerprint(encode(schema))
		if len(decision.Controls) > 0 || len(decision.MaskFields) > 0 {
			policyFingerprint = fingerprint(encode(struct {
				Fields   []Field
				Decision SecurityDecision
			}{schema, decision}))
		}
		current := ETLAuthorizationBasis{Sharing: sharing, GrantID: grant.ID, GrantVersion: grant.Version, GrantFingerprint: fingerprint(encode(grantData)), PolicyFingerprint: policyFingerprint}
		if basis.GrantID == "" {
			*basis = current
		} else if *basis != current {
			return TableConfig{}, problem(403, "ETL_AUTHORIZATION_CHANGED", "目录、授权或脱敏依据已变化，请基于当前版本重新建立运行")
		}
	}
	// Transformations require explicit permission to derive new data; copying follows delivery authorization.
	if !admin(p) && slices.ContainsFunc(d.Mapping, func(m ETLMapping) bool { return m.Rule != "copy" }) {
		var grants []Record
		if e = tx.Where("kind='processing_authorization' AND state='active' AND body->>'user_id'=?", p.UserID).Find(&grants).Error; e != nil {
			return TableConfig{}, e
		}
		permitted := false
		for _, r := range grants {
			g, err := decode[ProcessingAuthorization](r.Body)
			if err != nil {
				return TableConfig{}, err
			}
			covered := true
			for _, f := range fields {
				covered = covered && slices.Contains(g.Fields, f)
			}
			if g.Resource == ref && g.AllowDerived && g.Purpose == "数据治理" && time.Now().Before(g.ExpiresAt) && covered {
				permitted = true
			}
		}
		if !permitted {
			return TableConfig{}, problem(403, "ETL_DERIVED_REQUIRED", "字段转换需要覆盖所选字段且允许派生成果的数据加工授权；直接映射使用数据使用授权")
		}
	}
	cfg := TableConfig{SourceID: ref.ID, Schema: ref.Type, Table: ref.ID, Fields: []Field{}, Keys: []string{}}
	for _, f := range schema {
		if slices.Contains(allowed, f.Name) {
			cfg.Fields = append(cfg.Fields, f)
			if f.PrimaryKey {
				cfg.Keys = append(cfg.Keys, f.Name)
			}
		}
	}
	if ref.Type == "governed_dataset" {
		r, err := get(tx, "dataset", ref.ID, false)
		if err != nil {
			return cfg, err
		}
		if err = requireDatasetProcessingBasis(tx, r, p); err != nil {
			return cfg, err
		}
		data, err := decode[DatasetData](r.Body)
		if err != nil {
			return cfg, err
		}
		if data.GovernanceRunID != "" {
			if _, _, _, _, err = s.governanceOutputReady(tx.Statement.Context, tx, r); err != nil {
				return cfg, err
			}
		}
		if data.ResultCheckID != "" && data.ResultBinding == nil {
			return cfg, problem(409, "QUALITY_BLOCKED", "显式结果绑定尚未通过核验")
		}
		if data.ResultBinding != nil {
			if _, _, _, err = s.governedReady(tx.Statement.Context, tx, r); err != nil {
				return cfg, err
			}
		}
	}
	r, _, err := resource(tx, ref, false)
	if err != nil {
		return cfg, err
	}
	switch ref.Type {
	case "table_resource":
		v, err := decode[ManagedResource](r.Body)
		if err != nil {
			return cfg, err
		}
		cfg.Table = v.Name
		cfg.Schema = v.DataLayer
	case "governed_dataset":
		v, err := decode[DatasetData](r.Body)
		if err != nil {
			return cfg, err
		}
		cfg.Table = v.Definition.Name
	case "raw_object":
		v, err := decode[Object](r.Body)
		if err != nil {
			return cfg, err
		}
		cfg.Table = v.QualifiedName
	}
	return cfg, nil
}

func (s *Server) readETLResource(ctx context.Context, tx *gorm.DB, p identity.Principal, d ETLDefinition) ([][]interface{}, error) {
	decision := SecurityDecision{}
	cfg, e := s.etlResourceSchemaDecision(tx, p, d, &decision)
	if e != nil {
		return nil, e
	}
	ref := *d.SourceResource
	fields := []string{}
	sensitive := map[string]bool{}
	for _, f := range cfg.Fields {
		fields = append(fields, f.Name)
		sensitive[f.Name] = (f.Sensitive && !admin(p)) || slices.Contains(decision.MaskFields, f.Name)
	}
	var records []DataRow
	switch ref.Type {
	case "table_resource":
		_, v, err := managedResource(tx, ref, false)
		if err != nil {
			return nil, err
		}
		rows, _, err := s.readTable(ctx, tx, v, fields, nil, 10001, true, decision.RowFilters)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			row := DataRow{}
			for k, v := range r {
				row[k] = encode(v)
			}
			records = append(records, row)
		}
	case "governed_dataset":
		r, err := get(tx, "dataset", ref.ID, false)
		if err != nil {
			return nil, err
		}
		_, quality, rows, err := s.assess(ctx, tx, r)
		if err != nil {
			return nil, err
		}
		if quality.Status != "passed" {
			return nil, problem(409, "QUALITY_BLOCKED", "质量门禁阻断当前交换")
		}
		records = rows
	case "raw_object":
		var task Record
		if err := tx.Where("kind='task' AND body->>'object_id'=?", ref.ID).Take(&task).Error; err != nil {
			return nil, problem(409, "RAW_NOT_ARCHIVED", "原始对象尚未归档")
		}
		data, err := decode[TaskData](task.Body)
		if err != nil {
			return nil, err
		}
		archive, err := get(tx, "archive", data.ArchiveID, false)
		if err != nil {
			return nil, err
		}
		meta, err := decode[ArchiveData](archive.Body)
		if err != nil {
			return nil, err
		}
		if meta.Start != 0 {
			return nil, problem(409, "ARCHIVE_GAP", "需要从起点完整归档后才能交换当前原始数据")
		}
		events, err := s.rawRecords(ctx, meta)
		if err != nil {
			return nil, err
		}
		current := map[string]DataRow{}
		for _, r := range events {
			var event struct {
				After DataRow `json:"after"`
				Op    string  `json:"op"`
			}
			if err = json.Unmarshal(r.Event, &event); err != nil {
				return nil, err
			}
			var key DataRow
			if err = json.Unmarshal(r.Key, &key); err != nil {
				return nil, err
			}
			if len(key) == 0 {
				return nil, invalid
			}
			k := string(encode(key))
			if event.Op == "d" {
				delete(current, k)
			} else if event.Op == "r" || event.Op == "c" || event.Op == "u" {
				current[k] = event.After
			} else {
				return nil, invalid
			}
		}
		keys := []string{}
		for k := range current {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			records = append(records, current[k])
		}
	}
	if len(records) > 10000 {
		return nil, problem(409, "ETL_LIMIT", "单次交换最多 10,000 行")
	}
	out := [][]interface{}{}
	byteCount := 0
	for _, row := range records {
		projected := rawProjection(row, fields, sensitive)
		values := make([]interface{}, len(d.Mapping))
		for i, m := range d.Mapping {
			var value interface{}
			decoder := json.NewDecoder(bytes.NewReader(projected[m.Source]))
			decoder.UseNumber()
			if e = decoder.Decode(&value); e != nil {
				return nil, e
			}
			if number, ok := value.(json.Number); ok {
				value = number.String()
			}
			values[i], e = transformETL(value, m.Rule)
			if e != nil {
				return nil, e
			}
			byteCount += len(encode(values[i]))
		}
		if byteCount > 20*1024*1024 {
			return nil, problem(409, "ETL_LIMIT", "单次交换最多 20 MiB")
		}
		out = append(out, values)
	}
	return out, nil
}
