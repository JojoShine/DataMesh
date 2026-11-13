package workflow

import (
	"bytes"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Uses temporary tables inside a rolled-back transaction; never mutates application rows.
func TestSandboxPostgresScopeAndReservation(t *testing.T) {
	dsn := os.Getenv("SANDBOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SANDBOX_TEST_DATABASE_URL not set")
	}
	db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sqlDB, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, ddl := range []string{
		`CREATE TEMP TABLE business_records (id text,kind text) ON COMMIT DROP`,
		`CREATE TEMP TABLE governance_runs (id text) ON COMMIT DROP`,
		`CREATE TEMP TABLE audit_records (id bigint GENERATED ALWAYS AS IDENTITY,actor_id text,action text,object_id text,result text,trace_id text,created_at timestamptz) ON COMMIT DROP`,
		`CREATE TEMP TABLE business_operations (id text PRIMARY KEY,actor_id text,idempotency_key text,request_hash text,status text,resource_id text,response_status integer,response jsonb,created_at timestamptz,updated_at timestamptz,UNIQUE(actor_id,idempotency_key)) ON COMMIT DROP`,
		`CREATE TEMP TABLE data_sandboxes (id text,owner_id text,department_id text,visitor_scope text,state text,version integer,expires_at timestamptz) ON COMMIT DROP`,
		`CREATE TEMP TABLE data_sandbox_queries (sandbox_id text,reserved_bytes bigint,result_object jsonb) ON COMMIT DROP`,
		`CREATE TEMP TABLE data_sandbox_objects (key text,sandbox_id text,object jsonb) ON COMMIT DROP`,
		`CREATE TEMP TABLE data_sandbox_tables (sandbox_id text,name text,state text,bytes bigint,source_object jsonb,result_object jsonb) ON COMMIT DROP`,
	} {
		if e = tx.Exec(ddl).Error; e != nil {
			t.Fatal(e)
		}
	}
	p := identity.Principal{UserID: "owner", DepartmentID: "dept", FullExperience: true, CSRFToken: "first"}
	if e = tx.Exec("INSERT INTO data_sandboxes VALUES (?,?,?,?,?,?,?)", "box", p.UserID, p.DepartmentID, sandboxVisitorScope(p), "active", 1, time.Now().Add(time.Hour)).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = sandboxRead(tx, p, "box", true); e != nil {
		t.Fatal(e)
	}
	other := p
	other.CSRFToken = "second"
	if _, e = sandboxRead(tx, other, "box", false); e == nil {
		t.Fatal("cross-session read accepted")
	}
	other.CSRFToken = ""
	if _, e = sandboxRead(tx, other, "box", false); e == nil {
		t.Fatal("empty-session read accepted")
	}
	box := DataSandbox{ID: "box"}
	if e = tx.Exec("INSERT INTO data_sandbox_tables(sandbox_id,name,state,bytes) VALUES ('box','source','unknown',?)", sandboxMaxBytes-(20<<20)).Error; e != nil {
		t.Fatal(e)
	}
	server := &Server{db: tx, runtime: &Runtime{}}
	for attempt := 0; attempt < 5; attempt++ {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "duplicate.csv")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("id\n1\n"))
		_ = writer.WriteField("name", "SOURCE")
		_ = writer.Close()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/api/v1/data-sandboxes/box/tables", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		c.Params = gin.Params{{Key: "id", Value: "box"}}
		c.Set("principal", p)
		server.sandboxUpload(c)
		if recorder.Code != 409 {
			t.Fatalf("duplicate upload status=%d", recorder.Code)
		}
	}
	calls := 0
	router := gin.New()
	router.Use(func(c *gin.Context) {
		actor := p
		if c.GetHeader("X-Test-Session") != "" {
			actor.CSRFToken = c.GetHeader("X-Test-Session")
		}
		c.Set("principal", actor)
		c.Next()
	}, sandboxMutationScope)
	router.POST("/api/v1/data-sandboxes/:id/test", server.sandboxReplay("sandbox_replay_test", func(tx *gorm.DB, p identity.Principal, id, tableID string, body []byte) (int, json.RawMessage, string, error) {
		calls++
		key, err := newID()
		if err != nil {
			return 0, nil, "", err
		}
		return 201, encode(map[string]string{"file_id": key}), key, nil
	}))
	request := func(body, session string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/data-sandboxes/box/test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "same-key")
		req.Header.Set("X-Test-Session", session)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	replayFileID := func(response *httptest.ResponseRecorder) string {
		var value struct {
			FileID string `json:"file_id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value.FileID
	}
	first := request("{}", "")
	second := request("{}", "")
	if first.Code != 201 || second.Code != 201 || replayFileID(first) != replayFileID(second) || calls != 1 {
		t.Fatalf("replay not stable: %d %d calls=%d", first.Code, second.Code, calls)
	}
	if changed := request(`{"different":true}`, ""); changed.Code != 409 || calls != 1 {
		t.Fatal("different-body key accepted")
	}
	if other := request("{}", "other-session"); other.Code != 201 || calls != 2 || replayFileID(other) == replayFileID(first) {
		t.Fatal("cross-session replay collision")
	}
	router.GET("/api/v1/operations/:id", server.operation)
	var op Operation
	if e = tx.Where("resource_id=?", replayFileID(first)).Take(&op).Error; e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/api/v1/operations/"+op.ID, nil)
	req.Header.Set("X-Test-Session", "other-session")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatal("cross-session operation response readable")
	}
	var objects int64
	if e = tx.Table("data_sandbox_objects").Count(&objects).Error; e != nil {
		t.Fatal(e)
	}
	if objects != 0 {
		t.Fatal("repeated duplicate uploads created object intents")
	}
	if e = tx.Exec("INSERT INTO data_sandbox_objects VALUES ('pending','box',?)", encode(FileConfig{Key: "pending", Size: sandboxMaxBytes})).Error; e != nil {
		t.Fatal(e)
	}
	for retry := 0; retry < 3; retry++ {
		if _, err := server.sandboxStoreFile(sandboxObjectContext(t.Context(), "box"), "new.csv", "text/csv", []byte("x")); err == nil {
			t.Fatal("pending object quota bypassed")
		}
	}
	if e = tx.Table("data_sandbox_objects").Count(&objects).Error; e != nil {
		t.Fatal(e)
	}
	if objects != 1 {
		t.Fatal("over-budget retries grew object intents")
	}
	if e = tx.Exec("DELETE FROM data_sandbox_objects").Error; e != nil {
		t.Fatal(e)
	}

	if e = sandboxReserve(tx, box, "result", 20<<20); e != nil {
		t.Fatal(e)
	}
	if e = sandboxReserve(tx, box, "result", (20<<20)+1); e == nil {
		t.Fatal("unknown write reservation was released")
	}
	if e = tx.Exec("INSERT INTO data_sandbox_queries(sandbox_id,reserved_bytes) VALUES ('box',?)", 4<<20).Error; e != nil {
		t.Fatal(e)
	}
	if e = sandboxReserve(tx, box, "result", 20<<20); e == nil {
		t.Fatal("preview reservation ignored")
	}
	for _, ddl := range []string{
		`ALTER TABLE data_sandbox_tables ADD COLUMN id text, ADD COLUMN query_id text, ADD COLUMN attempt_id text, ADD COLUMN cancel_requested boolean DEFAULT false, ADD COLUMN lease_owner text, ADD COLUMN lease_until timestamptz, ADD COLUMN updated_at timestamptz`,
		`ALTER TABLE data_sandbox_queries ADD COLUMN id text, ADD COLUMN state text, ADD COLUMN result_table_id text, ADD COLUMN cancel_requested boolean DEFAULT false, ADD COLUMN lease_owner text, ADD COLUMN lease_until timestamptz, ADD COLUMN updated_at timestamptz`,
		`ALTER TABLE data_sandboxes ADD COLUMN updated_at timestamptz`,
	} {
		if e = tx.Exec(ddl).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Exec("DELETE FROM data_sandbox_tables").Error; e != nil {
		t.Fatal(e)
	}
	if e = tx.Exec("DELETE FROM data_sandbox_queries").Error; e != nil {
		t.Fatal(e)
	}
	if e = tx.Exec("INSERT INTO data_sandbox_queries(id,sandbox_id,state,result_table_id,reserved_bytes) VALUES ('cancel-job','box','queued','cancel-result',0)").Error; e != nil {
		t.Fatal(e)
	}
	if e = tx.Exec("INSERT INTO data_sandbox_tables(id,sandbox_id,name,state,query_id,bytes) VALUES ('cancel-result','box','pending','queued','cancel-job',?),('other-result','other-box','keep','queued','other-job',?)", 20<<20, 20<<20).Error; e != nil {
		t.Fatal(e)
	}
	cancelResponse := httptest.NewRecorder()
	cancelContext, _ := gin.CreateTestContext(cancelResponse)
	cancelContext.Request = httptest.NewRequest("POST", "/cancel", strings.NewReader("{}"))
	cancelContext.Params = gin.Params{{Key: "id", Value: "box"}, {Key: "query_id", Value: "cancel-job"}}
	cancelContext.Set("principal", p)
	server.sandboxQueryCancel(cancelContext)
	if cancelResponse.Code != 200 {
		t.Fatalf("cancel failed: %d %s", cancelResponse.Code, cancelResponse.Body.String())
	}
	var canceled DataSandboxQuery
	var reserved DataSandboxTable
	if e = tx.Where("id='cancel-job'").Take(&canceled).Error; e != nil {
		t.Fatal(e)
	}
	if canceled.State != "canceled" || !canceled.CancelRequested {
		t.Fatal("queued job not canceled")
	}
	if e = tx.Where("id='cancel-result'").Take(&reserved).Error; e != nil {
		t.Fatal(e)
	}
	if reserved.State != "removed" || reserved.Bytes != 0 || reserved.LeaseUntil != nil {
		t.Fatal("unstarted reservation not released")
	}
	if e = tx.Exec("UPDATE data_sandboxes SET state='closing',version=2,expires_at=now()-interval '1 second' WHERE id='box'").Error; e != nil {
		t.Fatal(e)
	}
	frozenVersion := 1
	if e = server.sandboxFence(t.Context(), tx, "box", "old-token", &frozenVersion); e == nil {
		t.Fatal("expired box permits publication")
	}
	if e = server.sandboxCleanupBox(t.Context(), DataSandbox{ID: "box"}); e != nil {
		t.Fatal(e)
	}
	var closed DataSandbox
	if e = tx.Where("id='box'").Take(&closed).Error; e != nil {
		t.Fatal(e)
	}
	if closed.State != "closed" {
		t.Fatal("expired box not cleaned")
	}
	reserved = DataSandboxTable{}
	if e = tx.Where("id='other-result'").Take(&reserved).Error; e != nil {
		t.Fatal(e)
	}
	if reserved.Bytes != 20<<20 || reserved.State != "queued" {
		t.Fatal("other box reservation altered")
	}

}
