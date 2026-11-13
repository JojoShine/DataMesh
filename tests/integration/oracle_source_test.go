package integration

import (
	"context"
	"database/sql"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	goora "github.com/sijms/go-ora/v3"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOracleRemoteTables(t *testing.T) {
	password := os.Getenv("ORACLE_TEST_PASSWORD")
	username := os.Getenv("ORACLE_TEST_USER")
	if password == "" {
		t.Skip("requires Oracle test instance")
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", `[{"kind":"oracle","host":"oracle-source","port":1521,"database":"FREEPDB1"}]`)
	u := url.URL{Scheme: "oracle", Host: "oracle-source:1521", Path: "FREEPDB1", User: url.UserPassword(username, password)}
	q := u.Query()
	q.Set("CONNECT TIMEOUT", "5")
	q.Set("FAST LOGIN", "false")
	u.RawQuery = q.Encode()
	oracle := sql.OpenDB(goora.NewConnector(u.String()))
	var err error
	defer oracle.Close()
	table := fmt.Sprintf("DM_TEST_%d", time.Now().UnixNano())
	initialCtx, initialCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer initialCancel()
	if _, err = oracle.ExecContext(initialCtx, `CREATE TABLE `+table+` ("id" NUMBER(20,0) PRIMARY KEY,"name" VARCHAR2(80))`); err != nil {
		t.Fatalf("create Oracle fixture: %v", err)
	}
	defer func() {
		if _, err := oracle.Exec(`DROP TABLE ` + table + ` PURGE`); err != nil {
			t.Error("cleanup Oracle fixture failed")
		}
	}()
	if _, err = oracle.Exec(`INSERT INTO ` + table + ` VALUES (9007199254740993,'oracle-test')`); err != nil {
		t.Fatal(err)
	}
	oracle.SetMaxIdleConns(1)
	probeCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var one int
	if err := oracle.QueryRowContext(probeCtx, "SELECT 1 FROM DUAL").Scan(&one); err != nil {
		t.Fatalf("Oracle SQL probe: %v", err)
	}
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "oracle_admin_", "oracle-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	post := func(path string, body interface{}, status int) map[string]interface{} {
		t.Helper()
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint(time.Now().UnixNano()))
		if w.Code != status {
			t.Fatalf("%s status %d: %s", path, w.Code, w.Body.String())
		}
		var v map[string]interface{}
		if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	input := workflow.SourceInput{Name: "Oracle remote SQL", Kind: "oracle", Host: "oracle-source", Port: 1521, Database: "FREEPDB1", OwnerID: uid, Credentials: workflow.Credential{Username: username, Password: password}}
	if post("/api/v1/sources/test-connection", input, 200)["passed"] != true {
		t.Fatal("Oracle draft connection failed")
	}
	source := post("/api/v1/sources", input, 201)
	sid := source["source_id"].(string)
	if post("/api/v1/sources/"+sid+"/test-connection", map[string]interface{}{}, 200)["passed"] != true {
		t.Fatal("Oracle saved connection failed")
	}
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}}

	assertNativeMetadataColumn(t, db, router, cookie, p, workflow.Object{ObjectID: "oracle-metadata-" + sid, SourceID: sid, QualifiedName: username + "." + table, SchemaVersion: 1, Fields: fields}, "name", 80)
	snapshot := seedSharingFixture(t, db, uid, "oracle-dept", workflow.Resource{Type: "raw_object", ID: "oracle-fixture", Version: 1}, fields)
	resource := post("/api/v1/table-resources", workflow.TableResourceInput{DirectoryID: snapshot.DirectoryID, SourceID: sid, Schema: username, Table: table, ExpectedVersion: 1}, 201)
	ref := resource["resource"].(map[string]interface{})
	w := workflowRequest(router, "GET", "/api/v1/table-resources/"+ref["resource_id"].(string)+"/preview", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"9007199254740993"`) || !strings.Contains(w.Body.String(), "oracle-test") {
		t.Fatalf("Oracle precision/preview failed %d: %s", w.Code, w.Body.String())
	}
}
