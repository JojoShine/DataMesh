package workflow

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/minio/minio-go/v7"
	"github.com/segmentio/kafka-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type archiveStoredObject struct {
	Key       string `gorm:"primaryKey"`
	ObjectID  string
	State     string
	UpdatedAt time.Time
}

func (archiveStoredObject) TableName() string { return "archive_objects" }

func (s *Server) putArchiveObject(ctx context.Context, objectID, key string, raw []byte, contentType string) error {
	stage := archiveStoredObject{Key: key, ObjectID: objectID, State: "staging", UpdatedAt: time.Now()}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]interface{}{"updated_at": time.Now()}), Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "archive_objects.state <> 'deleting'"}}}}).Create(&stage)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return problem(409, "ARCHIVE_STORE_BUSY", "归档对象正在回收，请稍后重试")
	}
	if _, e := s.runtime.store.PutObject(ctx, "datamesh-raw", key, bytes.NewReader(raw), int64(len(raw)), minio.PutObjectOptions{ContentType: contentType}); e != nil {
		return problem(503, "ARCHIVE_WRITE_FAILED", "归档块上传失败，未发布新范围")
	}
	check, e := s.archiveObject(ctx, key, int64(len(raw)))
	if e != nil {
		return e
	}
	if !bytes.Equal(check, raw) {
		return problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档对象回读校验失败")
	}
	return nil
}
func archiveManifestDigest(meta ArchiveData) string {
	meta.SHA256 = ""
	digest := sha256.Sum256(encode(meta))
	return hex.EncodeToString(digest[:])
}

