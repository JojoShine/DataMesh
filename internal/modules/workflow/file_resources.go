package workflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"datamesh/internal/modules/identity"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const resourceBucket = "datamesh-resources"

var fileWork = make(chan struct{}, 1)

func fileCapacity(c *gin.Context) bool {
	controller := http.NewResponseController(c.Writer)
	for _, err := range []error{controller.SetReadDeadline(time.Now().Add(120 * time.Second)), controller.SetWriteDeadline(time.Now().Add(120 * time.Second))} {
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			c.JSON(503, gin.H{"code": "FILE_TIMEOUT_CONFIG", "message": "文件传输暂不可用"})
			return false
		}
	}

	select {
	case fileWork <- struct{}{}:
		return true
	default:
		c.Header("Retry-After", "5")
		c.JSON(429, gin.H{"code": "FILE_BUSY", "message": "正在处理其他文件，请稍后重试"})
		return false
	}
}

type FileConfig struct {
	Encryption      string               `json:"encryption,omitempty"`
	CipherSize      int64                `json:"cipher_size,omitempty"`
	CipherSHA256    string               `json:"cipher_sha256,omitempty"`
	GovernanceInput *GovernanceFileInput `json:"governance_input,omitempty"`
	Name            string               `json:"name"`
	MIME            string               `json:"mime"`
	Size            int64                `json:"size"`
	SHA256          string               `json:"sha256"`
	Key             string               `json:"key"`
}
type UploadData struct {
	File              FileConfig `json:"file"`
	DirectoryRevision string     `json:"directory_revision"`
	Sheets            []string   `json:"sheets,omitempty"`
	ResourceID        string     `json:"resource_id,omitempty"`
	RequestHash       string     `json:"request_hash,omitempty"`
}

