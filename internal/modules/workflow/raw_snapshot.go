package workflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResourceSnapshot struct {
	Scope          string
	ID             string `gorm:"primaryKey"`
	ArchiveID      string
	ObjectID       string
	ObjectVersion  int
	State          string
	StoredBytes    int64
	RowCount       int64
	EventCount     int64
	LeaseOwner     string
	LeaseExpiresAt time.Time
	ErrorCode      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (ResourceSnapshot) TableName() string { return "resource_snapshots" }

type resourceSnapshotRow struct {
	SnapshotID    string `gorm:"primaryKey"`
	KeyDigest     []byte `gorm:"primaryKey"`
	Partition     int
	SourceOffset  int64
	Deleted       bool
	EncryptedBody []byte
}

func (resourceSnapshotRow) TableName() string { return "resource_snapshot_rows" }

type snapshotBody struct {
	Key DataRow `json:"key"`
	Row DataRow `json:"row"`
}

func (rt *Runtime) snapshotKeyDigest(snapshotID string, key DataRow) []byte {
	derive := hmac.New(sha256.New, rt.key)
	derive.Write([]byte("datamesh-raw-snapshot-key-v1:" + snapshotID))
	hash := hmac.New(sha256.New, derive.Sum(nil))
	hash.Write(encode(key))
	return hash.Sum(nil)
}
func rawSnapshotEvent(record RawRecord, obj Object) (snapshotBody, bool, error) {
	key, e := decodeCaptureKey(record.Key)
	if e != nil {
		return snapshotBody{}, false, e
	}
	event, e := decodeCaptureEvent(record.Event)
	if e != nil {
		return snapshotBody{}, false, e
	}
	if e = normalizeSnapshotNumbers(key, obj); e != nil {
		return snapshotBody{}, false, e
	}
	if e = normalizeSnapshotNumbers(event.After, obj); e != nil {
		return snapshotBody{}, false, e
	}
	primary := []string{}
	for _, field := range obj.Fields {
		if field.PrimaryKey {
			primary = append(primary, field.Name)
		}
	}
	if len(primary) == 0 || len(key) != len(primary) {
		return snapshotBody{}, false, problem(409, "CDC_KEY_MISMATCH", "事件主键与对象版本不一致")
	}
	for _, name := range primary {
		value, found := key[name]
		if !found || string(value) == "null" {
			return snapshotBody{}, false, problem(409, "CDC_KEY_MISMATCH", "事件缺少对象主键")
		}
	}
	if event.Op == "d" {
		return snapshotBody{Key: key}, true, nil
	}
	if event.Op != "r" && event.Op != "c" && event.Op != "u" {
		return snapshotBody{}, false, problem(409, "CDC_EVENT_UNSUPPORTED", "原始事件操作类型无法重建当前状态")
	}
	if len(event.After) != len(obj.Fields) {
		return snapshotBody{}, false, problem(409, "CDC_SCHEMA_CHANGED", "归档事件字段与对象版本不一致，请核查表结构")
	}
	for _, field := range obj.Fields {
		value, found := event.After[field.Name]
		if !found {
			return snapshotBody{}, false, problem(409, "CDC_SCHEMA_CHANGED", "归档事件缺少对象字段")
		}
		if string(value) == `"__debezium_unavailable_value"` || strings.HasPrefix(string(value), `"__DATAMESH_UNAVAILABLE_`) {
			return snapshotBody{}, false, problem(409, "CDC_PARTIAL_ROW", "日志未提供完整字段值，请核对复制标识、完整补充日志或大字段采集配置")
		}
	}
	for _, name := range primary {
		if !bytes.Equal(bytes.TrimSpace(key[name]), bytes.TrimSpace(event.After[name])) {
			return snapshotBody{}, false, problem(409, "CDC_KEY_MISMATCH", "事件内容与主键不一致")
		}
	}
	return snapshotBody{Key: key, Row: event.After}, false, nil
}

func (s *Server) buildRawSnapshot(ctx context.Context, task Record, meta ArchiveData, authorize func() error, progress ...func(int64) error) (string, error) {
	data, e := decode[TaskData](task.Body)
	if e != nil {
		return "", e
	}
	objectRecord, e := get(s.db.WithContext(ctx), "object", data.ObjectID, false)
	if e != nil {
		return "", e
	}
	obj, e := decode[Object](objectRecord.Body)
	if e != nil {
		return "", e
	}
	if data.SchemaVersion != objectRecord.Version {
		return "", problem(409, "CDC_SCHEMA_CHANGED", "归档任务的对象版本已变化")
	}
	if meta.ObjectID != obj.ObjectID || meta.Start != 0 {
		return "", problem(409, "ARCHIVE_GAP", "归档没有覆盖对象的完整起点")
	}
	if meta.ManifestVersion == 2 && meta.Boundary.ObjectVersion != objectRecord.Version {
		return "", problem(409, "CDC_SCHEMA_CHANGED", "归档对象版本与当前契约不一致")
	}
	if data.ArchiveID == "" {
		return "", problem(409, "RAW_NOT_ARCHIVED", "原始对象尚未归档")
	}
	candidateID, e := newID()
	if e != nil {
		return "", e
	}
	candidate := ResourceSnapshot{ID: candidateID, ArchiveID: data.ArchiveID, ObjectID: obj.ObjectID, ObjectVersion: objectRecord.Version, State: "building"}
	if e = s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "archive_id"}, {Name: "object_version"}, {Name: "scope"}}, DoNothing: true}).Create(&candidate).Error; e != nil {
		return "", e
	}
	var snapshot ResourceSnapshot
	token, e := newID()
	if e != nil {
		return "", e
	}
	for {
		claimed := false
		e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("archive_id=? AND object_version=? AND scope=''", data.ArchiveID, objectRecord.Version).Take(&snapshot).Error; err != nil {
				return err
			}
			if snapshot.State == "ready" {
				return tx.Model(&snapshot).Update("updated_at", time.Now()).Error
			}
			if snapshot.LeaseOwner != "" && snapshot.LeaseExpiresAt.After(time.Now()) {
				return nil
			}
			snapshot.State = "building"
			snapshot.ErrorCode = ""
			snapshot.LeaseOwner = token
			snapshot.LeaseExpiresAt = time.Now().Add(90 * time.Second)
			if err := tx.Save(&snapshot).Error; err != nil {
				return err
			}
			claimed = true
			return nil
		})
		if e != nil {
			return "", e
		}
		if snapshot.State == "ready" {
			return snapshot.ID, nil
		}
		if claimed {
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	events := int64(0)
	batchBytes := int64(0)
	batch := map[string]resourceSnapshotRow{}
	order := []string{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if authorize != nil {
			if err := authorize(); err != nil {
				return err
			}
		}
		rows := make([]resourceSnapshotRow, 0, len(batch))
		for _, key := range order {
			rows = append(rows, batch[key])
		}
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current ResourceSnapshot
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND lease_owner=? AND lease_expires_at>CURRENT_TIMESTAMP AND state='building'", snapshot.ID, token).Take(&current).Error; err != nil {
				return problem(409, "SNAPSHOT_LEASE_LOST", "原始快照构建租约已失效")
			}
			delta := int64(0)
			byteDelta := int64(0)
			updates := make([]resourceSnapshotRow, 0, len(rows))
			digests := make([][]byte, 0, len(rows))
			for _, row := range rows {
				digests = append(digests, row.KeyDigest)
			}
			var existing []struct {
				KeyDigest    []byte
				Partition    int
				SourceOffset int64
				Deleted      bool
				BodyBytes    int64
			}
			if err := tx.Model(&resourceSnapshotRow{}).Select("key_digest", "partition", "source_offset", "deleted", "octet_length(encrypted_body) AS body_bytes").Where("snapshot_id=? AND key_digest IN ?", snapshot.ID, digests).Find(&existing).Error; err != nil {
				return err
			}
			previousByKey := map[string]int{}
			for i, old := range existing {
				previousByKey[string(old.KeyDigest)] = i
			}
			for _, row := range rows {
				index, found := previousByKey[string(row.KeyDigest)]
				if found {
					old := existing[index]
					if old.Partition != row.Partition {
						return problem(409, "CDC_KEY_ORDER_UNVERIFIED", "同一主键跨分区出现，无法确认完整顺序")
					}
					if old.SourceOffset >= row.SourceOffset {
						continue
					}
					byteDelta -= old.BodyBytes
					if old.Deleted && !row.Deleted {
						delta++
					}
					if !old.Deleted && row.Deleted {
						delta--
					}
				} else if !row.Deleted {
					delta++
				}
				byteDelta += int64(len(row.EncryptedBody))
				updates = append(updates, row)
			}
			if current.RowCount+delta > int64(s.etl.MaxRows) {
				return problem(409, "ETL_LIMIT", "原始快照行数超过当前交换上限")
			}
			if current.StoredBytes+byteDelta > s.etl.MaxBytes {
				return problem(409, "SNAPSHOT_LIMIT", "原始快照暂存字节数超过配置上限")
			}
			if len(updates) > 0 {
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "snapshot_id"}, {Name: "key_digest"}}, DoUpdates: clause.AssignmentColumns([]string{"source_offset", "deleted", "encrypted_body"})}).Create(&updates).Error; err != nil {
					return err
				}
			}
			return tx.Model(&ResourceSnapshot{}).Where("id=? AND lease_owner=?", snapshot.ID, token).Updates(map[string]interface{}{"stored_bytes": current.StoredBytes + byteDelta, "event_count": events, "row_count": current.RowCount + delta, "lease_expires_at": time.Now().Add(90 * time.Second), "updated_at": time.Now()}).Error
		})
		if err == nil {
			batch = map[string]resourceSnapshotRow{}
			order = nil
			batchBytes = 0
			if len(progress) > 0 {
				err = progress[0](events)
			}
		}
		return err
	}
	e = s.streamArchive(ctx, meta, func(record RawRecord) error {
		body, deleted, err := rawSnapshotEvent(record, obj)
		if err != nil {
			return err
		}
		digest := s.runtime.snapshotKeyDigest(snapshot.ID, body.Key)
		key := string(digest)

		encrypted, err := s.runtime.cryptScope(encode(body), true, "datamesh-raw-snapshot:"+snapshot.ID)
		if err != nil {
			return err
		}
		if int64(len(encrypted)) > archiveChunkBytes {
			return problem(409, "SNAPSHOT_LIMIT", "单条快照记录超过允许大小")
		}
		if batchBytes+int64(len(encrypted)) > archiveChunkBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		if old, exists := batch[key]; exists {
			if old.Partition != record.Partition {
				return problem(409, "CDC_KEY_ORDER_UNVERIFIED", "同一主键跨分区无法核对顺序")
			}
		} else {
			order = append(order, key)
		}
		batchBytes += int64(len(encrypted))
		batch[key] = resourceSnapshotRow{SnapshotID: snapshot.ID, KeyDigest: digest, Partition: record.Partition, SourceOffset: record.Offset, Deleted: deleted, EncryptedBody: encrypted}
		events++
		if events%500 == 0 {
			return flush()
		}
		return nil
	})
	if e == nil {
		e = flush()
	}
	finish, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	state := "ready"
	code := ""
	if e != nil {
		state = "failed"
		code = captureErrorCode(e)
	}
	result := s.db.WithContext(finish).Model(&ResourceSnapshot{}).Where("id=? AND lease_owner=? AND lease_expires_at>CURRENT_TIMESTAMP", snapshot.ID, token).Updates(map[string]interface{}{"state": state, "event_count": events, "error_code": code, "lease_owner": "", "updated_at": time.Now()})
	if e != nil {
		return "", e
	}
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", problem(409, "SNAPSHOT_LEASE_LOST", "快照完成状态尚未确认")
	}
	return snapshot.ID, nil
}

