package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/xuri/excelize/v2"
)

func TestDataSecurityFileWatermarkAndTrace(t *testing.T) {
	t.Setenv("SECURITY_WATERMARK_KEY_ID", "file-test")
	t.Setenv("SECURITY_WATERMARK_KEYS", `{"file-test":"`+strings.Repeat("ab", 32)+`"}`)
	db, service, router := fixture(t, false)
	ownerID, ownerName := businessUser(t, service, "security_file_", "files", []string{"admin"})
	consumerID, consumerName := businessUser(t, service, "security_freader_", "consumer", []string{"consumer"})
	owner, op := login(t, router, ownerName)
	consumer, cp := login(t, router, consumerName)
	_, outsiderName := businessUser(t, service, "security_foutsider_", "other", []string{"consumer"})
	outsider, xp := login(t, router, outsiderName)
	auditorName := "security_only_auditor_" + fmt.Sprint(time.Now().UnixNano())
	if _, err := service.CreateUser(context.Background(), identity.NewUser{Username: auditorName, Password: password, DepartmentID: "audit", Platforms: []string{"security"}, Roles: []string{"auditor"}}); err != nil {
		t.Fatal(err)
	}
	auditor, aup := login(t, router, auditorName)
	extract := func(name string, data []byte, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "https://localhost:18443/api/v1/security/extract-trace", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.AddCookie(cookie)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	store, err := minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if exists, e := store.BucketExists(context.Background(), "datamesh-resources"); e != nil {
		t.Fatal(e)
	} else if !exists {
		if e = store.MakeBucket(context.Background(), "datamesh-resources", minio.MakeBucketOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	for _, test := range []struct {
		name, ext string
		visible   bool
	}{{"xlsx_visible_implicit", ".xlsx", true}, {"pdf_visible_implicit", ".pdf", true}, {"xlsx_implicit_only", ".xlsx", false}, {"pdf_implicit_only", ".pdf", false}, {"unsupported_text", ".txt", true}} {
		t.Run(test.name, func(t *testing.T) {
			ext := test.ext
			original := []byte("unsupported plain text")
			if ext == ".pdf" {
				original = securityFilePDFFixture()
			}

			if ext == ".xlsx" {
				book := excelize.NewFile()
				if err := book.SetCellStr("Sheet1", "A1", "business-value"); err != nil {
					t.Fatal(err)
				}
				buf, err := book.WriteToBuffer()
				book.Close()
				if err != nil {
					t.Fatal(err)
				}
				original = buf.Bytes()
			}
			id := fmt.Sprintf("security-file-%s-%d", ownerID, time.Now().UnixNano())
			key := "security-tests/" + id + ext
			if _, err = store.PutObject(context.Background(), "datamesh-resources", key, bytes.NewReader(original), int64(len(original)), minio.PutObjectOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.RemoveObject(context.Background(), "datamesh-resources", key, minio.RemoveObjectOptions{}); err != nil {
					t.Error("cleanup failed")
				}
			})
			ref := workflow.Resource{Type: "file_resource", ID: id, Version: 1}
			sum := sha256.Sum256(original)
			file := workflow.FileConfig{Name: "原件" + ext, MIME: "application/octet-stream", Key: key, Size: int64(len(original)), SHA256: hex.EncodeToString(sum[:])}
			managed := workflow.ManagedResource{Name: "受控文件", DataLayer: "raw_object", Adapter: "file", Fields: []workflow.Field{}, Config: jsonBody(t, file)}
			if err = db.Create(&workflow.Record{Kind: ref.Type, ID: id, DepartmentID: "files", OwnerID: ownerID, State: "ready", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
				t.Fatal(err)
			}
			snapshot := seedSharingFixture(t, db, ownerID, "files", ref, nil)
			var dr workflow.Record
			if err = db.Where("id=?", snapshot.DirectoryRevisionID).Take(&dr).Error; err != nil {
				t.Fatal(err)
			}
			var dv workflow.DirectoryRevisionData
			json.Unmarshal(dr.Body, &dv)
			dv.Definition.DataLayer = "raw_object"
			dv.Definition.Format = "file"
			dv.Definition.SharingMethod = "download"
			if err = db.Model(&dr).Update("body", jsonBody(t, dv)).Error; err != nil {
				t.Fatal(err)
			}
			grant := workflow.GrantData{Snapshot: &snapshot, ApplicationID: "security-file-application-" + id, GranteeUserID: consumerID, ApprovedBy: ownerID, ApprovedAt: time.Now(), Scope: workflow.GrantRequest{Resource: ref, DepartmentID: cp.DepartmentID, Fields: []string{}, Purpose: "文件安全测试", ExpiresAt: time.Now().Add(time.Hour), Materials: &workflow.ApplicationMaterials{Method: "download"}}}
			if err = db.Create(&workflow.Record{Kind: "grant", ID: "grant-" + id, ParentID: grant.ApplicationID, OwnerID: consumerID, DepartmentID: cp.DepartmentID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; err != nil {
				t.Fatal(err)
			}
			for _, entry := range []struct {
				kind       string
				definition workflow.SecurityDefinition
			}{{"watermark", workflow.SecurityDefinition{Name: "文件标记", Text: "{recipient} · {delivery_id}"}}, {"policy", workflow.SecurityDefinition{Name: "文件水印", Resource: &ref, Actions: []string{"download"}, FileMark: test.visible, Invisible: true, Template: &workflow.GovernanceRef{ID: "template-" + id, Version: 1}}}} {
				controlID := entry.kind + "-" + id
				if entry.kind == "watermark" {
					controlID = "template-" + id
				}
				h := workflow.SecurityControl{ID: controlID, Kind: entry.kind, OwnerID: ownerID, DepartmentID: "files", Generation: 3, LatestVersion: 1, PublishedVersion: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if err = db.Create(&h).Error; err != nil {
					t.Fatal(err)
				}
				if err = db.Create(&workflow.SecurityControlVersion{ControlID: controlID, Version: 1, State: "published", CreatedBy: ownerID, ReviewerID: consumerID, Definition: jsonBody(t, entry.definition), CreatedAt: time.Now()}).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := workflowRequest(router, "GET", "/delivery/v1/files/"+id+"/content?contract_version=1", nil, consumer, "", "")
			if ext == ".txt" {
				if w.Code != 409 || w.Header().Get("X-DataMesh-Delivery-ID") != "" {
					t.Fatalf("unsupported file delivered: %d", w.Code)
				}
				return
			}
			if w.Code != 200 || bytes.Equal(w.Body.Bytes(), original) {
				t.Fatalf("missing watermarked derivative: %d %s", w.Code, w.Body.String())
			}
			if ext == ".xlsx" {
				book, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				defer book.Close()
				value, err := book.GetCellValue("Sheet1", "A1")
				sheets := 1
				if test.visible {
					sheets = 2
				}
				if err != nil || value != "business-value" || len(book.GetSheetList()) != sheets {
					t.Fatal("business content changed", err)
				}
			} else {
				config := model.NewDefaultConfiguration()
				config.Offline = true
				ctx, err := api.ReadContext(bytes.NewReader(w.Body.Bytes()), config)
				if err != nil {
					t.Fatal(err)
				}
				if err = api.ValidateContext(ctx); err != nil {
					t.Fatal(err)
				}
				page, _, _, err := ctx.PageDict(1, false)
				if err != nil {
					t.Fatal(err)
				}
				content, err := ctx.PageContent(page, 1)
				if err != nil || !bytes.Contains(content, []byte("business-value")) || (test.visible && !bytes.Contains(content, []byte("Do"))) {
					t.Fatal("PDF business content or visible overlay missing", err)
				}
			}

			var proof workflow.SecurityDeliveryProof
			if err = db.Where("id=?", w.Header().Get("X-DataMesh-Delivery-ID")).Take(&proof).Error; err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(w.Body.Bytes())
			if proof.State != "completed" || proof.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("proof does not describe actual derivative")
			}
			verified := workflowRequest(router, "POST", "/api/v1/security/delivery-proofs/"+proof.ID+"/verify", jsonBody(t, map[string]string{"sha256": proof.SHA256}), owner, op.CSRFToken, "verify-"+id)
			if verified.Code != 200 || !strings.Contains(verified.Body.String(), `"matched"`) {
				t.Fatal("derived file signature mismatch")
			}
			for _, actor := range []struct {
				cookie *http.Cookie
				csrf   string
			}{{consumer, cp.CSRFToken}, {auditor, aup.CSRFToken}} {
				recovered := extract("delivered"+ext, w.Body.Bytes(), actor.cookie, actor.csrf)
				var result struct {
					ID      string `json:"delivery_id"`
					Valid   bool   `json:"trace_valid"`
					Matched bool   `json:"content_matched"`
				}
				if err = json.Unmarshal(recovered.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if recovered.Code != 200 || result.ID != proof.ID || !result.Valid || !result.Matched {
					t.Fatalf("actual delivered trace mismatch: %d %s", recovered.Code, recovered.Body)
				}
			}
			unauthorized := extract("delivered"+ext, w.Body.Bytes(), outsider, xp.CSRFToken)
			if unauthorized.Code != 403 {
				t.Fatalf("unrelated consumer read proof: %d %s", unauthorized.Code, unauthorized.Body)
			}
			noCSRF := extract("delivered"+ext, w.Body.Bytes(), auditor, "")
			if noCSRF.Code != 403 {
				t.Fatalf("extract bypassed CSRF: %d", noCSRF.Code)
			}
			changed := append(bytes.Clone(w.Body.Bytes()), []byte("\n%changed\n")...)
			recovered := extract("changed"+ext, changed, auditor, aup.CSRFToken)
			if recovered.Code == 200 {
				var result struct {
					Valid   bool `json:"trace_valid"`
					Matched bool `json:"content_matched"`
				}
				if err = json.Unmarshal(recovered.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if !result.Valid || result.Matched {
					t.Fatalf("changed content misreported: %s", recovered.Body)
				}
			} else if recovered.Code != 400 && recovered.Code != 409 {
				t.Fatalf("unexpected tamper result: %d %s", recovered.Code, recovered.Body)
			}
			if ext == ".xlsx" {
				tampered := securityFileTamperExcelTrace(t, w.Body.Bytes())
				recovered = extract("tampered.xlsx", tampered, auditor, aup.CSRFToken)
				if recovered.Code == 200 {
					var result struct {
						Valid   bool `json:"trace_valid"`
						Matched bool `json:"content_matched"`
					}
					if err = json.Unmarshal(recovered.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.Valid || result.Matched {
						t.Fatalf("forged marker accepted: %s", recovered.Body)
					}
				} else if recovered.Code != 400 && recovered.Code != 409 {
					t.Fatalf("unexpected forged marker result: %d %s", recovered.Code, recovered.Body)
				}
			}
			object, err := store.GetObject(context.Background(), "datamesh-resources", key, minio.GetObjectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := io.ReadAll(object)
			object.Close()
			if err != nil || !bytes.Equal(stored, original) {
				t.Fatal("stored original changed")
			}
		})
	}
}

func securityFilePDFFixture() []byte {
	content := "BT /F1 12 Tf 72 720 Td (business-value) Tj ET\n"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content), "<< /Type /Font /Subtype /Type1 /BaseFont /Courier /FirstChar 32 /LastChar 126 /Widths [" + strings.Repeat("600 ", 95) + "] /Encoding /WinAnsiEncoding /FontDescriptor 6 0 R >>", "<< /Type /FontDescriptor /FontName /Courier /Flags 33 /FontBBox [-23 -250 715 805] /ItalicAngle 0 /Ascent 629 /Descent -157 /CapHeight 562 /StemV 51 >>"}
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	start := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), start)
	return out.Bytes()
}
func securityFileTamperExcelTrace(t *testing.T, body []byte) []byte {
	t.Helper()
	source, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	found := false
	for _, entry := range source.File {
		if entry.Name != "customXml/datamesh-delivery-trace.xml" {
			if err = writer.Copy(entry); err != nil {
				t.Fatal(err)
			}
			continue
		}
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		end := bytes.Index(raw, []byte("</trace>"))
		if end < 1 {
			t.Fatal("trace XML missing")
		}
		if raw[end-1] == 'a' {
			raw[end-1] = 'b'
		} else {
			raw[end-1] = 'a'
		}
		part, err := writer.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(raw); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatal("trace part missing")
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSecurityFilePDFFixture(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	conf.Offline = true
	conf.ValidationMode = model.ValidationStrict
	ctx, err := api.ReadContext(bytes.NewReader(securityFilePDFFixture()), conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = api.ValidateContext(ctx); err != nil {
		t.Fatal(err)
	}
}
