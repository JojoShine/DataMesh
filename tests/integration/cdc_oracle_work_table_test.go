package integration

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"datamesh/internal/modules/workflow"
	goora "github.com/sijms/go-ora/v3"
)

func TestCDCOracleWorkTableQuota(t *testing.T) {
	user, password := os.Getenv("ORACLE_TEST_USER"), os.Getenv("ORACLE_TEST_PASSWORD")
	if !strings.HasPrefix(user, "C##DMT_") || password == "" {
		t.Fatal("isolated common Oracle account required")
	}
	u := url.URL{Scheme: "oracle", Host: "oracle-source:1521", Path: "/FREEPDB1", User: url.UserPassword(user, password)}
	q := u.Query()
	q.Set("FAST LOGIN", "false")
	u.RawQuery = q.Encode()
	db := sql.OpenDB(goora.NewConnector(u.String()))
	defer db.Close()
	if _, e := db.Exec(`CREATE TABLE QUOTA_PROBE("id" NUMBER(18,0) PRIMARY KEY,"name" VARCHAR2(80))`); e != nil {
		t.Fatal("isolated Oracle table setup failed")
	}
	if _, e := db.Exec(`ALTER TABLE QUOTA_PROBE ADD SUPPLEMENTAL LOG DATA (ALL) COLUMNS`); e != nil {
		t.Fatal("isolated supplemental log setup failed")
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "oracle", "host": "oracle-source", "port": 1521, "database": "FREE"}, {"kind": "oracle", "host": "oracle-source", "port": 1521, "database": "FREEPDB1"}})))
	_, service, router := fixture(t)
	uid, name := businessUser(t, service, "oracle_quota_", "oracle-quota", []string{"admin"})
	cookie, p := login(t, router, name)
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, workflow.SourceInput{Name: "isolated quota diagnostic", Kind: "oracle", Host: "oracle-source", Port: 1521, Database: "FREE", OwnerID: uid, Credentials: workflow.Credential{Username: user, Password: password}}), cookie, p.CSRFToken, "oracle-quota-source")
	if w.Code != 201 {
		t.Fatalf("source %d", w.Code)
	}
	var source struct {
		ID string `json:"source_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &source)
	w = workflowRequest(router, "POST", "/api/v1/cdc-tasks/validate", jsonBody(t, workflow.CDCConfigInput{SourceID: source.ID, SourceVersion: 1, Schema: user, Table: "QUOTA_PROBE", PDB: "FREEPDB1"}), cookie, p.CSRFToken, "oracle-quota-check")
	if w.Code != 200 {
		t.Fatalf("diagnostic %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Passed bool `json:"passed"`
		Checks []struct {
			Check  string   `json:"check"`
			Passed bool     `json:"passed"`
			Reason string   `json:"reason"`
			Steps  []string `json:"steps"`
		} `json:"checks"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, check := range result.Checks {
		if check.Check == "mining_work_table" {
			found = true
			if check.Passed || (!strings.Contains(check.Reason, "额度") && !strings.Contains(check.Reason, "表空间")) || !strings.Contains(strings.Join(check.Steps, " "), "QUOTA") {
				t.Fatal("quota diagnostic did not identify missing condition and DBA remedy")
			}
		}
	}
	if !found || result.Passed {
		t.Fatal("missing CDB quota was not diagnosed")
	}
	if strings.Contains(w.Body.String(), password) {
		t.Fatal("diagnostic leaked Oracle credentials")
	}
}
