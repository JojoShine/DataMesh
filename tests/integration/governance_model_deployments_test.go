package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernancePhysicalPostgres(t *testing.T) { testGovernancePhysicalModel(t, "postgres") }
func TestGovernancePhysicalMySQL(t *testing.T)    { testGovernancePhysicalModel(t, "mysql") }
func testGovernancePhysicalModel(t *testing.T, kind string) {
	u, e := url.Parse(os.Getenv("TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	port, _ := strconv.Atoi(u.Port())
	database := strings.TrimPrefix(u.Path, "/")
	host := u.Hostname()
	username := u.User.Username()
	secret, _ := u.User.Password()
	if kind == "mysql" {
		host = "mysql-source"
		port = 3306
		database = os.Getenv("MYSQL_ETL_TEST_DATABASE")
		username = os.Getenv("MYSQL_ETL_TEST_USER")
		secret = os.Getenv("MYSQL_ETL_TEST_PASSWORD")
		if database == "" {
			t.Fatal("isolated MySQL fixture is required")
		}
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []workflow.SourceTarget{{Kind: kind, Host: host, Port: port, Database: database}})))
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "modeldeploy_", "model-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	seq := 0
	post := func(path string, value interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", path, jsonBody(t, value), cookie, p.CSRFToken, fmt.Sprint("model-", seq))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	var sourceID string
	value := post("/api/v1/sources", workflow.SourceInput{Name: "物理建表目标", Kind: kind, Host: host, Port: port, Database: database, OwnerID: uid, Credentials: workflow.Credential{Username: username, Password: secret}}, 201)
	if e = json.Unmarshal(value["source_id"], &sourceID); e != nil {
		t.Fatal(e)
	}
	schema := database
	if kind == "postgres" {
		schema = "model_" + strings.ToLower(strings.ReplaceAll(uid, "-", ""))
		if e = db.Exec("CREATE SCHEMA " + schema).Error; e != nil {
			t.Fatal(e)
		}
	}
	definition := workflow.GovernanceDefinition{Name: "实际物理模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "physical", Schema: schema, Table: "created_model", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "label", LogicalType: "string", Length: 40, Nullable: true}, {Name: "amount", LogicalType: "decimal", Precision: 20, Scale: 6}, {Name: "enabled", LogicalType: "boolean"}}}}
	value = post("/api/v1/governance/models", map[string]interface{}{"definition": definition}, 201)
	var mid string
	if e = json.Unmarshal(value["id"], &mid); e != nil {
		t.Fatal(e)
	}
	base := "/api/v1/governance/models/" + mid + "/versions/1"
	post(base+"/publish", map[string]interface{}{}, 200)
	input := map[string]interface{}{"source_id": sourceID, "source_version": 1}
	preview := post(base+"/deployment-preview", input, 200)
	var token string
	if e = json.Unmarshal(preview["preview_token"], &token); e != nil || token == "" {
		t.Fatal("missing preview proof")
	}
	input["preview_token"] = token
	input["confirm_table"] = "wrong"
	post(base+"/deployments", input, 409)
	input["confirm_table"] = schema + ".created_model"
	value = post(base+"/deployments", input, 201)
	var id string
	if e = json.Unmarshal(value["id"], &id); e != nil {
		t.Fatal(e)
	}
	post(base+"/deployments", input, 409)
	worker, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stop := worker.StartGovernanceModelWorker(context.Background())
	defer stop()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		var job workflow.GovernanceModelDeployment
		if e = db.Where("id=?", id).Take(&job).Error; e != nil {
			t.Fatal(e)
		}
		if job.State == "succeeded" {
			break
		}
		if job.State == "failed" || job.State == "unknown" || time.Now().After(deadline) {
			t.Fatalf("physical %s state=%s code=%s", kind, job.State, job.ErrorCode)
		}
	}
	preview = post(base+"/deployment-preview", map[string]interface{}{"source_id": sourceID, "source_version": 1}, 200)
	var exists, allowed bool
	_ = json.Unmarshal(preview["target_exists"], &exists)
	_ = json.Unmarshal(preview["can_execute"], &allowed)
	if !exists || allowed {
		t.Fatal("existing target can be overwritten")
	}
	post(base+"/deployments", input, 409)
	if e = db.Model(&workflow.GovernanceModelDeployment{}).Where("id=?", id).Updates(map[string]interface{}{"state": "unknown", "error_code": "MODEL_COMMIT_UNKNOWN"}).Error; e != nil {
		t.Fatal(e)
	}
	post("/api/v1/governance/model-deployments/"+id+"/reconcile", map[string]interface{}{}, 200)
	_, otherName := businessUser(t, service, "model_other_", "model-other", []string{"governor"})
	other, otherP := login(t, router, otherName)
	w := workflowRequest(router, "POST", "/api/v1/governance/model-deployments/"+id+"/reconcile", []byte(`{}`), other, otherP.CSRFToken, "model-forbidden")
	if w.Code != 403 {
		t.Fatalf("cross-department deployment exposed: %d", w.Code)
	}
}
