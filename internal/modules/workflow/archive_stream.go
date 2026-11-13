package workflow

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"

	"github.com/minio/minio-go/v7"
)

const archiveChunkBytes = int64(4 * 1024 * 1024)

type ArchiveChunk struct {
	Key       string `json:"key"`
	SHA256    string `json:"sha256"`
	Partition int    `json:"partition"`
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	Count     int    `json:"count"`
	Bytes     int64  `json:"bytes"`
}

type archiveLimits struct{ Events, Bytes int64 }

func loadArchiveLimits() (archiveLimits, error) {
	limits := archiveLimits{Events: 10000000, Bytes: 10 * 1024 * 1024 * 1024}
	for _, item := range []struct {
		name  string
		value *int64
		max   int64
	}{{"ARCHIVE_MAX_EVENTS", &limits.Events, 100000000}, {"ARCHIVE_MAX_BYTES", &limits.Bytes, 100 * 1024 * 1024 * 1024}} {
		if raw := os.Getenv(item.name); raw != "" {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n < 1 || n > item.max {
				return limits, problem(503, "ARCHIVE_CONFIG_INVALID", "归档容量配置无效，请管理员核查")
			}
			*item.value = n
		}
	}
	return limits, nil
}

func validateArchiveManifest(meta ArchiveData) error {
	if meta.ManifestVersion != 2 || meta.Boundary.State != "complete" || meta.Boundary.ObjectVersion < 1 || meta.Count < 0 {
		return problem(409, "ARCHIVE_MANIFEST_INVALID", "归档清单未通过完整性检查")
	}
	positions := map[int]int64{}
	ends := map[int]int64{}
	var expected int64
	for _, r := range meta.Boundary.Partitions {
		if r.Topic != meta.Topic || r.Start != 0 || r.End < 0 {
			return problem(409, "ARCHIVE_RANGE_INVALID", "原始范围缺少完整起点")
		}
		if _, exists := ends[r.Partition]; exists {
			return problem(409, "ARCHIVE_RANGE_INVALID", "分区范围重复")
		}
		positions[r.Partition] = 0
		ends[r.Partition] = r.End
		expected += r.End
	}
	var count int64
	for _, chunk := range meta.Chunks {
		end, exists := ends[chunk.Partition]
		if !exists || chunk.Start != positions[chunk.Partition] || chunk.End <= chunk.Start || chunk.End > end || int64(chunk.Count) != chunk.End-chunk.Start {
			return problem(409, "ARCHIVE_RANGE_INVALID", "归档块位点缺失、重复或越界")
		}
		positions[chunk.Partition] = chunk.End
		count += int64(chunk.Count)
	}
	if count != int64(meta.Count) || expected != count {
		return problem(409, "ARCHIVE_COUNT_INVALID", "归档事件数量与分区范围不一致")
	}
	for part, end := range ends {
		if positions[part] != end {
			return problem(409, "ARCHIVE_RANGE_INVALID", "归档没有覆盖完整范围")
		}
	}
	return nil
}

