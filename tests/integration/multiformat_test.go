package integration

import (
	"bytes"
	"context"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/xuri/excelize/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMultiformatRealDelivery(t *testing.T) {
	db, service, router := fixture(t)
	ownerID, ownerName := businessUser(t, service, "multi_admin_", "multi-provider", []string{"admin"})
	_, consumerName := businessUser(t, service, "multi_user_", "multi-consumer", []string{"consumer"})
	owner, op := login(t, router, ownerName)
	consumer, cp := login(t, router, consumerName)
	post := func(path string, v interface{}, status int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, v), owner, op.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s %d: %s", path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	upload := func(path, dir, name string, data []byte) map[string]interface{} {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("directory_id", dir); err != nil {
			t.Fatal(err)
		}
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
		req := httptest.NewRequest("POST", "https://localhost:18443"+path, &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", op.CSRFToken)
		req.AddCookie(owner)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 201 {
			t.Fatalf("upload %d %s", w.Code, w.Body.String())
		}
		var out map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	store, e := minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Region: "us-east-1"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		var uploads []workflow.Record
		if err := db.Where("kind IN ? AND owner_id=?", []string{"resource_upload", "resource_import"}, ownerID).Find(&uploads).Error; err != nil {
			t.Error(err)
			return
		}
		for _, upload := range uploads {
			var v workflow.UploadData
			if err := json.Unmarshal(upload.Body, &v); err != nil {
				t.Error(err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := store.RemoveObject(ctx, "datamesh-resources", v.File.Key, minio.RemoveObjectOptions{})
			cancel()
			if err != nil {
				t.Error("test object cleanup failed")
			}
		}
	})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"private"}]`))
	}))
	defer upstream.Close()
	t.Setenv("API_ALLOWED_TARGETS", fmt.Sprintf(`[{"origin":%q,"addresses":["127.0.0.1"]}]`, upstream.URL))
	fields := []workflow.Field{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Sensitive: true}}
	for _, variant := range []string{"excel", "file", "api", "database", "mysql", "governed_database", "governed_excel"} {
		t.Run(variant, func(t *testing.T) {
			kind := strings.TrimPrefix(variant, "governed_")
			stem := fmt.Sprintf("multi_%s_%d", kind, time.Now().UnixNano())
			testFields := fields
			if kind == "file" {
				testFields = nil
			}
			snapshot := seedSharingFixture(t, db, ownerID, "multi-provider", workflow.Resource{Type: "raw_object", ID: stem, Version: 1}, testFields)
			var revision workflow.Record
			if e := db.Where("kind='directory_revision' AND id=?", snapshot.DirectoryRevisionID).Take(&revision).Error; e != nil {
				t.Fatal(e)
			}
			var def workflow.DirectoryRevisionData
			json.Unmarshal(revision.Body, &def)
			method := "query"
			if kind == "file" {
				def.Definition.Format = "file"
				method = "download"
			}
			if kind == "api" {
				def.Definition.Format = "api"
				method = "invoke"
			}
			if strings.HasPrefix(variant, "governed_") {
				def.Definition.DataLayer = "governed_dataset"
			}
			def.Definition.SharingMethod = method
			if e := db.Model(&revision).Update("body", jsonBody(t, def)).Error; e != nil {
				t.Fatal(e)
			}
			if e := db.Where("id IN ?", []string{snapshot.MountID, snapshot.MountRevisionID}).Delete(&workflow.Record{}).Error; e != nil {
				t.Fatal(e)
			}
			var created map[string]interface{}
			switch kind {
			case "excel":
				f := excelize.NewFile()
				for i, row := range [][]interface{}{{"id", "name"}, {"9007199254740993", "private"}} {
					axis, _ := excelize.CoordinatesToCellName(1, i+1)
					f.SetSheetRow("Sheet1", axis, &row)
				}
				buf, e := f.WriteToBuffer()
				f.Close()
				if e != nil {
					t.Fatal(e)
				}
				uploaded := upload("/api/v1/resource-imports", snapshot.DirectoryID, "data.xlsx", buf.Bytes())
				batch := uploaded["batch_id"].(string)
				input := workflow.ImportMapping{Sheet: "Sheet1", HeaderRow: 1, ExpectedVersion: 1}
				post("/api/v1/resource-imports/"+batch+"/preview", input, 200)
				created = post("/api/v1/resource-imports/"+batch+"/confirm", input, 201)
				repeated := post("/api/v1/resource-imports/"+batch+"/confirm", input, 200)
				if fmt.Sprint(repeated["resource"]) != fmt.Sprint(created["resource"]) {
					t.Fatal("duplicate confirmation created another version")
				}
			case "file":
				up := upload("/api/v1/file-resources/uploads", snapshot.DirectoryID, "说明.txt", []byte("真实文件内容"))
				created = post("/api/v1/file-resources", map[string]interface{}{"upload_id": up["upload_id"]}, 201)
			case "api":
				created = post("/api/v1/api-resources", workflow.APIInput{DirectoryID: snapshot.DirectoryID, ExpectedVersion: 1, Config: workflow.APIConfig{URL: upstream.URL, Method: "GET", AuthType: "none", ResponseFields: map[string]string{"id": "id", "name": "name"}}}, 201)
			case "mysql":
				src := post("/api/v1/sources", workflow.SourceInput{Name: stem, Kind: "mysql", Host: "mysql-source", Port: 3306, Database: "sample", OwnerID: ownerID, Credentials: workflow.Credential{Username: "datamesh_cdc", Password: os.Getenv("SOURCE_CDC_PASSWORD")}}, 201)
				created = post("/api/v1/table-resources", workflow.TableResourceInput{DirectoryID: snapshot.DirectoryID, SourceID: src["source_id"].(string), Schema: "sample", Table: "customers", ExpectedVersion: 1}, 201)
			case "database":
				u, e := url.Parse(os.Getenv("TEST_APP_DSN"))
				if e != nil {
					t.Fatal(e)
				}
				port, _ := strconv.Atoi(u.Port())
				if port == 0 {
					port = 5432
				}
				pass, _ := u.User.Password()
				t.Setenv("SOURCE_ALLOWED_TARGETS", fmt.Sprintf(`[{"kind":"postgres","host":%q,"port":%d,"database":%q}]`, u.Hostname(), port, strings.TrimPrefix(u.Path, "/")))
				if e = db.Exec(`CREATE TABLE ` + stem + ` (id bigint PRIMARY KEY,name text NOT NULL)`).Error; e != nil {
					t.Fatal(e)
				}
				if e = db.Exec(`INSERT INTO ` + stem + ` VALUES (9007199254740993,'private')`).Error; e != nil {
					t.Fatal(e)
				}
				if e = db.Exec(`GRANT SELECT ON ` + stem + ` TO datamesh_app`).Error; e != nil {
					t.Fatal(e)
				}
				src := post("/api/v1/sources", workflow.SourceInput{Name: stem, Kind: "postgres", Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"), OwnerID: ownerID, Credentials: workflow.Credential{Username: u.User.Username(), Password: pass}}, 201)
				created = post("/api/v1/table-resources", workflow.TableResourceInput{DirectoryID: snapshot.DirectoryID, SourceID: src["source_id"].(string), Schema: "public", Table: stem, ExpectedVersion: 1}, 201)
			}
			var ref workflow.Resource
			json.Unmarshal(jsonBody(t, created["resource"]), &ref)
			mapping := map[string]string{}
			scope := []string{}
			for _, field := range testFields {
				mapping[field.Name] = field.Name
				scope = append(scope, field.Name)
			}
			mounted := post("/api/v1/resource-mounts", workflow.MountInput{DirectoryID: snapshot.DirectoryID, Resource: ref, Mapping: mapping}, 201)
			mid := mounted["mount_id"].(string)
			version := func() int {
				t.Helper()
				var m workflow.Record
				if e := db.Where("kind='mount' AND id=?", mid).Take(&m).Error; e != nil {
					t.Fatal(e)
				}
				return m.Version
			}
			post("/api/v1/resource-mounts/"+mid+"/validate", workflow.VersionAction{ExpectedVersion: version()}, 200)
			post("/api/v1/resource-mounts/"+mid+"/submit", workflow.VersionAction{ExpectedVersion: version()}, 200)
			var review workflow.Record
			if e := db.Where("kind='review' AND body->>'target_id'=? AND state='pending'", mid).Take(&review).Error; e != nil {
				t.Fatal(e)
			}
			post("/api/v1/directory-reviews/"+review.ID+"/approve", workflow.VersionAction{ExpectedVersion: review.Version}, 200)
			post("/api/v1/resource-mounts/"+mid+"/activate", workflow.VersionAction{ExpectedVersion: version()}, 200)
			application := workflow.GrantRequest{Resource: ref, DepartmentID: cp.DepartmentID, Purpose: "真实交付验证", Fields: scope, ExpiresAt: time.Now().Add(time.Hour), Materials: &workflow.ApplicationMaterials{LegalBasis: "test", Scenario: "test", Scope: "test", Method: method, DepartmentApprovalRef: "test"}}
			w := workflowRequest(router, "POST", "/api/v1/applications", jsonBody(t, application), consumer, cp.CSRFToken, stem)
			if w.Code != 201 {
				t.Fatalf("apply %d %s", w.Code, w.Body.String())
			}
			var app struct {
				ID string `json:"application_id"`
			}
			json.Unmarshal(w.Body.Bytes(), &app)
			post("/api/v1/applications/"+app.ID+"/approve", map[string]string{}, 200)
			var grant workflow.Record
			if e := db.Where("kind='grant' AND parent_id=?", app.ID).Take(&grant).Error; e != nil {
				t.Fatal(e)
			}
			deliver := func() *httptest.ResponseRecorder {
				switch kind {
				case "file":
					return workflowRequest(router, "GET", "/delivery/v1/files/"+ref.ID+"/content?contract_version=1", nil, consumer, "", "")
				case "api":
					return workflowRequest(router, "POST", "/delivery/v1/apis/"+ref.ID+"/invoke", jsonBody(t, map[string]interface{}{"contract_version": 1, "parameters": map[string]string{}, "fields": scope}), consumer, cp.CSRFToken, "")
				default:
					return workflowRequest(router, "GET", "/delivery/v1/tables/"+ref.ID+"/records?contract_version=1", nil, consumer, "", "")
				}
			}
			result := deliver()
			if result.Code != 200 {
				t.Fatalf("deliver %d %s", result.Code, result.Body.String())
			}
			if kind == "file" {
				if result.Body.String() != "真实文件内容" {
					t.Fatal("file changed")
				}
			} else {
				if strings.Contains(result.Body.String(), "private") {
					t.Fatal("sensitive value leaked")
				}
				if kind != "api" && kind != "mysql" && !strings.Contains(result.Body.String(), "9007199254740993") {
					t.Fatal("integer precision lost")
				}
			}
			post("/api/v1/grants/"+grant.ID+"/revoke", map[string]string{}, 200)
			before := calls.Load()
			result = deliver()
			if result.Code != 403 {
				t.Fatalf("revoked %d %s", result.Code, result.Body.String())
			}
			if calls.Load() != before {
				t.Fatal("revoked request reached upstream")
			}
		})
	}
}
