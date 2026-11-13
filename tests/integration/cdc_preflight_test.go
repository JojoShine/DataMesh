package integration

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"datamesh/internal/modules/workflow"
	"github.com/go-sql-driver/mysql"
)

func TestCDCRemoteDiagnostics(t *testing.T) {
	u, err := url.Parse(os.Getenv("TEST_APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	database := strings.TrimPrefix(u.Path, "/")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	db, service, router := fixture(t, false)
	if err = db.Exec("CREATE TABLE public.cdc_probe(id bigint PRIMARY KEY); CREATE TABLE public.cdc_no_key(value text); GRANT SELECT ON public.cdc_probe,public.cdc_no_key TO datamesh_app").Error; err != nil {
		t.Fatal(err)
	}
	_, name := businessUser(t, service, "cdc_diag_", "cdc-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	pass, _ := u.User.Password()
	input := workflow.SourceInput{Name: "CDC diagnostics", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: p.UserID, Credentials: workflow.Credential{Username: u.User.Username(), Password: pass}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, input), cookie, p.CSRFToken, "diag-source")
	if w.Code != 201 {
		t.Fatalf("source=%d", w.Code)
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/v1/sources/" + source.ID + "/preflight-cdc"
	var before, after int64
	if err = db.Raw("SELECT count(*) FROM pg_publication").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	type check struct {
		Check, Status, Label, Owner, Actual, Expected string
		VerifySQL                                     string `json:"verify_sql"`
		Passed                                        bool
		Reason, Suggestion                            *string
		Steps                                         []string
	}
	var result struct {
		Passed bool
		Checks []check
	}
	run := func(table, key string) {
		t.Helper()
		response := workflowRequest(router, "POST", endpoint, jsonBody(t, map[string]string{"schema": "public", "table": table}), cookie, p.CSRFToken, key)
		if response.Code != 200 {
			t.Fatalf("diagnostic=%d %s", response.Code, response.Body.String())
		}
		if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Passed {
			t.Fatal("remote CDC incorrectly marked ready")
		}
		if strings.Contains(response.Body.String(), pass) {
			t.Fatal("credential leaked")
		}
	}
	run("cdc_probe", "diag-first")
	checks := map[string]check{}
	for _, c := range result.Checks {
		checks[c.Check] = c
		if c.Status == "" || c.Label == "" || c.Owner == "" {
			t.Fatalf("incomplete check %s", c.Check)
		}
		if c.Status != "passed" && (c.Suggestion == nil || len(c.Steps) == 0) {
			t.Fatalf("missing resolution %s", c.Check)
		}
	}
	for _, code := range []string{"authentication", "wal_level", "replication_account", "replication_capacity", "table_identity", "publication", "platform_cdc"} {
		if _, ok := checks[code]; !ok {
			t.Fatalf("missing %s", code)
		}
	}
	if checks["authentication"].Status != "passed" || checks["table_identity"].Status != "passed" {
		t.Fatal("real connection and primary key not detected")
	}
	if checks["replication_account"].Status != "failed" {
		t.Fatal("ordinary read user incorrectly has replication")
	}
	if checks["platform_cdc"].Status == "unsupported" || checks["platform_cdc"].Owner != "平台管理员" {
		t.Fatal("configurable remote capture still reported as unsupported")
	}
	run("cdc_no_key", "diag-no-key")
	for _, c := range result.Checks {
		if c.Check == "table_identity" && (c.Status != "failed" || c.VerifySQL == "") {
			t.Fatal("missing primary key must have exact remediation and verification SQL")
		}
	}
	if err = db.Exec(`CREATE TABLE public."odd'table"(id bigint PRIMARY KEY); GRANT SELECT ON public."odd'table" TO datamesh_app`).Error; err != nil {
		t.Fatal(err)
	}
	run("odd'table", "diag-quoted-table")
	for _, c := range result.Checks {
		if c.Check == "table_identity" && c.Status != "passed" {
			t.Fatal("quoted table name was not bound as a value")
		}
	}
	if w = workflowRequest(router, "POST", endpoint, []byte(`{"schema":"public"}`), cookie, p.CSRFToken, "diag-invalid"); w.Code != 400 {
		t.Fatalf("partial table=%d", w.Code)
	}
	if err = db.Raw("SELECT count(*) FROM pg_publication").Scan(&after).Error; err != nil || after != before {
		t.Fatal("preflight modified publications")
	}
	var count int64
	if err = db.Model(&workflow.Record{}).Where("kind='object' AND parent_id=?", source.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("diagnostics created a false CDC object")
	}
	// Invalid credentials must leave database checks untested, not misreport settings as missing.
	input.Name = "unavailable CDC source"
	input.Credentials.Password = "incorrect-cdc-diagnostic-password"
	w = workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, input), cookie, p.CSRFToken, "diag-bad-source")
	if w.Code != 201 {
		t.Fatal("bad source registration")
	}
	if err = json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	endpoint = "/api/v1/sources/" + source.ID + "/preflight-cdc"
	run("cdc_probe", "diag-connection-failed")
	for _, c := range result.Checks {
		if c.Check == "authentication" && c.Status != "failed" {
			t.Fatal("connection failure not identified")
		}
		if c.Check == "wal_level" && c.Status != "skipped" {
			t.Fatal("unreachable database misreported as misconfigured")
		}
	}
}

func TestCDCMySQLDiagnostics(t *testing.T) {
	if os.Getenv("MYSQL_CDC_WRITE_USER") == "" {
		t.Skip("run through scripts/test_identity.py --run TestCDCMySQLDiagnostics")
	}
	database := os.Getenv("MYSQL_ETL_TEST_DATABASE")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "mysql", "host": "mysql-source", "port": 3306, "database": database}})))
	_, service, router := fixture(t, false)
	_, name := businessUser(t, service, "cdc_mysql_", "cdc-mysql", []string{"admin"})
	cookie, p := login(t, router, name)
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd = os.Getenv("MYSQL_ETL_TEST_USER"), os.Getenv("MYSQL_ETL_TEST_PASSWORD")
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", "mysql-source:3306", database
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE cdc_probe(id bigint PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, writeOnly := range []bool{false, true} {
		user, password := cfg.User, cfg.Passwd
		if writeOnly {
			user, password = os.Getenv("MYSQL_CDC_WRITE_USER"), os.Getenv("MYSQL_CDC_WRITE_PASSWORD")
		}
		key := "cdc-mysql-read"
		if writeOnly {
			key = "cdc-mysql-write"
		}
		input := workflow.SourceInput{Name: key, Kind: "mysql", Host: "mysql-source", Port: 3306, Database: database, OwnerID: p.UserID, Credentials: workflow.Credential{Username: user, Password: password}}
		response := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, input), cookie, p.CSRFToken, key)
		if response.Code != 201 {
			t.Fatalf("source=%d", response.Code)
		}
		var source struct {
			ID string `json:"source_id"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &source); err != nil {
			t.Fatal(err)
		}
		for i, table := range []string{"cdc_probe", "invalid\\'table"} {
			response = workflowRequest(router, "POST", "/api/v1/sources/"+source.ID+"/preflight-cdc", jsonBody(t, map[string]string{"schema": database, "table": table}), cookie, p.CSRFToken, key+strconv.Itoa(i))
			if response.Code != 200 {
				t.Fatalf("preflight=%d %s", response.Code, response.Body.String())
			}
			var result struct {
				Passed bool
				Checks []struct{ Check, Status string }
			}
			if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			checks := map[string]string{}
			for _, c := range result.Checks {
				checks[c.Check] = c.Status
			}
			if checks["platform_cdc"] == "unsupported" || checks["platform_cdc"] == "" {
				t.Fatal("configurable remote MySQL still reported as unsupported")
			}
			if (writeOnly || i > 0) && result.Passed {
				t.Fatal("remote MySQL incorrectly ready without readable selected table")
			}
			if checks["authentication"] != "passed" {
				t.Fatal("connection check failed")
			}
			if i == 0 {
				if checks["table_identity"] != "passed" {
					t.Fatal("visible primary key not detected")
				}
				expected := "passed"
				if writeOnly {
					expected = "failed"
				}
				if checks["table_read"] != expected {
					t.Fatalf("writeOnly=%t read=%s", writeOnly, checks["table_read"])
				}
			} else if checks["table_identity"] != "failed" || checks["table_read"] != "failed" {
				t.Fatal("quoted input caused an ambiguous SQL error instead of an absent table")
			}
			if strings.Contains(response.Body.String(), password) {
				t.Fatal("credential leaked")
			}
		}
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM cdc_probe").Scan(&count); err != nil || count != 0 {
		t.Fatal("diagnostic changed business data")
	}
}

func TestCDCLocalScopeStatus(t *testing.T) {
	db, service, router := fixture(t, false)
	_, name := businessUser(t, service, "cdc_scope_", "cdc-scope", []string{"admin"})
	cookie, p := login(t, router, name)
	input := workflow.SourceInput{Name: "bound CDC scope", Kind: "postgres", Host: "postgres-source", Port: 5432, Database: "sample", OwnerID: p.UserID, Credentials: workflow.Credential{Username: "scope-test", Password: "scope-test-unavailable"}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, input), cookie, p.CSRFToken, "cdc-scope-source")
	if w.Code != 201 {
		t.Fatalf("source=%d", w.Code)
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE business_records SET body=jsonb_set(body,'{cdc_status}','\"passed\"'::jsonb) WHERE kind='source' AND id=?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	w = workflowRequest(router, "POST", "/api/v1/sources/"+source.ID+"/preflight-cdc", []byte(`{"schema":"public","table":"other_table"}`), cookie, p.CSRFToken, "cdc-scope-other")
	if w.Code != 200 {
		t.Fatalf("diagnostic=%d", w.Code)
	}
	var status string
	if err := db.Raw("SELECT body->>'cdc_status' FROM business_records WHERE kind='source' AND id=?", source.ID).Scan(&status).Error; err != nil || status != "passed" {
		t.Fatal("unsupported table check invalidated the bound source status")
	}
}
