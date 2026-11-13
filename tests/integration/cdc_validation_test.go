package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCDCConfigurationValidation(t *testing.T) {
	db, service, router := fixture(t, false)
	u, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(u.Port())
	password, _ := u.User.Password()
	database := strings.TrimPrefix(u.Path, "/")
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	if e = db.Exec("CREATE TABLE cdc_validation_probe(id bigint PRIMARY KEY)").Error; e != nil {
		t.Fatal(e)
	}
	_, name := businessUser(t, service, "cdc_validate_", "validation", []string{"admin"})
	cookie, p := login(t, router, name)
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, workflow.SourceInput{Name: "validation", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: p.UserID, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}), cookie, p.CSRFToken, "cdc-validation-source")
	var src struct {
		ID string `json:"source_id"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &src); e != nil {
		t.Fatal(e)
	}
	input := map[string]interface{}{"source_id": src.ID, "source_version": 1, "schema": "public", "table": "cdc_validation_probe", "publication": "missing_publication"}
	w = workflowRequest(router, "POST", "/api/v1/cdc-tasks/validate", jsonBody(t, input), cookie, p.CSRFToken, "cdc-validation-config")
	if w.Code != 200 {
		t.Fatalf("validate %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Passed bool
		Checks []struct {
			Check  string
			Passed bool
			Status string
		}
	}
	if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.Passed {
		t.Fatal("missing publication and WAL conditions incorrectly pass")
	}
	seen := false
	for _, c := range result.Checks {
		if c.Check == "publication" {
			seen = true
			if c.Passed || c.Status == "skipped" {
				t.Fatal("selected publication was not checked")
			}
		}
	}
	if !seen {
		t.Fatal("publication check missing")
	}
	var n int64
	if e = db.Model(&workflow.Record{}).Where("kind='task' AND parent_id=?", src.ID).Count(&n).Error; e != nil || n != 0 {
		t.Fatal("validation created a task")
	}
	if strings.Contains(w.Body.String(), password) {
		t.Fatal("credential leaked")
	}
}
