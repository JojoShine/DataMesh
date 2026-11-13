package workflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"datamesh/internal/modules/identity"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

func securityMarkExcel(body []byte, text, id string) ([]byte, error) {
	if len(id) < 8 {
		return nil, invalid
	}
	z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, invalid
	}
	for _, entry := range z.File {
		if strings.HasPrefix(strings.ToLower(entry.Name), "_xmlsignatures/") {
			return nil, problem(409, "SECURITY_SIGNED_FILE_UNSUPPORTED", "带数字签名的文件不能重新保存为水印副本")
		}
	}
	if e := validateOffice(body, ".xlsx"); e != nil {
		return nil, e
	}
	book, e := excelize.OpenReader(bytes.NewReader(body))
	if e != nil {
		return nil, problem(409, "SECURITY_EXCEL_UNSUPPORTED", "此Excel文件无法安全生成交付标记")
	}
	defer book.Close()
	for _, sheet := range book.GetSheetList() {
		if e = book.SetHeaderFooter(sheet, &excelize.HeaderFooterOptions{OddHeader: "&C" + strings.ReplaceAll(text, "&", "&&"), EvenHeader: "&C" + strings.ReplaceAll(text, "&", "&&")}); e != nil {
			return nil, e
		}
	}
	sheet := "交付说明-" + id[:8]
	if _, e = book.NewSheet(sheet); e != nil {
		return nil, e
	}
	if e = book.SetCellStr(sheet, "A1", "安全交付标记（说明页与打印页眉；不代表网格内动态水印）"); e != nil {
		return nil, e
	}
	if e = book.SetCellStr(sheet, "A2", text); e != nil {
		return nil, e
	}
	if e = book.SetCellStr(sheet, "A3", "交付编号："+id); e != nil {
		return nil, e
	}
	out, e := book.WriteToBuffer()
	if e != nil {
		return nil, e
	}
	if out.Len() > 64<<20 {
		return nil, problem(413, "SECURITY_FILE_LIMIT", "水印副本超过64MiB上限")
	}
	return out.Bytes(), nil
}
func (s *Server) securitySendFile(c *gin.Context, p identity.Principal, ref Resource, body []byte, file FileConfig) error {
	live, err := s.securityDeliveryPrincipal(c.Request.Context(), p.UserID)
	if err != nil {
		return err
	}
	p = live
	tx := s.db.WithContext(c.Request.Context())
	var proof *SecurityDeliveryProof
	name := file.Name
	e := tx.Transaction(func(t *gorm.DB) error {
		grant, _, _, fields, err := s.authorizeDelivery(t, p, ref, DeliveryDownload, nil)
		if err != nil {
			return err
		}
		d, err := s.securityPolicyCheck(t, p, ref, "download")
		if err != nil {
			return err
		}
		proof, err = s.securityPrepareProof(t, p, ref, "download", grant.ID, fields, nil, d, "")
		if err != nil {
			return err
		}
		if d.FileMark || d.Invisible {
			extension := strings.ToLower(filepath.Ext(file.Name))
			if proof == nil {
				return problem(503, "SECURITY_PROOF_REQUIRED", "文件标记缺少签名交付凭据")
			}
			if d.FileMark {
				switch extension {
				case ".xlsx":
					body, err = securityMarkExcel(body, proof.Text, proof.ID)
				case ".pdf":
					body, err = securityMarkPDF(body, proof.Text, proof.ID)
				default:
					return problem(409, "SECURITY_FILE_FORMAT_UNSUPPORTED", "此格式不支持受控内容水印")
				}
				if err != nil {
					return err
				}
			}
			if d.Invisible {
				_, key, keyErr := securitySigningKey(proof.KeyID)
				if keyErr != nil {
					return keyErr
				}
				token := securityTraceToken(*proof, key)
				switch extension {
				case ".xlsx":
					body, err = securityTraceExcel(body, token)
				case ".pdf":
					body, err = securityTracePDF(body, token)
				default:
					return problem(409, "SECURITY_FILE_FORMAT_UNSUPPORTED", "此格式不支持隐式溯源标识")
				}
				if err != nil {
					return err
				}
			}
			name = strings.TrimSuffix(file.Name, filepath.Ext(file.Name)) + "-受控交付" + extension
		}
		if proof != nil {
			_, key, err := securitySigningKey(proof.KeyID)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			proof.SHA256 = hex.EncodeToString(sum[:])
			proof.SizeBytes = int64(len(body))
			proof.Signature = securityProofSignature(*proof, key)
			if err = t.Save(proof).Error; err != nil {
				return err
			}
		}
		return s.audit(t, p, "download_file", ref.ID, "started", c.GetString("trace_id"))
	})
	if e != nil {
		return e
	}
	if proof != nil {
		c.Header("X-DataMesh-Delivery-ID", proof.ID)
		c.Header("X-DataMesh-Watermark", url.QueryEscape(proof.Text))
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Length", fmt.Sprint(len(body)))
	c.Header("Content-Type", file.MIME)
	n, writeErr := c.Writer.Write(body)
	state, result := "completed", "success"
	if writeErr != nil || n != len(body) {
		state = "interrupted"
		result = "interrupted"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(final *gorm.DB) error {
		if err := s.audit(final, p, "download_file", ref.ID, result, c.GetString("trace_id")); err != nil {
			return err
		}
		if proof != nil {
			return final.Model(&SecurityDeliveryProof{}).Where("id=?", proof.ID).Updates(map[string]interface{}{"state": state, "updated_at": time.Now().UTC()}).Error
		}
		return nil
	})
	if err != nil {
		c.Error(err)
	}
	return writeErr
}
