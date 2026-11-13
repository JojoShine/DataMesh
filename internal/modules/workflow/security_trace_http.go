package workflow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

var securityTraceInspections = make(chan struct{}, 1)

// Multipart spill files are removed after inspection; nothing enters object storage.
func (s *Server) securityExtractTrace(c *gin.Context) {
	select {
	case securityTraceInspections <- struct{}{}:
		defer func() { <-securityTraceInspections }()
	default:
		c.Header("Retry-After", "2")
		s.fail(c, problem(429, "SECURITY_INSPECTION_BUSY", "已有文件正在核验，请稍后重试"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65<<20)
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		s.fail(c, problem(413, "SECURITY_FILE_LIMIT", "文件最多支持64MiB"))
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		s.fail(c, invalid)
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
	if err != nil {
		s.fail(c, err)
		return
	}
	if len(raw) > 64<<20 {
		s.fail(c, problem(413, "SECURITY_FILE_LIMIT", "文件最多支持64MiB"))
		return
	}
	var token string
	switch strings.ToLower(filepath.Ext(header.Filename)) {
	case ".xlsx":
		token, err = securityExtractExcelTrace(raw)
	case ".pdf":
		token, err = securityExtractPDFMarker(raw)
	default:
		err = problem(400, "SECURITY_FILE_FORMAT_UNSUPPORTED", "请选择PDF或Excel文件")
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	id, err := securityTraceID(token)
	if err != nil {
		s.fail(c, invalid)
		return
	}
	p := principal(c)
	tx := s.db.WithContext(c.Request.Context())
	proof, err := securityProofAccess(tx, p, id)
	if err != nil {
		s.fail(c, err)
		return
	}
	_, key, err := securitySigningKey(proof.KeyID)
	if err != nil {
		s.fail(c, err)
		return
	}
	tokenValid := hmac.Equal([]byte(token), []byte(securityTraceToken(proof, key)))
	signatureValid := proof.Signature != "" && hmac.Equal([]byte(proof.Signature), []byte(securityProofSignature(proof, key)))
	sum := sha256.Sum256(raw)
	matched := tokenValid && signatureValid && hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(proof.SHA256))
	if err = s.audit(tx, p, "extract_security_trace", id, "success", c.GetString("trace_id")); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(200, gin.H{"delivery_id": id, "trace_valid": tokenValid && signatureValid, "content_matched": matched, "delivery_state": proof.State, "notice": "标识可恢复且签名有效用于追溯；编辑或重新保存可能移除标识，匹配不单独证明责任归属"})
}