func (s *Server) archiveStream(ctx context.Context, task Record, data TaskData, binding CDCBinding, p identity.Principal) error {
	if s.runtime.store == nil {
		return problem(503, "ARCHIVE_UNAVAILABLE", "归档服务未配置")
	}
	proof, e := s.cdcSnapshotBoundary(ctx, task)
	if e != nil {
		return e
	}
	if proof.State != "complete" {
		return problem(409, "CDC_SNAPSHOT_REQUIRED", "首次快照未完成核对，暂不能发布完整原始范围")
	}
	ranges, e := cdcTopicRanges(ctx, binding.Topic)
	if e != nil {
		if captureErrorCode(e) != "CDC_TOPIC_PENDING" || len(proof.Partitions) != 0 {
			return e
		}
		ranges = []CDCPartitionRange{}
	}
	if e = archiveCoversSnapshot(proof, ranges); e != nil {
		return e
	}
	limits, e := loadArchiveLimits()
	if e != nil {
		return e
	}
	var count int64
	for _, r := range ranges {
		if r.Start != 0 {
			return problem(409, "ARCHIVE_RANGE_EXPIRED", "原始保留范围缺失，不能发布不完整归档")
		}
		count += r.End
	}
	if count > limits.Events {
		return problem(409, "ARCHIVE_LIMIT", "归档事件数超过当前配置上限")
	}
	exists, e := s.runtime.store.BucketExists(ctx, "datamesh-raw")
	if e != nil {
		return e
	}
	if !exists {
		if e = s.runtime.store.MakeBucket(ctx, "datamesh-raw", minio.MakeBucketOptions{Region: "us-east-1"}); e != nil {
			return e
		}
	}
	id, e := newID()
	if e != nil {
		return e
	}
	meta := ArchiveData{ManifestVersion: 2, ObjectID: data.ObjectID, Topic: binding.Topic, Start: 0, End: count, Count: int(count), Boundary: CDCSnapshotBoundary{State: "complete", ObjectVersion: data.SchemaVersion, Partitions: ranges}, Chunks: []ArchiveChunk{}, Key: "business/" + data.ObjectID + "/" + id + ".manifest.json"}
	if data.RunID != nil {
		meta.RunID = *data.RunID
	}
	var raw bytes.Buffer
	var chunk ArchiveChunk
	var totalBytes int64
	flush := func() error {
		if chunk.Count == 0 {
			return nil
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, e := writer.Write(raw.Bytes()); e != nil {
			return e
		}
		if e := writer.Close(); e != nil {
			return e
		}
		digest := sha256.Sum256(compressed.Bytes())
		chunk.SHA256 = hex.EncodeToString(digest[:])
		chunk.Key = "business/" + data.ObjectID + "/" + chunk.SHA256 + ".jsonl.gz"
		chunk.Bytes = int64(raw.Len())
		if e := s.putArchiveObject(ctx, data.ObjectID, chunk.Key, compressed.Bytes(), "application/gzip"); e != nil {
			return e
		}
		meta.Chunks = append(meta.Chunks, chunk)
		raw.Reset()
		chunk = ArchiveChunk{}
		return nil
	}
	for _, r := range ranges {
		chunk.Partition = r.Partition
		chunk.Start = 0
		_, e = cdcReadRange(ctx, r, int(limits.Events), func(msg kafka.Message) error {
			event := RawRecord{Topic: binding.Topic, Partition: r.Partition, Offset: msg.Offset, Key: msg.Key, Event: msg.Value}
			encoded := append(encode(event), '\n')
			if int64(len(encoded)) > archiveChunkBytes {
				return problem(409, "ARCHIVE_LIMIT", "单条采集事件超过归档块大小上限")
			}
			if int64(raw.Len()+len(encoded)) > archiveChunkBytes {
				if err := flush(); err != nil {
					return err
				}
				chunk.Partition = r.Partition
				chunk.Start = msg.Offset
			}
			if chunk.Count == 0 {
				chunk.Partition = r.Partition
				chunk.Start = msg.Offset
			}
			if !json.Valid(msg.Key) || !json.Valid(msg.Value) {
				return problem(409, "ARCHIVE_FORMAT_INVALID", "采集事件格式无效，未推进归档范围")
			}
			totalBytes += int64(len(encoded))
			if totalBytes > limits.Bytes {
				return problem(409, "ARCHIVE_LIMIT", "归档字节数超过当前配置上限")
			}
			raw.Write(encoded)
			chunk.Count++
			chunk.End = msg.Offset + 1
			return nil
		})
		if e != nil {
			return e
		}
		if e = flush(); e != nil {
			return e
		}
	}
	if e = validateArchiveManifest(meta); e != nil {
		return e
	}
	meta.SHA256 = archiveManifestDigest(meta)
	if e = s.putArchiveObject(ctx, data.ObjectID, meta.Key, encode(meta), "application/json"); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		currentActor, err := s.etlPrincipal(ctx, p.UserID)
		if err != nil {
			return denied
		}
		if !platform(currentActor, "exchange") || !role(currentActor, "operator") {
			return denied
		}
		current, err := get(tx, "task", task.ID, true)
		if err != nil {
			return err
		}
		if err = requireResourceCapability(tx, currentActor, Resource{Type: "task", ID: task.ID}, "operate_resource"); err != nil {
			return err
		}
		latest, err := decode[TaskData](current.Body)
		if err != nil {
			return err
		}
		object, err := get(tx, "object", data.ObjectID, true)
		if err != nil {
			return err
		}
		if object.Version != data.SchemaVersion || latest.SchemaVersion != data.SchemaVersion {
			return problem(409, "CDC_SCHEMA_CHANGED", "归档期间对象版本已变化")
		}
		if latest.ArchiveID != "" {
			previous, err := get(tx, "archive", latest.ArchiveID, false)
			if err != nil {
				return err
			}
			prior, err := decode[ArchiveData](previous.Body)
			if err != nil {
				return err
			}
			for _, old := range prior.Boundary.Partitions {
				found := false
				for _, next := range ranges {
					if next.Partition == old.Partition && next.End >= old.End {
						found = true
						break
					}
				}
				if !found {
					return problem(409, "STALE_ARCHIVE", "新归档范围不能覆盖已有更新范围")
				}
			}
		}
		record := Record{Kind: "archive", ID: id, OwnerID: current.OwnerID, DepartmentID: current.DepartmentID, ParentID: data.ObjectID, State: "verified", Version: 1, Body: encode(meta)}
		if err = tx.Create(&record).Error; err != nil {
			return err
		}
		keys := []string{meta.Key}
		for _, c := range meta.Chunks {
			keys = append(keys, c.Key)
		}
		marked := tx.Model(&archiveStoredObject{}).Where("key IN ? AND state<>'deleting'", keys).Updates(map[string]interface{}{"state": "referenced", "updated_at": time.Now()})
		if marked.Error != nil {
			return marked.Error
		}
		if marked.RowsAffected != int64(len(keys)) {
			return problem(409, "ARCHIVE_STORE_BUSY", "归档对象状态已变化，未发布新范围")
		}
		latest.ArchiveID = id
		current.Body = encode(latest)
		return save(tx, &current)
	})
}

func archiveCoversSnapshot(proof CDCSnapshotBoundary, ranges []CDCPartitionRange) error {
	if proof.State != "complete" {
		return problem(409, "CDC_SNAPSHOT_REQUIRED", "首次快照尚未完成")
	}
	for _, old := range proof.Partitions {
		found := false
		for _, current := range ranges {
			if current.Topic == old.Topic && current.Partition == old.Partition && current.Start == 0 && current.End >= old.End {
				found = true
				break
			}
		}
		if !found {
			return problem(409, "ARCHIVE_RANGE_EXPIRED", "已验证的初始快照范围缺失，不能发布新归档")
		}
	}
	return nil
}
