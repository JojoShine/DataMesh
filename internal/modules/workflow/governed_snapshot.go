package workflow

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

func resultDataRow(fields []Field, values []interface{}) (DataRow, error) {
	out := DataRow{}
	for i, field := range fields {
		value := values[i]
		if raw, ok := value.([]byte); ok {
			value = string(raw)
		}
		if stamp, ok := value.(time.Time); ok {
			if field.LogicalType == "date" {
				value = stamp.Format("2006-01-02")
			} else {
				value = stamp.UTC().Format(time.RFC3339Nano)
			}
		}
		if field.LogicalType == "boolean" && value != nil {
			switch v := value.(type) {
			case int64:
				if v == 0 || v == 1 {
					value = v == 1
				}
			case string:
				if v == "0" || v == "1" {
					value = v == "1"
				}
			}
		}
		raw, e := json.Marshal(value)
		if e != nil {
			return nil, problem(409, "RESULT_VALUE_INVALID", "结果字段值无法无损序列化")
		}
		out[field.Name] = raw
	}
	return out, normalizeSnapshotNumbers(out, Object{Fields: fields})
}
func resultKey(row DataRow, fields []Field) (DataRow, error) {
	key := DataRow{}
	for _, f := range fields {
		if f.PrimaryKey {
			value, exists := row[f.Name]
			if !exists || string(value) == "null" {
				return nil, problem(409, "RESULT_KEY_REQUIRED", "结果记录缺少主键")
			}
			key[f.Name] = value
		}
	}
	if len(key) == 0 {
		return nil, problem(409, "RESULT_KEY_REQUIRED", "结果契约缺少主键")
	}
	return key, nil
}
func logicalRowsEqual(left, right DataRow, fields []Field) bool {
	for _, field := range fields {
		a, b := left[field.Name], right[field.Name]
		if len(a) == 0 || len(b) == 0 {
			return false
		}
		if string(a) == string(b) {
			continue
		}
		if string(a) == "null" || string(b) == "null" {
			return false
		}
		switch field.LogicalType {
		case "decimal", "integer":
			at, bt := string(a), string(b)
			if json.Unmarshal(a, &at) != nil {
				at = string(a)
			}
			if json.Unmarshal(b, &bt) != nil {
				bt = string(b)
			}
			av, aok := new(big.Rat).SetString(at)
			bv, bok := new(big.Rat).SetString(bt)
			if !aok || !bok || av.Cmp(bv) != 0 {
				return false
			}
		case "datetime":
			var at, bt string
			if json.Unmarshal(a, &at) != nil || json.Unmarshal(b, &bt) != nil {
				return false
			}
			parse := func(text string) (time.Time, error) {
				for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999"} {
					if stamp, e := time.Parse(layout, text); e == nil {
						return stamp, nil
					}
				}
				return time.Time{}, invalid
			}
			av, ae := parse(at)
			bv, be := parse(bt)
			if ae != nil || be != nil || !av.Equal(bv) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (s *Server) buildGovernedResult(ctx context.Context, job GovernedCheck, token string, p identity.Principal, input GovernedBindingInput, data DatasetData, object Object, inputID string, authorize func() error) (Quality, error) {
	quality := Quality{DatasetID: job.DatasetID, ContractVersion: input.ContractVersion, RuleVersion: input.RuleVersion, Status: "blocked", FailureReasons: []Failure{}, CheckedRange: encode(map[string]interface{}{})}
	source, src, db, e := s.sourceForRead(ctx, s.db, p, input.SourceID)
	if e != nil {
		return quality, e
	}
	defer db.Close()
	if source.Version != input.SourceVersion {
		return quality, problem(409, "SOURCE_VERSION_CHANGED", "结果数据源版本已变化")
	}
	physical, e := discoverTable(ctx, db, src, input.Schema, input.Table)
	if e != nil {
		return quality, e
	}
	expected := TableConfig{Fields: data.Definition.Fields, Keys: []string{}}
	for _, f := range expected.Fields {
		if f.PrimaryKey {
			expected.Keys = append(expected.Keys, f.Name)
		}
	}
	if !slices.Equal(physical.Keys, expected.Keys) {
		return quality, problem(409, "RESULT_KEY_REQUIRED", "结果表主键已变化")
	}
	if e = validateETLMapping(input.Mapping, expected, physical); e != nil {
		return quality, e
	}
	var rawSnapshot ResourceSnapshot
	if e = s.db.WithContext(ctx).Where("id=? AND state='ready'", inputID).Take(&rawSnapshot).Error; e != nil {
		return quality, e
	}
	snapshot := ResourceSnapshot{ID: job.ResultSnapshotID, Scope: job.ResultSnapshotID, ArchiveID: input.InputArchiveID, ObjectID: object.ObjectID, ObjectVersion: rawSnapshot.ObjectVersion, State: "building", LeaseOwner: token, LeaseExpiresAt: time.Now().Add(90 * time.Second)}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.governedCheckFence(tx, job.ID, token); err != nil {
			return err
		}
		if err := tx.Where("snapshot_id=?", snapshot.ID).Delete(&resourceSnapshotRow{}).Error; err != nil {
			return err
		}
		return tx.Save(&snapshot).Error
	})
	if e != nil {
		return quality, e
	}
	sourceTx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return quality, e
	}
	defer sourceTx.Rollback()
	if e = etlSessionTimeZone(ctx, sourceTx, src.Kind); e != nil {
		return quality, e
	}
	table := quoteIdentifier(src.Kind, input.Schema) + "." + quoteIdentifier(src.Kind, input.Table)
	var count int64
	if e = sourceTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM "+table+" LIMIT "+strconv.Itoa(s.etl.MaxRows+1)+") bounded_result").Scan(&count); e != nil {
		return quality, e
	}
	if count > int64(s.etl.MaxRows) {
		return quality, problem(409, "RESULT_LIMIT", "治理结果超过配置行数上限")
	}
	if count != rawSnapshot.RowCount {
		return quality, problem(409, "RESULT_RECONCILIATION_FAILED", "治理结果行数与批准的原始快照不一致")
	}
	rule, e := ruleVersion(s.db.WithContext(ctx), data.RuleID, data.RuleVersion)
	if e != nil {
		return quality, e
	}
	ruleData, e := decode[RuleData](rule.Body)
	if e != nil {
		return quality, e
	}
	for _, check := range ruleData.Definition.Checks {
		if check.Kind == "business_key" {
			var duplicate bool
			q := "SELECT EXISTS(SELECT 1 FROM " + table + " GROUP BY " + quoteIdentifier(src.Kind, check.Field) + " HAVING COUNT(*)>1)"
			if e = sourceTx.QueryRowContext(ctx, q).Scan(&duplicate); e != nil {
				return quality, e
			}
			if duplicate {
				return quality, problem(409, "QUALITY_BLOCKED", "治理结果存在重复业务主键")
			}
		}
	}
	columns := []string{}
	keys := []string{}
	for _, field := range expected.Fields {
		columns = append(columns, quoteIdentifier(src.Kind, field.Name))
	}
	for _, key := range physical.Keys {
		keys = append(keys, quoteIdentifier(src.Kind, key))
	}
	rows, e := sourceTx.QueryContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM "+table+" ORDER BY "+strings.Join(keys, ","))
	if e != nil {
		return quality, e
	}
	batch := []resourceSnapshotRow{}
	batchBytes := int64(0)
	totalBytes := int64(0)
	processed := int64(0)
	reasons := map[string]int{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := authorize(); err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.governedCheckFence(tx, job.ID, token); err != nil {
				return err
			}
			if err := tx.Create(&batch).Error; err != nil {
				return err
			}
			if err := tx.Model(&ResourceSnapshot{}).Where("id=? AND lease_owner=?", snapshot.ID, token).Updates(map[string]interface{}{"row_count": processed, "stored_bytes": totalBytes, "lease_expires_at": time.Now().Add(90 * time.Second), "updated_at": time.Now()}).Error; err != nil {
				return err
			}
			return tx.Model(&GovernedCheck{}).Where("id=? AND lease_owner=?", job.ID, token).Update("processed", processed).Error
		}); err != nil {
			return err
		}
		batch = nil
		batchBytes = 0
		return nil
	}
	for rows.Next() {
		values := make([]interface{}, len(expected.Fields))
		ptrs := make([]interface{}, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if e = rows.Scan(ptrs...); e != nil {
			rows.Close()
			return quality, e
		}
		row, err := resultDataRow(expected.Fields, values)
		if err != nil {
			rows.Close()
			return quality, err
		}
		pass, fail, failures := evaluate(ruleData.Definition, []DataRow{row})
		quality.Passed += pass
		quality.Failed += fail
		for _, failure := range failures {
			reasons[failure.Code] += failure.Count
		}
		key, err := resultKey(row, expected.Fields)
		if err != nil {
			rows.Close()
			return quality, err
		}
		cipher, err := s.runtime.cryptScope(encode(snapshotBody{Key: key, Row: row}), true, "datamesh-raw-snapshot:"+snapshot.ID)
		if err != nil {
			rows.Close()
			return quality, err
		}
		size := int64(len(cipher))
		if size > archiveChunkBytes || totalBytes+size > s.etl.MaxBytes {
			rows.Close()
			return quality, problem(409, "RESULT_LIMIT", "治理结果暂存大小超过配置上限")
		}
		if len(batch) > 0 && batchBytes+size > archiveChunkBytes {
			if e = flush(); e != nil {
				rows.Close()
				return quality, e
			}
		}
		processed++
		totalBytes += size
		batchBytes += size
		batch = append(batch, resourceSnapshotRow{SnapshotID: snapshot.ID, KeyDigest: s.runtime.snapshotKeyDigest(snapshot.ID, key), SourceOffset: processed, EncryptedBody: cipher})
		if len(batch) == 500 {
			if e = flush(); e != nil {
				rows.Close()
				return quality, e
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return quality, e
	}
	if e = flush(); e != nil {
		return quality, e
	}
	if processed != count {
		return quality, problem(409, "RESULT_RECONCILIATION_FAILED", "结果读取行数与一致性范围不一致")
	}
	for code, n := range reasons {
		quality.FailureReasons = append(quality.FailureReasons, Failure{Code: code, Count: n})
	}
	slices.SortFunc(quality.FailureReasons, func(a, b Failure) int { return strings.Compare(a.Code, b.Code) })
	if quality.Failed > 0 {
		return quality, problem(409, "QUALITY_BLOCKED", "治理结果存在未通过质量门禁的数据")
	}
	if e = s.compareGovernedInput(ctx, inputID, snapshot.ID, input.Mapping, expected.Fields, authorize); e != nil {
		return quality, e
	}
	quality.Status = "passed"
	quality.CheckedRange = encode(map[string]interface{}{"input_snapshot_id": inputID, "result_snapshot_id": snapshot.ID, "manifest_id": input.InputArchiveID, "row_count": processed, "checked_at": time.Now().UTC(), "contract_version": input.ContractVersion, "rule_version": input.RuleVersion})
	return quality, nil
}
func (s *Server) compareGovernedInput(ctx context.Context, inputID, resultID string, mapping []ETLMapping, fields []Field, authorize func() error) error {
	expected := map[string]DataRow{}
	digests := [][]byte{}
	batchBytes := int64(0)
	flush := func() error {
		if len(digests) == 0 {
			return nil
		}
		if e := authorize(); e != nil {
			return e
		}
		rows, e := s.db.WithContext(ctx).Model(&resourceSnapshotRow{}).Where("snapshot_id=? AND key_digest IN ? AND deleted=false", resultID, digests).Rows()
		if e != nil {
			return e
		}
		defer rows.Close()
		foundRows := 0
		for rows.Next() {
			var row resourceSnapshotRow
			if e = s.db.ScanRows(rows, &row); e != nil {
				return e
			}
			foundRows++
			plain, e := s.runtime.cryptScope(row.EncryptedBody, false, "datamesh-raw-snapshot:"+resultID)
			if e != nil {
				return e
			}
			var body snapshotBody
			if json.Unmarshal(plain, &body) != nil || !hmac.Equal(row.KeyDigest, s.runtime.snapshotKeyDigest(resultID, body.Key)) {
				return problem(409, "RESULT_SNAPSHOT_INVALID", "治理结果暂存无法校验")
			}
			want, found := expected[string(row.KeyDigest)]
			if !found || !logicalRowsEqual(want, body.Row, fields) {
				return problem(409, "RESULT_RECONCILIATION_FAILED", "实际物化结果与批准的输入范围或字段转换不一致")
			}
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if foundRows != len(digests) {
			return problem(409, "RESULT_RECONCILIATION_FAILED", "治理结果缺少批准快照中的主键")
		}

		expected = map[string]DataRow{}
		digests = nil
		batchBytes = 0
		return nil
	}
	e := s.rawSnapshotRows(ctx, inputID, func(row DataRow) error {
		projected := DataRow{}
		for _, m := range mapping {
			var value interface{}
			raw, exists := row[m.Source]
			if !exists {
				return problem(409, "RESULT_SCHEMA_CHANGED", "输入快照缺少映射字段")
			}
			if m.Rule == "copy" {
				projected[m.Target] = raw
				continue
			}
			if string(raw) != "null" {
				var text string
				if json.Unmarshal(raw, &text) != nil {
					return problem(409, "RESULT_SCHEMA_CHANGED", "文本转换的输入类型不一致")
				}
				value = text
			}
			transformed, e := transformETL(value, m.Rule)
			if e != nil {
				return e
			}
			projected[m.Target] = encode(transformed)
		}
		key, e := resultKey(projected, fields)
		if e != nil {
			return e
		}
		size := int64(len(encode(projected)))
		if size > archiveChunkBytes {
			return problem(409, "RESULT_LIMIT", "单条治理记录超过允许大小")
		}
		if len(digests) > 0 && batchBytes+size > archiveChunkBytes {
			if e = flush(); e != nil {
				return e
			}
		}
		digest := s.runtime.snapshotKeyDigest(resultID, key)
		expected[string(digest)] = projected
		digests = append(digests, digest)
		batchBytes += size
		if len(digests) == 500 {
			return flush()
		}
		return nil
	})
	if e != nil {
		return e
	}
	return flush()
}
