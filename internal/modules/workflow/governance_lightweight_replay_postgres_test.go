package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLightweightPostgresCentralReplayGuard(t *testing.T) {
	dsn := os.Getenv("SANDBOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SANDBOX_TEST_DATABASE_URL not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	tx := db.Begin()
	defer tx.Rollback()
	for _, ddl := range []string{
		`CREATE TEMP TABLE audit_records (id bigint GENERATED ALWAYS AS IDENTITY,actor_id text,action text,object_id text,result text,trace_id text,created_at timestamptz) ON COMMIT DROP`,
		`CREATE TEMP TABLE business_operations (id text PRIMARY KEY,actor_id text,idempotency_key text,request_hash text,status text,resource_id text,response_status integer,response jsonb,created_at timestamptz,updated_at timestamptz,UNIQUE(actor_id,idempotency_key)) ON COMMIT DROP`,
		`CREATE TEMP TABLE business_records (id text,kind text,owner_id text,department_id text,body jsonb) ON COMMIT DROP`,
	} {
		if err = tx.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := identity.Principal{UserID: "owner", DepartmentID: "dept", FullExperience: true, CSRFToken: "first"}
	if err = tx.Exec("INSERT INTO business_records VALUES (?,?,?,?,?::jsonb)", "unit", lightweightUnitKind, p.UserID, p.DepartmentID, string(encode(lightweightUnitBody{VisitorScope: sandboxVisitorScope(p)}))).Error; err != nil {
		t.Fatal(err)
	}
	s := &Server{db: tx}
	calls := 0
	r := gin.New()
	r.POST("/general-cancel", func(c *gin.Context) { c.Set("principal", p); c.Next() }, s.mutate("cancel", func(_ *gorm.DB, _ identity.Principal, _ []byte) (int, json.RawMessage, string, error) {
		calls++
		return 200, json.RawMessage(`{"private":"result"}`), "unit", nil
	}))
	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/general-cancel", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "same-key")
		r.ServeHTTP(w, req)
		return w
	}
	a := send()
	if a.Code != 200 {
		t.Fatalf("first %d %s", a.Code, a.Body)
	}
	again := send()
	if again.Code != 200 || strings.ReplaceAll(again.Body.String(), " ", "") != strings.ReplaceAll(a.Body.String(), " ", "") || calls != 1 {
		t.Fatalf("own replay changed code=%d calls=%d body=%s", again.Code, calls, again.Body)
	}
	p.CSRFToken = "second"
	other := send()
	if other.Code != 403 || strings.Contains(other.Body.String(), "private") {
		t.Fatalf("cross session replay %d %s", other.Code, other.Body)
	}
}
