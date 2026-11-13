package workflow

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"github.com/segmentio/kafka-go"
	"gorm.io/gorm"
)

type RawRecord struct {
	Topic     string          `json:"topic"`
	Partition int             `json:"partition"`
	Offset    int64           `json:"offset"`
	Key       json.RawMessage `json:"key"`
	Event     json.RawMessage `json:"raw_event"`
}

func topicFor(kind string) string { return "datamesh_" + kind + "." + qualified(kind) }
func (s *Server) archiveAction(c *gin.Context) {
	id := c.Param("id")
	s.mutatePath("archive_task", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if _, e := bind[struct{}](b); e != nil {
			return 0, nil, id, e
		}
		r, e := get(tx, "task", id, true)
		if e != nil {
			return 0, nil, id, e
		}
		if err := requireResourceCapability(tx, p, Resource{Type: r.Kind, ID: r.ID}, "operate_resource"); err != nil {
			return 0, nil, id, err
		}
		return 202, encode(gin.H{"action": "archive"}), id, nil
	})(c)
	if c.Writer.Status() == 202 {
		var op Operation
		if e := s.db.Where("actor_id=? AND idempotency_key=?", principal(c).UserID, c.GetHeader("Idempotency-Key")).Take(&op).Error; e == nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				if s.beginWork(ctx, op.ID) {
					defer func() { <-s.work }()
					e := s.archive(ctx, id, op.ActorID)
					s.finish(context.Background(), op.ID, e)
				}
			}()
		}
	}
}
func (s *Server) archive(ctx context.Context, id string, actorID string) error {
	task, e := get(s.db.WithContext(ctx), "task", id, false)
	if e != nil {
		return e
	}
	v, e := decode[TaskData](task.Body)
	if e != nil {
		return e
	}
	binding, err := resolveCDCBinding(task, v)
	if err != nil {
		return err
	}
	topic := binding.Topic
	if !binding.Legacy {
		p, e := s.etlPrincipal(ctx, actorID)
		if e != nil {
			return e
		}
		return s.archiveStream(ctx, task, v, binding, p)
	}
	first, last, e := s.runtime.end(ctx, topic)
	if e != nil {
		return e
	}
	if first != 0 {
		return problem(409, "ARCHIVE_RANGE_EXPIRED", "原始保留范围已缺失，不能将不完整归档标为完成")
	}
	if last == 0 || last-first > 10000 {
		return problem(409, "ARCHIVE_LIMIT", "当前有界归档要求 1 至 10000 条事件")
	}
	if s.runtime.store == nil {
		return problem(503, "ARCHIVE_UNAVAILABLE", "归档服务未配置")
	}
	reader := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{"kafka:9092"}, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1024 * 1024, MaxWait: time.Second, QueueCapacity: 1, StartOffset: first, OffsetOutOfRangeError: true})
	defer reader.Close()
	if e = reader.SetOffset(first); e != nil {
		return e
	}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	for offset := first; offset < last; offset++ {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			return problem(503, "ARCHIVE_READ_FAILED", "原始范围读取失败，未推进归档水位")
		}
		if msg.Offset != offset || !json.Valid(msg.Value) || !json.Valid(msg.Key) {
			return problem(409, "ARCHIVE_RANGE_INVALID", "原始范围不连续或事件格式无效")
		}
		if e = encoder.Encode(RawRecord{topic, msg.Partition, msg.Offset, msg.Key, msg.Value}); e != nil {
			return e
		}
		if raw.Len() > 16*1024*1024 {
			return problem(409, "ARCHIVE_LIMIT", "样例归档超过 16 MiB 上限")
		}
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, e = writer.Write(raw.Bytes()); e != nil {
		return e
	}
	if e = writer.Close(); e != nil {
		return e
	}
	checksum := sha256.Sum256(compressed.Bytes())
	hash := hex.EncodeToString(checksum[:])
	key := "business/" + v.ObjectID + "/" + hash + ".jsonl.gz"
	bucket := "datamesh-raw"
	exists, e := s.runtime.store.BucketExists(ctx, bucket)
	if e != nil {
		return e
	}
	if !exists {
		if e = s.runtime.store.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); e != nil {
			return e
		}
	}
	if _, e = s.runtime.store.PutObject(ctx, bucket, key, bytes.NewReader(compressed.Bytes()), int64(compressed.Len()), minio.PutObjectOptions{ContentType: "application/gzip"}); e != nil {
		return problem(503, "ARCHIVE_WRITE_FAILED", "原始归档写入失败，未推进水位")
	}
	object, e := s.runtime.store.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if e != nil {
		return e
	}
	stored, e := io.ReadAll(io.LimitReader(object, 20*1024*1024))
	closeErr := object.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if sha256.Sum256(stored) != checksum {
		return problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档读取校验失败")
	}
	runID := ""
	if v.RunID != nil {
		runID = *v.RunID
	}
	meta := ArchiveData{ObjectID: v.ObjectID, Key: key, SHA256: hash, Topic: topic, Start: first, End: last, Count: int(last - first), RunID: runID}
	manifest := encode(meta)
	if _, e = s.runtime.store.PutObject(ctx, bucket, key+".manifest.json", bytes.NewReader(manifest), int64(len(manifest)), minio.PutObjectOptions{ContentType: "application/json"}); e != nil {
		return e
	}
	manifestObject, e := s.runtime.store.GetObject(ctx, bucket, key+".manifest.json", minio.GetObjectOptions{})
	if e != nil {
		return e
	}
	readback, e := io.ReadAll(io.LimitReader(manifestObject, 65536))
	closeErr = manifestObject.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if !bytes.Equal(readback, manifest) {
		return problem(409, "MANIFEST_CHECK_FAILED", "归档清单校验失败")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := get(tx, "task", id, true)
		if err != nil {
			return err
		}
		data, err := decode[TaskData](current.Body)
		if err != nil {
			return err
		}
		if data.ArchiveID != "" {
			prior, err := get(tx, "archive", data.ArchiveID, false)
			if err != nil {
				return err
			}
			prev, err := decode[ArchiveData](prior.Body)
			if err != nil {
				return err
			}
			if prev.End > last {
				return problem(409, "STALE_ARCHIVE", "已有更新归档，不允许旧范围覆盖")
			}
		}
		archiveID, err := newID()
		if err != nil {
			return err
		}
		r := Record{Kind: "archive", ID: archiveID, DepartmentID: current.DepartmentID, OwnerID: current.OwnerID, ParentID: v.ObjectID, State: "verified", Version: 1, Body: manifest}
		if err = tx.Create(&r).Error; err != nil {
			return err
		}
		data.ArchiveID = r.ID
		current.Body = encode(data)
		return save(tx, &current)
	})
}
func (s *Server) rawRecords(ctx context.Context, meta ArchiveData) ([]RawRecord, error) {
	if meta.ManifestVersion == 2 {
		if meta.Count > 10000 {
			return nil, problem(409, "TEMPLATE_LIMIT", "此交付模板最多支持 10000 条事件，请使用批量资源适配器")
		}
		rows := []RawRecord{}
		err := s.streamArchive(ctx, meta, func(row RawRecord) error { rows = append(rows, row); return nil })
		return rows, err
	}
	if s.runtime.store == nil {
		return nil, problem(503, "ARCHIVE_UNAVAILABLE", "归档服务未配置")
	}
	obj, e := s.runtime.store.GetObject(ctx, "datamesh-raw", meta.Key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer obj.Close()
	compressed, e := io.ReadAll(io.LimitReader(obj, 20*1024*1024))
	if e != nil {
		return nil, e
	}
	checksum := sha256.Sum256(compressed)
	if hex.EncodeToString(checksum[:]) != meta.SHA256 {
		return nil, problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档读取校验失败")
	}
	reader, e := gzip.NewReader(bytes.NewReader(compressed))
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	decoder := json.NewDecoder(io.LimitReader(reader, 16*1024*1024+1))
	records := []RawRecord{}
	for decoder.More() {
		var row RawRecord
		if e = decoder.Decode(&row); e != nil {
			return nil, e
		}
		records = append(records, row)
		if len(records) > 10000 {
			return nil, invalid
		}
	}
	if len(records) != meta.Count {
		return nil, problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档事件数不匹配")
	}
	return records, nil
}
func normalizeOp(op string) string {
	switch op {
	case "r":
		return "snapshot"
	case "c":
		return "create"
	case "u":
		return "update"
	default:
		return "delete"
	}
}
func rawProjection(v DataRow, fields []string, sensitive map[string]bool) DataRow {
	if v == nil {
		return nil
	}
	out := DataRow{}
	for _, f := range fields {
		value, ok := v[f]
		if !ok {
			value = json.RawMessage("null")
		}
		if sensitive[f] && string(value) != "null" {
			value = encode("***")
		}
		out[f] = value
	}
	return out
}
func redactSource(v json.RawMessage) json.RawMessage {
	var source map[string]json.RawMessage
	if json.Unmarshal(v, &source) != nil {
		return encode(gin.H{"unavailable": true})
	}
	for k := range source {
		if strings.Contains(strings.ToLower(k), "password") || strings.Contains(strings.ToLower(k), "secret") {
			delete(source, k)
		}
	}
	return encode(source)
}
