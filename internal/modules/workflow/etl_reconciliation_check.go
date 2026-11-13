package workflow

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"
)

func etlTargetKeys(ctx context.Context, db etlQueryer, kind string, target TableConfig) ([]string, error) {
	q := `SELECT c.column_name FROM information_schema.table_constraints t JOIN information_schema.key_column_usage c ON t.constraint_schema=c.constraint_schema AND t.constraint_name=c.constraint_name AND t.table_schema=c.table_schema AND t.table_name=c.table_name WHERE t.constraint_type='PRIMARY KEY' AND t.table_schema=$1 AND t.table_name=$2 ORDER BY c.ordinal_position`
	if kind == "mysql" {
		q = strings.ReplaceAll(strings.ReplaceAll(q, "$1", "?"), "$2", "?")
	}
	rows, e := db.QueryContext(ctx, q, target.Schema, target.Table)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			return nil, e
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
func (s *Server) checkETLDelivery(ctx context.Context, runID string, targetTx *sql.Tx, progress ...func(ETLReconciliation) error) (ETLReconciliation, error) {
	report := ETLReconciliation{RunID: runID, State: "checking", InputScope: json.RawMessage("null")}
	record, e := get(s.db.WithContext(ctx), "etl_run", runID, false)
	if e != nil {
		return report, e
	}
	run, e := decode[ETLRun](record.Body)
	if e != nil {
		return report, e
	}
	if run.InputSnapshot != nil {
		run.authorization = run.InputSnapshot.Authorization
	}
	p, e := s.etlPrincipal(ctx, record.OwnerID)
	if e != nil {
		return report, e
	}
	if e = s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization); e != nil {
		return report, e
	}
	var input etlDeliveryInput
	if e = s.db.WithContext(ctx).Where("run_id=? AND state='ready'", runID).Take(&input).Error; e != nil {
		return report, problem(409, "DELIVERY_INDEX_UNAVAILABLE", "交付期望索引尚未就绪或已清理")
	}
	if !input.ExpiresAt.After(time.Now()) {
		return report, problem(409, "DELIVERY_INDEX_EXPIRED", "交付核对索引已过保留期，不能重新核对")
	}
	key, e := s.runtime.cryptScope(input.WrappedKey, false, "datamesh-etl-delivery-envelope:"+runID)
	if e != nil || len(key) != 32 {
		return report, problem(409, "DELIVERY_INDEX_KEY_UNAVAILABLE", "交付核对密钥不可用")
	}
	var columns []etlTargetColumn
	if json.Unmarshal(input.TargetColumns, &columns) != nil {
		return report, invalid
	}
	current, e := etlTargetColumns(ctx, targetTx, input.TargetKind, run.Definition.Target, run.Definition.Mapping)
	if e != nil {
		return report, e
	}
	keys, e := etlTargetKeys(ctx, targetTx, input.TargetKind, run.Definition.Target)
	if e != nil {
		return report, e
	}
	if !reflect.DeepEqual(current, columns) || !slices.Equal(keys, run.Definition.Target.Keys) {
		return report, problem(409, "TARGET_SCHEMA_CHANGED", "目标映射字段类型、主键或排序规则已变化，无法沿用旧索引核对")
	}
	source, e := get(s.db.WithContext(ctx), "source", run.Definition.Target.SourceID, false)
	if e != nil {
		return report, e
	}
	if source.Version != input.TargetVersion {
		return report, problem(409, "SOURCE_VERSION_CHANGED", "目标数据源配置已变化，无法沿用旧核对范围")
	}
	src, e := decode[SourceSecret](source.Body)
	if e != nil {
		return report, e
	}
	if src.Kind != input.TargetKind {
		return report, problem(409, "SOURCE_VERSION_CHANGED", "目标数据库类型已变化")
	}
	if e = s.runtime.allowSource(ctx, s.db, SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database}); e != nil {
		return report, e
	}
	report.KeyCount = input.KeyCount
	report.TaskVersion = run.TaskVersion
	report.ReadRows = int64(run.Read)
	report.ProcessedRows = int64(run.Read)
	if run.InputSnapshot != nil {
		report.InputSnapshotID = run.InputSnapshot.SnapshotID
		report.InputScope = encode(run.InputSnapshot)
	}
	sqlDB, e := s.db.DB()
	if e != nil {
		return report, e
	}
	indexRows, e := sqlDB.QueryContext(ctx, "SELECT key_digest,encrypted_key,expected_digest FROM etl_delivery_keys WHERE run_id=$1 ORDER BY key_digest", runID)
	if e != nil {
		return report, e
	}
	defer indexRows.Close()
	batch := []etlDeliveryKey{}
	batchValues := [][]interface{}{}
	batchBytes := 0
	authorize := func() error {
		if e := s.etlInputIdentity(ctx, p); e != nil {
			return e
		}
		return s.etlPermission(s.db.WithContext(ctx), p, run.Definition, run.authorization)
	}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if e := authorize(); e != nil {
			return e
		}
		query, args, e := etlDeliveryQuery(input.TargetKind, run.Definition.Target, run.Definition.Mapping, columns, batchValues, true)
		if e != nil {
			return e
		}
		rows, e := targetTx.QueryContext(ctx, query, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		seen := make([]bool, len(batch))
		for rows.Next() {
			var seq int
			keyValues := make([]interface{}, len(keys))
			values := make([]interface{}, len(columns))
			ptrs := []interface{}{&seq}
			for i := range keyValues {
				ptrs = append(ptrs, &keyValues[i])
			}
			for i := range values {
				ptrs = append(ptrs, &values[i])
			}
			if e = rows.Scan(ptrs...); e != nil {
				return e
			}
			if seq < 0 || seq >= len(batch) || seen[seq] {
				return problem(409, "DELIVERY_KEY_UNVERIFIED", "目标主键无法对应唯一记录，已阻断核对")
			}
			seen[seq] = true
			fields, normalized := etlDigestValues(columns, values)
			digest, e := etlRowDigest(key, runID, fields, normalized)
			if e != nil {
				return e
			}
			report.CheckedRows++
			if !hmac.Equal(digest, batch[seq].ExpectedDigest) {
				report.MismatchCount++
			}
		}
		if e = rows.Err(); e != nil {
			return e
		}
		for _, found := range seen {
			if !found {
				report.MismatchCount++
			}
		}
		batch = nil
		batchValues = nil
		batchBytes = 0
		if len(progress) > 0 {
			if e := progress[0](report); e != nil {
				return e
			}
		}
		return nil
	}
	var indexCount int64
	for indexRows.Next() {
		var row etlDeliveryKey
		if e = indexRows.Scan(&row.KeyDigest, &row.EncryptedKey, &row.ExpectedDigest); e != nil {
			return report, e
		}
		if len(row.EncryptedKey) > int(archiveChunkBytes) {
			return report, problem(409, "DELIVERY_INDEX_LIMIT", "单条核对索引超过允许大小")
		}
		if len(batch) > 0 && batchBytes+len(row.EncryptedKey) > int(archiveChunkBytes) {
			if e = flush(); e != nil {
				return report, e
			}
		}
		plain, e := (&Runtime{key: key}).cryptScope(row.EncryptedKey, false, "datamesh-etl-delivery-key:"+runID)
		if e != nil {
			return report, problem(409, "DELIVERY_INDEX_INVALID", "交付主键索引无法解密校验")
		}
		values, e := deserializeETLKey(plain)
		if e != nil || len(values) != len(keys) {
			return report, problem(409, "DELIVERY_INDEX_INVALID", "交付主键索引与目标字段不一致")
		}
		keyCols, e := etlKeyColumns(run.Definition.Target, run.Definition.Mapping, columns)
		if e != nil {
			return report, e
		}
		fields, normalized := etlDigestValues(keyCols, values)
		digest, e := etlRowDigest(key, runID+":keys", fields, normalized)
		if e != nil || !hmac.Equal(digest, row.KeyDigest) {
			return report, problem(409, "DELIVERY_INDEX_INVALID", "交付主键摘要校验失败")
		}
		batch = append(batch, row)
		batchValues = append(batchValues, values)
		batchBytes += len(row.EncryptedKey)
		indexCount++
		if len(batch) == 500 {
			if e = flush(); e != nil {
				return report, e
			}
		}
	}
	if e = indexRows.Err(); e != nil {
		return report, e
	}
	if e = flush(); e != nil {
		return report, e
	}
	if indexCount != input.KeyCount {
		return report, problem(409, "DELIVERY_INDEX_INCOMPLETE", "交付核对索引记录缺失")
	}
	if e = authorize(); e != nil {
		return report, e
	}
	report.State = "passed"
	if report.MismatchCount > 0 {
		report.State = "mismatched"
	}
	now := time.Now().UTC()
	report.CheckedAt = &now
	return report, nil
}
