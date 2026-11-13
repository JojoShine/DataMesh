package workflow

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GovernanceExport struct {
	ID             string          `json:"id" gorm:"primaryKey"`
	RunID          string          `json:"run_id"`
	ActorID        string          `json:"actor_id"`
	State          string          `json:"state"`
	Manifest       json.RawMessage `json:"manifest" gorm:"type:jsonb"`
	ErrorCode      string          `json:"error_code,omitempty"`
	LeaseOwner     string          `json:"-"`
	LeaseExpiresAt time.Time       `json:"-"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func (GovernanceExport) TableName() string { return "governance_exports" }

type governanceExportChunk struct {
	Number int    `json:"number"`
	Rows   int    `json:"rows"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type governanceExportManifest struct {
	RunID     string                  `json:"run_id"`
	DataLayer string                  `json:"data_layer"`
	Rows      int64                   `json:"rows"`
	SHA256    string                  `json:"sha256"`
	Format    string                  `json:"format"`
	Masked    bool                    `json:"masked"`
	CreatedAt time.Time               `json:"created_at"`
	Chunks    []governanceExportChunk `json:"chunks"`
}

func exportObjectKey(id string, n int) string {
	return fmt.Sprintf("governance_exports/%s/%06d.gz.enc", id, n)
}
func (s *Server) exportBasis(tx *gorm.DB, p identity.Principal, runID string) (GovernanceRun, governanceFlowExecution, governanceFlowReport, []Field, error) {
	ctx := tx.Statement.Context
	run, e := governanceRunRead(tx, p, runID, false)
	if e != nil {
		return run, governanceFlowExecution{}, governanceFlowReport{}, nil, e
	}
	def, e := decode[governanceFlowExecution](run.Definition)
	if e != nil {
		return run, def, governanceFlowReport{}, nil, e
	}
	report, e := decode[governanceFlowReport](run.Result)
	if e != nil {
		return run, def, report, nil, e
	}
	if run.State != "succeeded" || run.Kind != "dataflow" || report.Storage == nil {
		return run, def, report, nil, problem(409, "DORIS_RESULT_REQUIRED", "请先完成一次 Doris 治理运行，再生成存档快照")
	}
	actor, e := s.etlPrincipal(ctx, run.ActorID)
	if e != nil {
		return run, def, report, nil, e
	}
	if e = s.governanceFlowAuthorize(ctx, actor, def); e != nil {
		return run, def, report, nil, e
	}
	var purged int64
	if e = tx.Table("governance_snapshot_purges").Where("run_id=?", run.ID).Count(&purged).Error; e != nil {
		return run, def, report, nil, e
	}
	if purged > 0 {
		return run, def, report, nil, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "成果已清理")
	}
	_, plan, _, e := governanceExecutionPlan(def)
	fields := []Field{}
	for _, f := range plan.Outputs[GovernanceFlowEdge{Node: report.OutputNode}] {
		fields = append(fields, f.Field)
	}
	return run, def, report, fields, e
}
func (s *Server) registerGovernanceExports(api, write *gin.RouterGroup) {
	write.POST("/governance/runs/:id/archive", s.require("governance", "governor"), s.mutatePath("governance_archive_result", func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
		if _, e := bind[struct{}](b); e != nil {
			return 0, nil, id, e
		}
		if _, _, _, _, e := s.exportBasis(tx, p, id); e != nil {
			return 0, nil, id, e
		}
		var row GovernanceExport
		e := tx.Where("run_id=? AND actor_id=?", id, p.UserID).Take(&row).Error
		if e == nil {
			if row.State == "failed" {
				row.State = "queued"
				row.ErrorCode = ""
				row.Manifest = encode(map[string]interface{}{})
				if e = tx.Save(&row).Error; e != nil {
					return 0, nil, row.ID, e
				}
			}
			return 200, encode(row), row.ID, nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return 0, nil, id, e
		}
		exportID, e := newID()
		if e != nil {
			return 0, nil, id, e
		}
		row = GovernanceExport{ID: exportID, RunID: id, ActorID: p.UserID, State: "queued", Manifest: encode(map[string]interface{}{}), LeaseExpiresAt: time.Unix(0, 0).UTC()}
		if e = tx.Create(&row).Error; e != nil {
			return 0, nil, id, e
		}
		return 201, encode(row), row.ID, nil
	}))
	api.GET("/governance/exports/:id", s.require("governance", "governor"), func(c *gin.Context) {
		row, e := s.exportRead(c)
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, row)
	})
	api.GET("/governance/exports/:id/download", s.require("governance", "governor"), s.exportDownload)
}
func (s *Server) exportRead(c *gin.Context) (GovernanceExport, error) {
	var row GovernanceExport
	e := s.db.WithContext(c.Request.Context()).Where("id=? AND actor_id=?", c.Param("id"), principal(c).UserID).Take(&row).Error
	if e != nil {
		return row, e
	}
	_, _, _, _, e = s.exportBasis(s.db.WithContext(c.Request.Context()), principal(c), row.RunID)
	return row, e
}
func (s *Server) exportTick(parent context.Context) error {
	var row GovernanceExport
	token, e := newID()
	if e != nil {
		return e
	}
	e = s.db.WithContext(parent).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='queued' OR (state='running' AND lease_expires_at<CURRENT_TIMESTAMP)").Order("created_at").Take(&row).Error; e != nil {
			return e
		}
		row.State = "running"
		row.LeaseOwner = token
		row.LeaseExpiresAt = time.Now().Add(12 * time.Minute)
		return tx.Save(&row).Error
	})
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	manifest, e := s.buildGovernanceExport(ctx, row)
	state := "ready"
	code := ""
	if e != nil {
		state = "failed"
		code = "RESULT_ARCHIVE_FAILED"
		var p *Problem
		if errors.As(e, &p) {
			code = p.Code
		}
	}
	saved := s.db.WithContext(parent).Model(&GovernanceExport{}).Where("id=? AND lease_owner=?", row.ID, token).Updates(map[string]interface{}{"state": state, "manifest": encode(manifest), "error_code": code, "updated_at": time.Now(), "lease_owner": "", "lease_expires_at": time.Unix(0, 0).UTC()})
	if saved.Error != nil {
		return saved.Error
	}
	return e
}
func (s *Server) buildGovernanceExport(ctx context.Context, job GovernanceExport) (governanceExportManifest, error) {
	manifest := governanceExportManifest{RunID: job.RunID, Format: "jsonl.gz", CreatedAt: job.CreatedAt, Chunks: []governanceExportChunk{}}
	p, e := s.etlPrincipal(ctx, job.ActorID)
	if e != nil {
		return manifest, e
	}
	_, _, report, fields, e := s.exportBasis(s.db.WithContext(ctx), p, job.RunID)
	if e != nil {
		return manifest, e
	}
	manifest.DataLayer = report.DataLayer
	if s.runtime.store == nil {
		return manifest, problem(503, "ARCHIVE_UNAVAILABLE", "RustFS 尚未配置")
	}
	hash := sha256.New()
	var totalBytes int64
	for offset := 0; int64(offset) < report.OutputRows || offset == 0; offset += 250 {
		if _, _, _, _, e = s.exportBasis(s.db.WithContext(ctx), p, job.RunID); e != nil {
			return manifest, e
		}
		rows, e := dorisResultPage(ctx, report.Storage, job.RunID, fields, offset, 250)
		if e != nil {
			return manifest, e
		}
		if len(rows) != min(250, int(report.OutputRows)-offset) {
			return manifest, problem(409, "GOVERNANCE_OUTPUT_INVALID", "成果行数变化，未发布快照")
		}
		var raw bytes.Buffer
		for _, row := range rows {
			for _, f := range fields {
				if f.Sensitive && !admin(p) && string(row[f.Name]) != "null" {
					row[f.Name] = encode("***")
					manifest.Masked = true
				}
			}
			line := append(encode(row), '\n')
			raw.Write(line)
			hash.Write(line)
		}
		totalBytes += int64(raw.Len())
		if totalBytes > s.etl.MaxBytes || raw.Len() > 16*1024*1024 {
			return manifest, problem(409, "GOVERNANCE_LIMIT", "成果快照超过存档批次限制")
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, e = writer.Write(raw.Bytes()); e != nil {
			return manifest, e
		}
		if e = writer.Close(); e != nil {
			return manifest, e
		}
		n := len(manifest.Chunks)
		key := exportObjectKey(job.ID, n)
		cipher, e := s.runtime.cryptScope(compressed.Bytes(), true, key)
		if e != nil {
			return manifest, e
		}
		if _, e = s.runtime.store.PutObject(ctx, "datamesh-raw", key, bytes.NewReader(cipher), int64(len(cipher)), minio.PutObjectOptions{ContentType: "application/octet-stream"}); e != nil {
			return manifest, e
		}
		object, e := s.runtime.store.GetObject(ctx, "datamesh-raw", key, minio.GetObjectOptions{})
		if e != nil {
			return manifest, e
		}
		check, e := io.ReadAll(io.LimitReader(object, int64(len(cipher)+1)))
		closeErr := object.Close()
		if e != nil {
			return manifest, e
		}
		if closeErr != nil {
			return manifest, closeErr
		}
		if !bytes.Equal(check, cipher) {
			return manifest, problem(409, "ARCHIVE_CHECKSUM_FAILED", "快照回读校验失败")
		}
		digest := sha256.Sum256(cipher)
		manifest.Chunks = append(manifest.Chunks, governanceExportChunk{Number: n, Rows: len(rows), Bytes: len(cipher), SHA256: hex.EncodeToString(digest[:])})
		manifest.Rows += int64(len(rows))
	}
	if _, _, _, _, e = s.exportBasis(s.db.WithContext(ctx), p, job.RunID); e != nil {
		return manifest, e
	}
	manifest.SHA256 = hex.EncodeToString(hash.Sum(nil))
	key := "governance_exports/" + job.ID + "/manifest.json.enc"
	cipher, e := s.runtime.cryptScope(encode(manifest), true, key)
	if e != nil {
		return manifest, e
	}
	if _, e = s.runtime.store.PutObject(ctx, "datamesh-raw", key, bytes.NewReader(cipher), int64(len(cipher)), minio.PutObjectOptions{ContentType: "application/octet-stream"}); e != nil {
		return manifest, e
	}
	object, e := s.runtime.store.GetObject(ctx, "datamesh-raw", key, minio.GetObjectOptions{})
	if e != nil {
		return manifest, e
	}
	check, readErr := io.ReadAll(io.LimitReader(object, int64(len(cipher)+1)))
	closeErr := object.Close()
	if readErr != nil {
		return manifest, readErr
	}
	if closeErr != nil {
		return manifest, closeErr
	}
	if !bytes.Equal(check, cipher) {
		return manifest, problem(409, "ARCHIVE_CHECKSUM_FAILED", "快照清单回读校验失败")
	}
	return manifest, nil
}
func (s *Server) exportDownload(c *gin.Context) {
	job, e := s.exportRead(c)
	if e != nil {
		s.fail(c, e)
		return
	}
	if job.State != "ready" {
		s.fail(c, conflict)
		return
	}
	manifest, e := decode[governanceExportManifest](job.Manifest)
	if e != nil {
		s.fail(c, e)
		return
	}
	// Verify the complete archive before sending a successful download response.
	file, e := os.CreateTemp("", "datamesh-export-*.gz")
	if e != nil {
		s.fail(c, e)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	var written int64
	for _, chunk := range manifest.Chunks {
		if _, e = s.exportRead(c); e != nil {
			s.fail(c, e)
			return
		}
		key := exportObjectKey(job.ID, chunk.Number)
		object, e := s.runtime.store.GetObject(c.Request.Context(), "datamesh-raw", key, minio.GetObjectOptions{})
		if e != nil {
			s.fail(c, e)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(object, int64(chunk.Bytes+1)))
		closeErr := object.Close()
		if e != nil {
			s.fail(c, e)
			return
		}
		if closeErr != nil {
			s.fail(c, closeErr)
			return
		}
		sum := sha256.Sum256(raw)
		if len(raw) != chunk.Bytes || hex.EncodeToString(sum[:]) != chunk.SHA256 {
			s.fail(c, problem(409, "ARCHIVE_CHECKSUM_FAILED", "存档完整性校验失败"))
			return
		}
		plain, e := s.runtime.cryptScope(raw, false, key)
		if e != nil {
			s.fail(c, e)
			return
		}
		written += int64(len(plain))
		if written > s.etl.MaxBytes+int64(len(manifest.Chunks))*1024 {
			s.fail(c, problem(409, "GOVERNANCE_LIMIT", "存档超过下载限制"))
			return
		}
		if _, e = file.Write(plain); e != nil {
			s.fail(c, e)
			return
		}
	}
	if _, e = s.exportRead(c); e != nil {
		s.fail(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "application/gzip")
	c.FileAttachment(file.Name(), fmt.Sprintf("governance-%s.jsonl.gz", job.ID))
}