func validateOffice(data []byte, ext string) error {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return invalid
	}
	var total uint64
	expected := map[string]string{".xlsx": "xl/workbook.xml", ".docx": "word/document.xml", ".pptx": "ppt/presentation.xml"}[ext]
	found := false
	if len(z.File) > 10000 {
		return problem(400, "ARCHIVE_LIMIT", "文件内部项目过多")
	}
	for _, f := range z.File {
		if f.UncompressedSize64 > (64<<20)-total {
			return problem(400, "ARCHIVE_LIMIT", "文件解压内容超过 64 MiB")
		}
		total += f.UncompressedSize64
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "vbaproject") || strings.Contains(name, "externallink") || strings.Contains(name, "embeddings/") {
			return problem(400, "ACTIVE_CONTENT_DENIED", "不支持宏、外部链接或嵌入对象")
		}
		if strings.HasSuffix(name, ".rels") {
			stream, err := f.Open()
			if err != nil {
				return invalid
			}
			raw, err := io.ReadAll(io.LimitReader(stream, 1<<20+1))
			stream.Close()
			if err != nil || len(raw) > 1<<20 {
				return invalid
			}
			var rels struct {
				Items []struct {
					Mode string `xml:"TargetMode,attr"`
				} `xml:"Relationship"`
			}
			if xml.Unmarshal(raw, &rels) != nil {
				return invalid
			}
			for _, rel := range rels.Items {
				if strings.EqualFold(rel.Mode, "External") {
					return problem(400, "ACTIVE_CONTENT_DENIED", "不支持包含外部链接的文件")
				}
			}
		}
		if f.Name == expected {
			found = true
		}
	}
	if !found {
		return problem(400, "FILE_FORMAT_INVALID", "文件内容与扩展名不符")
	}
	return nil
}
func validateSharedFile(name string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if len(data) == 0 {
		return "", invalid
	}
	switch ext {
	case ".xlsx", ".docx", ".pptx":
		if e := validateOffice(data, ext); e != nil {
			return "", e
		}
		return map[string]string{".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation"}[ext], nil
	case ".pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return "", invalid
		}
		if bytes.Contains(data, []byte("/JavaScript")) || bytes.Contains(data, []byte("/Launch")) {
			return "", problem(400, "ACTIVE_CONTENT_DENIED", "不支持包含活动脚本的 PDF")
		}
		return "application/pdf", nil
	case ".txt", ".csv":
		if strings.Contains(http.DetectContentType(data), "text/plain") {
			return "text/plain; charset=utf-8", nil
		}
	}
	return "", problem(400, "FILE_TYPE_DENIED", "支持 PDF、TXT、CSV、DOCX、XLSX、PPTX，文件内容须符合类型")
}
func (s *Server) storeFile(ctx context.Context, name, typ string, data []byte) (FileConfig, error) {
	v := FileConfig{Name: filepath.Base(name), MIME: typ, Size: int64(len(data))}
	if s.runtime.store == nil {
		return v, problem(503, "STORE_UNAVAILABLE", "文件存储未配置")
	}
	id, e := newID()
	if e != nil {
		return v, e
	}
	return s.storeFileAt(ctx, name, typ, data, "resources/"+id)
}
func (s *Server) storeFileAt(ctx context.Context, name, typ string, data []byte, key string) (FileConfig, error) {
	v := FileConfig{Name: filepath.Base(name), MIME: typ, Size: int64(len(data)), Key: key}
	if s.runtime.store == nil {
		return v, problem(503, "STORE_UNAVAILABLE", "文件存储未配置")
	}
	sum := sha256.Sum256(data)
	v.SHA256 = hex.EncodeToString(sum[:])
	exists, e := s.runtime.store.BucketExists(ctx, resourceBucket)
	if e != nil {
		return v, e
	}
	if !exists {
		if e = s.runtime.store.MakeBucket(ctx, resourceBucket, minio.MakeBucketOptions{}); e != nil {
			return v, e
		}
	}
	_, e = s.runtime.store.PutObject(ctx, resourceBucket, v.Key, bytes.NewReader(data), v.Size, minio.PutObjectOptions{ContentType: typ})
	return v, e
}
func (s *Server) readStoredFile(ctx context.Context, v FileConfig, max int64) ([]byte, error) {
	if s.runtime.store == nil {
		return nil, problem(503, "STORE_UNAVAILABLE", "文件存储未配置")
	}
	if v.Encryption != "" {
		return s.readPrivateFile(ctx, v, max)
	}
	if v.Size < 1 || v.Size > max {
		return nil, invalid
	}
	obj, e := s.runtime.store.GetObject(ctx, resourceBucket, v.Key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	defer obj.Close()
	data, e := io.ReadAll(io.LimitReader(obj, max+1))
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != v.Size || hex.EncodeToString(sum[:]) != v.SHA256 {
		return nil, problem(409, "FILE_INTEGRITY_FAILED", "文件大小或摘要校验失败")
	}
	return data, nil
}
func (s *Server) uploadResource(excel bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !fileCapacity(c) {
			return
		}
		defer func() { <-fileWork }()
		if e := s.cleanupUploads(c.Request.Context()); e != nil {
			s.fail(c, e)
			return
		}
		max := int64(50 << 20)
		kind := "resource_upload"
		if excel {
			max = 20 << 20
			kind = "resource_import"
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max+(1<<20))
		if e := c.Request.ParseMultipartForm(1 << 20); e != nil {
			s.fail(c, problem(400, "UPLOAD_LIMIT", "文件超过限制或上传未完成"))
			return
		}
		defer c.Request.MultipartForm.RemoveAll()
		p := principal(c)
		tx := s.db.WithContext(c.Request.Context())
		dir, def, rev, e := directoryForResource(tx, p, c.PostForm("directory_id"))
		if e != nil {
			s.fail(c, e)
			return
		}
		if (excel && (def.Format != "database" || len(def.Items) == 0)) || (!excel && def.Format != "file") {
			s.fail(c, problem(400, "DIRECTORY_FORMAT_MISMATCH", "请先维护与挂载类型匹配的目录和数据项"))
			return
		}
		header, e := c.FormFile("file")
		if e != nil || header.Size > max {
			s.fail(c, invalid)
			return
		}
		f, e := header.Open()
		if e != nil {
			s.fail(c, e)
			return
		}
		data, e := io.ReadAll(io.LimitReader(f, max+1))
		f.Close()
		if e != nil || int64(len(data)) > max {
			s.fail(c, invalid)
			return
		}
		typ, e := validateSharedFile(header.Filename, data)
		if e != nil {
			s.fail(c, e)
			return
		}
		sheets := []string{}
		if excel {
			if strings.ToLower(filepath.Ext(header.Filename)) != ".xlsx" {
				s.fail(c, problem(400, "XLSX_REQUIRED", "请上传 .xlsx 格式"))
				return
			}
			sheets, e = excelSheets(data)
			if e != nil {
				s.fail(c, e)
				return
			}
		}
		meta, e := s.storeFile(c.Request.Context(), header.Filename, typ, data)
		if e != nil {
			s.fail(c, e)
			return
		}
		r, e := newRecord(kind, dir.DepartmentID, p.UserID, dir.ID, "uploaded", UploadData{File: meta, DirectoryRevision: rev, Sheets: sheets})
		if e == nil {
			e = tx.Transaction(func(t *gorm.DB) error {
				if err := requireResourceCapability(t, p, Resource{Type: "directory", ID: dir.ID}, "operate_resource"); err != nil {
					return err
				}
				if err := t.Create(&r).Error; err != nil {
					return err
				}
				return s.audit(t, p, "upload_resource", r.ID, "success", c.GetString("trace_id"))
			})
		}
		if e != nil {
			if cleanupErr := s.runtime.store.RemoveObject(c.Request.Context(), resourceBucket, meta.Key, minio.RemoveObjectOptions{}); cleanupErr != nil {
				s.fail(c, problem(503, "UPLOAD_CLEANUP_FAILED", "上传确认失败，暂存文件待清理"))
				return
			}
			s.fail(c, e)
			return
		}
		c.JSON(201, gin.H{"batch_id": r.ID, "upload_id": r.ID, "sheets": sheets, "name": meta.Name, "size": meta.Size, "directory_version": dir.Version})
	}
}
func (s *Server) registerFileResources(api, write *gin.RouterGroup) {
	write.POST("/file-resources/uploads", s.require("exchange"), s.uploadResource(false))
	write.POST("/file-resources", s.require("exchange"), s.mutate("register_file_resource", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		v, e := bind[struct {
			UploadID string `json:"upload_id"`
		}](b)
		if e != nil {
			return 0, nil, "", e
		}
		up, e := get(tx, "resource_upload", v.UploadID, true)
		if e != nil {
			return 0, nil, "", e
		}
		if up.OwnerID != p.UserID && !admin(p) {
			return 0, nil, "", denied
		}
		u, e := decode[UploadData](up.Body)
		if e != nil {
			return 0, nil, "", e
		}
		dir, def, rev, e := directoryForResource(tx, p, up.ParentID)
		if e != nil {
			return 0, nil, "", e
		}
		if rev != u.DirectoryRevision || def.Format != "file" {
			return 0, nil, "", conflict
		}
		if up.State == "ready" {
			return 200, encode(gin.H{"resource": Resource{Type: "file_resource", ID: u.ResourceID, Version: 1}}), u.ResourceID, nil
		}
		if up.State != "uploaded" || time.Since(up.CreatedAt) > 24*time.Hour {
			return 0, nil, "", conflict
		}
		resource, e := newRecord("file_resource", dir.DepartmentID, p.UserID, dir.ID, "ready", ManagedResource{Name: u.File.Name, DataLayer: def.DataLayer, Fields: []Field{}, Adapter: "file", Config: encode(u.File)})
		if e != nil {
			return 0, nil, "", e
		}
		if e = tx.Create(&resource).Error; e != nil {
			return 0, nil, "", e
		}
		up.State = "ready"
		u.ResourceID = resource.ID
		up.Body = encode(u)
		if e = save(tx, &up); e != nil {
			return 0, nil, "", e
		}
		return 201, encode(gin.H{"resource": Resource{Type: resource.Kind, ID: resource.ID, Version: 1}}), resource.ID, nil
	}))
}
func (s *Server) deliverFile(c *gin.Context) {
	if !fileCapacity(c) {
		return
	}
	defer func() { <-fileWork }()
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	version, e := strconv.Atoi(c.Query("contract_version"))
	if e != nil || version < 1 {
		s.fail(c, invalid)
		return
	}
	ref := Resource{Type: "file_resource", ID: c.Param("id"), Version: version}
	check := func() error {
		return tx.Transaction(func(t *gorm.DB) error {
			_, _, _, _, err := s.authorizeDelivery(t, p, ref, DeliveryDownload, nil)
			return err
		})
	}
	if e = check(); e != nil {
		s.fail(c, e)
		return
	}
	_, v, e := managedResource(tx, ref, false)
	if e != nil {
		s.fail(c, e)
		return
	}
	f, e := decode[FileConfig](v.Config)
	if e != nil {
		s.fail(c, e)
		return
	}
	data, e := s.readStoredFile(c.Request.Context(), f, 50<<20)
	if e != nil {
		s.fail(c, e)
		return
	}
	if e = check(); e != nil {
		s.fail(c, e)
		return
	}
	if e = s.securitySendFile(c, p, ref, data, f); e != nil && !c.Writer.Written() {
		s.fail(c, e)
	}
}