func decodeArchiveChunk(compressed []byte, topic string, chunk ArchiveChunk, maxBytes int64, consume func(RawRecord) error) error {
	checksum := sha256.Sum256(compressed)
	if hex.EncodeToString(checksum[:]) != chunk.SHA256 {
		return problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档块摘要校验失败")
	}
	if chunk.Bytes < 0 || chunk.Bytes > maxBytes || chunk.Count < 0 || chunk.End-chunk.Start != int64(chunk.Count) {
		return problem(409, "ARCHIVE_RANGE_INVALID", "归档块大小或范围无效")
	}
	reader, e := gzip.NewReader(bytes.NewReader(compressed))
	if e != nil {
		return problem(409, "ARCHIVE_FORMAT_INVALID", "归档压缩格式无效")
	}
	defer reader.Close()
	raw, e := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if e != nil || int64(len(raw)) > maxBytes {
		return problem(409, "ARCHIVE_LIMIT", "归档解压内容超过允许大小或无法读取")
	}
	if chunk.Bytes != 0 && chunk.Bytes != int64(len(raw)) {
		return problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档块解压字节数不一致")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	count := 0
	for {
		var event RawRecord
		e = decoder.Decode(&event)
		if e == io.EOF {
			break
		}
		if e != nil {
			return problem(409, "ARCHIVE_FORMAT_INVALID", "归档事件格式无效")
		}
		if event.Topic != topic || event.Partition != chunk.Partition || event.Offset != chunk.Start+int64(count) || !json.Valid(event.Key) || !json.Valid(event.Event) {
			return problem(409, "ARCHIVE_RANGE_INVALID", "归档事件位点或来源不一致")
		}
		var key DataRow
		if json.Unmarshal(event.Key, &key) != nil || len(key) == 0 {
			return problem(409, "ARCHIVE_KEY_REQUIRED", "归档事件缺少可核对的主键")
		}
		count++
		if count > chunk.Count {
			return problem(409, "ARCHIVE_COUNT_INVALID", "归档块事件数超出清单")
		}
		if e = consume(event); e != nil {
			return e
		}
	}
	if count != chunk.Count {
		return problem(409, "ARCHIVE_COUNT_INVALID", "归档块事件数与清单不一致")
	}
	return nil
}

func (s *Server) archiveObject(ctx context.Context, key string, limit int64) ([]byte, error) {
	if s.runtime.store == nil {
		return nil, problem(503, "ARCHIVE_UNAVAILABLE", "归档存储未配置")
	}
	object, e := s.runtime.store.GetObject(ctx, "datamesh-raw", key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer object.Close()
	raw, e := io.ReadAll(io.LimitReader(object, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) > limit {
		return nil, problem(409, "ARCHIVE_LIMIT", "归档对象超过允许大小")
	}
	return raw, nil
}

func (s *Server) streamArchive(ctx context.Context, meta ArchiveData, consume func(RawRecord) error) error {
	if meta.ManifestVersion < 2 {
		rows, e := s.rawRecords(ctx, meta)
		if e != nil {
			return e
		}
		for _, row := range rows {
			if e = consume(row); e != nil {
				return e
			}
		}
		return nil
	}
	if e := validateArchiveManifest(meta); e != nil {
		return e
	}
	if e := s.verifyArchiveManifest(ctx, meta); e != nil {
		return e
	}
	limits, e := loadArchiveLimits()
	if e != nil {
		return e
	}
	if int64(meta.Count) > limits.Events {
		return problem(409, "ARCHIVE_LIMIT", "归档事件数超过当前配置上限")
	}
	var total int64
	for _, chunk := range meta.Chunks {
		if chunk.Bytes <= 0 || chunk.Bytes > archiveChunkBytes {
			return problem(409, "ARCHIVE_LIMIT", "归档块大小无效")
		}
		total += chunk.Bytes
		if total > limits.Bytes {
			return problem(409, "ARCHIVE_LIMIT", "归档内容超过当前配置上限")
		}
		compressed, e := s.archiveObject(ctx, chunk.Key, archiveChunkBytes+65536)
		if e != nil {
			return e
		}
		if e = decodeArchiveChunk(compressed, meta.Topic, chunk, archiveChunkBytes, consume); e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) verifyArchiveManifest(ctx context.Context, meta ArchiveData) error {
	if e := validateArchiveManifest(meta); e != nil {
		return e
	}
	if meta.SHA256 != archiveManifestDigest(meta) {
		return problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档清单摘要不一致")
	}
	manifest, e := s.archiveObject(ctx, meta.Key, 16*1024*1024)
	if e != nil {
		return e
	}
	var stored ArchiveData
	if json.Unmarshal(manifest, &stored) != nil || stored.SHA256 != meta.SHA256 || archiveManifestDigest(stored) != meta.SHA256 {
		return problem(409, "ARCHIVE_CHECKSUM_FAILED", "归档清单回读校验失败")
	}
	return nil
}

func (s *Server) archivePage(ctx context.Context, meta ArchiveData, start, limit int) ([]RawRecord, error) {
	if start < 0 || start > meta.Count || limit < 1 || limit > 100 {
		return nil, invalid
	}
	if meta.ManifestVersion < 2 {
		rows, e := s.rawRecords(ctx, meta)
		if e != nil {
			return nil, e
		}
		return rows[start:min(start+limit, len(rows))], nil
	}
	if e := s.verifyArchiveManifest(ctx, meta); e != nil {
		return nil, e
	}
	out := []RawRecord{}
	pageBytes := 0
	base := 0
	end := min(start+limit, meta.Count)
	for _, chunk := range meta.Chunks {
		next := base + chunk.Count
		if base < end && next > start {
			compressed, e := s.archiveObject(ctx, chunk.Key, archiveChunkBytes+65536)
			if e != nil {
				return nil, e
			}
			i := base
			if e = decodeArchiveChunk(compressed, meta.Topic, chunk, archiveChunkBytes, func(row RawRecord) error {
				if i >= start && i < end {
					pageBytes += len(row.Key) + len(row.Event)
					if pageBytes > 20*1024*1024 {
						return problem(409, "DELIVERY_LIMIT", "分页内容超过20 MiB，请减小页大小")
					}
					out = append(out, row)
				}
				i++
				return nil
			}); e != nil {
				return nil, e
			}
		}
		base = next
		if base >= end {
			break
		}
	}
	if len(out) != end-start {
		return nil, problem(409, "ARCHIVE_COUNT_INVALID", "分页事件数与归档清单不一致")
	}
	return out, nil
}