func (s *Server) rawSnapshotRows(ctx context.Context, id string, consume func(DataRow) error) error {
	var snapshot ResourceSnapshot
	if e := s.db.WithContext(ctx).Where("id=? AND state='ready'", id).Take(&snapshot).Error; e != nil {
		return e
	}

	sqlDB, e := s.db.DB()
	if e != nil {
		return e
	}
	rows, e := sqlDB.QueryContext(ctx, "SELECT key_digest,encrypted_body FROM resource_snapshot_rows WHERE snapshot_id=$1 AND deleted=false ORDER BY key_digest", id)
	if e != nil {
		return e
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var digest, ciphertext []byte
		if e = rows.Scan(&digest, &ciphertext); e != nil {
			return e
		}
		plain, e := s.runtime.cryptScope(ciphertext, false, "datamesh-raw-snapshot:"+id)
		if e != nil {
			return problem(409, "SNAPSHOT_DECRYPT_FAILED", "快照内容无法校验")
		}
		var body snapshotBody
		if json.Unmarshal(plain, &body) != nil {
			return problem(409, "SNAPSHOT_FORMAT_INVALID", "快照内容格式无效")
		}
		if !hmac.Equal(digest, s.runtime.snapshotKeyDigest(id, body.Key)) {
			return problem(409, "SNAPSHOT_KEY_INVALID", "快照主键摘要不一致")
		}
		if e = consume(body.Row); e != nil {
			return e
		}
		count++
	}
	if e = rows.Err(); e != nil {
		return e
	}

	if count != snapshot.RowCount {
		return problem(409, "SNAPSHOT_COUNT_INVALID", "快照行数与完成记录不一致")
	}
	return nil
}

var snapshotDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]{1,4})?$`)

func normalizeSnapshotNumbers(row DataRow, obj Object) error {
	for _, field := range obj.Fields {
		value, ok := row[field.Name]
		if !ok || string(value) == "null" {
			continue
		}
		if field.LogicalType != "integer" && field.LogicalType != "decimal" {
			continue
		}
		text := string(value)
		if len(text) > 0 && text[0] == '"' {
			if json.Unmarshal(value, &text) != nil {
				return problem(409, "CDC_NUMERIC_INVALID", "采集数值格式无效")
			}
		}
		if len(text) > 4096 || !snapshotDecimal.MatchString(text) {
			return problem(409, "CDC_NUMERIC_INVALID", "采集数值格式或长度无效")
		}
		if i := strings.IndexAny(text, "eE"); i >= 0 {
			exponent, err := strconv.Atoi(text[i+1:])
			if err != nil || exponent > 4096 || exponent < -4096 {
				return problem(409, "CDC_NUMERIC_INVALID", "采集数值指数超过支持范围")
			}
		}
		number, ok := new(big.Rat).SetString(text)
		if !ok {
			return problem(409, "CDC_NUMERIC_INVALID", "采集数值无法无损解析")
		}
		if field.LogicalType == "integer" {
			if !number.IsInt() {
				return problem(409, "CDC_NUMERIC_INVALID", "整数字段包含小数")
			}
			row[field.Name] = json.RawMessage(number.Num().String())
		} else {
			row[field.Name] = encode(text)
		}
	}
	return nil
}

func (s *Server) snapshotPage(ctx context.Context, id string, start, limit int) ([]DataRow, error) {
	if start < 0 || limit < 1 || limit > 100 {
		return nil, invalid
	}
	db, e := s.db.DB()
	if e != nil {
		return nil, e
	}
	rows, e := db.QueryContext(ctx, "SELECT key_digest,encrypted_body FROM resource_snapshot_rows WHERE snapshot_id=$1 AND deleted=false ORDER BY key_digest OFFSET $2 LIMIT $3", id, start, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DataRow{}
	size := 0
	for rows.Next() {
		var digest, cipher []byte
		if e = rows.Scan(&digest, &cipher); e != nil {
			return nil, e
		}
		plain, e := s.runtime.cryptScope(cipher, false, "datamesh-raw-snapshot:"+id)
		if e != nil {
			return nil, e
		}
		var body snapshotBody
		if json.Unmarshal(plain, &body) != nil || !hmac.Equal(digest, s.runtime.snapshotKeyDigest(id, body.Key)) {
			return nil, problem(409, "SNAPSHOT_KEY_INVALID", "快照内容校验失败")
		}
		size += len(plain)
		if size > 20*1024*1024 {
			return nil, problem(409, "DELIVERY_LIMIT", "分页内容超过20 MiB，请减小页大小")
		}
		out = append(out, body.Row)
	}
	return out, rows.Err()
}
