package workflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type etlSavedKeyValue struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

func serializeETLKey(values []interface{}) ([]byte, error) {
	out := make([]etlSavedKeyValue, 0, len(values))
	for _, value := range values {
		kind := "value"
		switch value.(type) {
		case []byte:
			kind = "bytes"
		case time.Time:
			kind = "time"
		}
		raw, e := json.Marshal(value)
		if e != nil {
			return nil, e
		}
		out = append(out, etlSavedKeyValue{Kind: kind, Value: raw})
	}
	return json.Marshal(out)
}
func deserializeETLKey(raw []byte) ([]interface{}, error) {
	var saved []etlSavedKeyValue
	if json.Unmarshal(raw, &saved) != nil {
		return nil, problem(409, "DELIVERY_INDEX_INVALID", "交付核对索引格式无效")
	}
	out := make([]interface{}, 0, len(saved))
	for _, entry := range saved {
		switch entry.Kind {
		case "bytes":
			var value []byte
			if json.Unmarshal(entry.Value, &value) != nil {
				return nil, invalid
			}
			out = append(out, value)
		case "time":
			var value time.Time
			if json.Unmarshal(entry.Value, &value) != nil {
				return nil, invalid
			}
			out = append(out, value)
		case "value":
			var value interface{}
			decoder := json.NewDecoder(bytes.NewReader(entry.Value))
			decoder.UseNumber()
			if e := decoder.Decode(&value); e != nil {
				return nil, e
			}
			if n, ok := value.(json.Number); ok {
				value = n.String()
			}
			out = append(out, value)
		default:
			return nil, problem(409, "DELIVERY_INDEX_INVALID", "交付主键类型无法还原")
		}
	}
	return out, nil
}
func etlDigestValues(columns []etlTargetColumn, values []interface{}) ([]Field, []interface{}) {
	fields := make([]Field, len(columns))
	normalized := append([]interface{}{}, values...)
	for i, col := range columns {
		fields[i] = col.Field
		if col.Char {
			switch v := values[i].(type) {
			case string:
				normalized[i] = strings.TrimRight(v, " ")
			case []byte:
				normalized[i] = bytes.TrimRight(v, " ")
			}
		}
	}
	return fields, normalized
}
func etlKeyColumns(target TableConfig, mapping []ETLMapping, columns []etlTargetColumn) ([]etlTargetColumn, error) {
	out := []etlTargetColumn{}
	for _, key := range target.Keys {
		i := slices.IndexFunc(mapping, func(m ETLMapping) bool { return m.Target == key })
		if i < 0 {
			return nil, invalid
		}
		out = append(out, columns[i])
	}
	return out, nil
}
func (s *Server) initETLDelivery(ctx context.Context, runID string, run *ETLRun, db etlQueryer, kind string) error {
	columns, e := etlTargetColumns(ctx, db, kind, run.Definition.Target, run.Definition.Mapping)
	if e != nil {
		return e
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		return e
	}
	wrapped, e := s.runtime.cryptScope(key, true, "datamesh-etl-delivery-envelope:"+runID)
	if e != nil {
		return e
	}
	source, e := get(s.db.WithContext(ctx), "source", run.Definition.Target.SourceID, false)
	if e != nil {
		return e
	}
	input := etlDeliveryInput{RunID: runID, State: "building", WrappedKey: wrapped, TargetKind: kind, TargetVersion: source.Version, TargetColumns: encode(columns), ReceiptVersion: 1, ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}
	if e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := etlLeaseValid(tx, runID, run.leaseOwner); err != nil {
			return err
		}
		return tx.Create(&input).Error
	}); e != nil {
		return e
	}
	run.delivery = &etlDeliverySession{Input: input, Key: key, Columns: columns}
	return nil
}
func (s *Server) indexETLBatch(ctx context.Context, runID string, run *ETLRun, targetTx *sql.Tx, kind string, batch [][]interface{}) error {
	session := run.delivery
	if session == nil {
		return problem(409, "DELIVERY_INDEX_MISSING", "交付期望索引未建立")
	}
	query, args, e := etlExpectedQuery(kind, run.Definition.Target, run.Definition.Mapping, session.Columns, batch)
	if e != nil {
		return e
	}
	rows, e := targetTx.QueryContext(ctx, query, args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	keyColumns, e := etlKeyColumns(run.Definition.Target, run.Definition.Mapping, session.Columns)
	if e != nil {
		return e
	}
	records := map[string]etlDeliveryKey{}
	order := []string{}
	seen := 0
	for rows.Next() {
		var sequence int
		keyValues := make([]interface{}, len(keyColumns))
		values := make([]interface{}, len(session.Columns))
		ptrs := []interface{}{&sequence}
		for i := range keyValues {
			ptrs = append(ptrs, &keyValues[i])
		}
		for i := range values {
			ptrs = append(ptrs, &values[i])
		}
		if e = rows.Scan(ptrs...); e != nil {
			return e
		}
		if sequence != seen || sequence >= len(batch) {
			return problem(409, "DELIVERY_KEY_UNVERIFIED", "本批主键未能对应唯一的实际目标记录")
		}
		seen++
		fields, normalized := etlDigestValues(session.Columns, values)
		expected, e := etlRowDigest(session.Key, runID, fields, normalized)
		if e != nil {
			return e
		}
		keyFields, normalizedKeys := etlDigestValues(keyColumns, keyValues)
		digest, e := etlRowDigest(session.Key, runID+":keys", keyFields, normalizedKeys)
		if e != nil {
			return e
		}
		plain, e := serializeETLKey(keyValues)
		if e != nil {
			return e
		}
		cipher, e := (&Runtime{key: session.Key}).cryptScope(plain, true, "datamesh-etl-delivery-key:"+runID)
		if e != nil {
			return e
		}
		session.StoredBytes += int64(len(cipher) + len(digest) + len(expected))
		if session.StoredBytes > s.etl.MaxBytes+int64(s.etl.MaxRows)*256 {
			return problem(409, "DELIVERY_INDEX_LIMIT", "交付核对索引超过配置容量，未提交目标事务")
		}
		record := etlDeliveryKey{RunID: runID, KeyDigest: digest, EncryptedKey: cipher, ExpectedDigest: expected, Sequence: int64(run.Read - len(batch) + sequence + 1)}
		identity := string(digest)
		if _, found := records[identity]; !found {
			order = append(order, identity)
		}
		records[identity] = record
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if seen != len(batch) {
		return problem(409, "DELIVERY_KEY_UNVERIFIED", "本批有主键无法按目标真实类型、排序规则或字符集无损定位")
	}
	updates := make([]etlDeliveryKey, 0, len(order))
	for _, key := range order {
		updates = append(updates, records[key])
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := etlLeaseValid(tx, runID, run.leaseOwner); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}, {Name: "key_digest"}}, DoUpdates: clause.AssignmentColumns([]string{"encrypted_key", "expected_digest", "sequence"}), Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "EXCLUDED.sequence>etl_delivery_keys.sequence"}}}}).Create(&updates).Error; err != nil {
			return err
		}
		return tx.Model(&etlDeliveryInput{}).Where("run_id=? AND state='building'", runID).Updates(map[string]interface{}{"stored_bytes": session.StoredBytes, "updated_at": time.Now()}).Error
	})
}
func (s *Server) sealETLDelivery(ctx context.Context, runID string, run *ETLRun) error {
	if run.delivery == nil {
		return invalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := etlLeaseValid(tx, runID, run.leaseOwner); e != nil {
			return e
		}
		var count int64
		if e := tx.Model(&etlDeliveryKey{}).Where("run_id=?", runID).Count(&count).Error; e != nil {
			return e
		}
		if e := tx.Model(&etlDeliveryInput{}).Where("run_id=? AND state='building'", runID).Updates(map[string]interface{}{"state": "ready", "key_count": count, "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		scope := json.RawMessage("null")
		snapshotID := ""
		if run.InputSnapshot != nil {
			scope = encode(run.InputSnapshot)
			snapshotID = run.InputSnapshot.SnapshotID
		}
		return tx.Model(&ETLReconciliation{}).Where("run_id=? AND state='pending'", runID).Updates(map[string]interface{}{"input_snapshot_id": snapshotID, "input_scope": scope, "read_rows": run.Read, "processed_rows": run.Read, "key_count": count, "updated_at": time.Now()}).Error
	})
}
