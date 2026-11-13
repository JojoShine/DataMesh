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

func TestCDCTaskRegistration(t *testing.T) {
	db, service, router := fixture(t, false)
	u, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil {
		t.Fatal(e)
	}
	database := strings.TrimPrefix(u.Path, "/")
	password, _ := u.User.Password()
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": u.Hostname(), "port": port, "database": database}})))
	if e = db.Exec(`CREATE TABLE cdc_registration_probe(id bigint PRIMARY KEY,name text); CREATE TABLE cdc_registration_other(id bigint PRIMARY KEY); CREATE VIEW cdc_registration_view AS SELECT id FROM cdc_registration_probe`).Error; e != nil {
		t.Fatal(e)
	}
	_, name := businessUser(t, service, "cdc_register_", "cdc-test", []string{"admin"})
	cookie, p := login(t, router, name)
	source := workflow.SourceInput{Name: "isolated CDC", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: p.UserID, Credentials: workflow.Credential{Username: u.User.Username(), Password: password}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, source), cookie, p.CSRFToken, "cdc-registration-source")
	if w.Code != 201 {
		t.Fatalf("source %d", w.Code)
	}
	var src struct {
		ID string `json:"source_id"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &src); e != nil {
		t.Fatal(e)
	}
	input := map[string]interface{}{"source_id": src.ID, "source_version": 1, "schema": "public", "table": "cdc_registration_probe", "publication": "isolated_publication"}
	// Registration saves a draft; creating/starting external capture is a separate operation.
	w = workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), cookie, p.CSRFToken, "cdc-registration-task")
	if w.Code != 201 {
		t.Fatalf("register %d %s", w.Code, w.Body.String())
	}
	var task struct {
		ID       string              `json:"task_id"`
		ObjectID string              `json:"object_id"`
		State    string              `json:"state"`
		Capture  workflow.CDCBinding `json:"capture_binding"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &task); e != nil {
		t.Fatal(e)
	}
	if task.ID == "" || task.State != "draft" || task.Capture.Table != "cdc_registration_probe" || task.Capture.Legacy {
		t.Fatal("wrong draft binding")
	}
	if strings.Contains(w.Body.String(), password) {
		t.Fatal("source credentials leaked")
	}
	var object workflow.Record
	if e = db.Where("kind='object' AND id=?", task.ObjectID).Take(&object).Error; e != nil {
		t.Fatal(e)
	}
	var obj workflow.Object
	if e = json.Unmarshal(object.Body, &obj); e != nil {
		t.Fatal(e)
	}
	if obj.QualifiedName != "public.cdc_registration_probe" || len(obj.Fields) != 2 || !obj.Fields[0].PrimaryKey {
		t.Fatal("object was not discovered from selected table")
	}
	repeated := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), cookie, p.CSRFToken, "cdc-registration-task")
	var repeatedTask map[string]interface{}
	if err := json.Unmarshal(repeated.Body.Bytes(), &repeatedTask); err != nil {
		t.Fatal(err)
	}
	if repeated.Code != 201 || repeatedTask["task_id"] != task.ID {
		t.Fatal("idempotent task changed")
	}
	duplicate := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), cookie, p.CSRFToken, "cdc-registration-duplicate")
	if duplicate.Code != 409 {
		t.Fatalf("duplicate %d", duplicate.Code)
	}
	input["table"] = "cdc_registration_other"
	next := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), cookie, p.CSRFToken, "cdc-registration-other")
	if next.Code != 201 {
		t.Fatalf("second table %d %s", next.Code, next.Body.String())
	}
	input["source_version"] = 2
	stale := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), cookie, p.CSRFToken, "cdc-registration-stale")
	if stale.Code != 409 {
		t.Fatalf("stale %d", stale.Code)
	}
	input["source_version"] = 1
	input["table"] = "cdc_registration_view"
	view := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), cookie, p.CSRFToken, "cdc-registration-view")
	if view.Code != 400 {
		t.Fatalf("view %d", view.Code)
	}
	_, consumerName := businessUser(t, service, "cdc_denied_", "other", []string{"consumer"})
	other, op := login(t, router, consumerName)
	noPermission := workflowRequest(router, "POST", "/api/v1/cdc-tasks", jsonBody(t, input), other, op.CSRFToken, "cdc-registration-denied")
	if noPermission.Code != 403 {
		t.Fatalf("permission %d", noPermission.Code)
	}
}