// Expired, unconfirmed uploads are reclaimed on the next upload; confirmed versions are retained.
func (s *Server) cleanupUploads(ctx context.Context) error {
	if s.runtime.store == nil {
		return nil
	}
	var rows []Record
	tx := s.db.WithContext(ctx)
	if e := tx.Where("kind IN ? AND state IN ? AND created_at < ?", []string{"resource_upload", "resource_import"}, []string{"uploaded", "expired"}, time.Now().Add(-24*time.Hour)).Order("created_at").Limit(10).Find(&rows).Error; e != nil {
		return e
	}
	for _, row := range rows {
		var file FileConfig
		e := tx.Transaction(func(t *gorm.DB) error {
			r, err := get(t, row.Kind, row.ID, true)
			if err != nil {
				return err
			}
			u, err := decode[UploadData](r.Body)
			if err != nil {
				return err
			}
			if (r.State != "uploaded" && r.State != "expired") || u.ResourceID != "" {
				return nil
			}
			r.State = "expired"
			file = u.File
			return save(t, &r)
		})
		if e != nil {
			return e
		}
		if file.Key == "" {
			continue
		}
		if e = s.runtime.store.RemoveObject(ctx, resourceBucket, file.Key, minio.RemoveObjectOptions{}); e != nil {
			return e
		}
		if e = tx.Model(&Record{}).Where("kind=? AND id=? AND state='expired'", row.Kind, row.ID).Update("state", "cleaned").Error; e != nil {
			return e
		}
	}
	return nil
}
