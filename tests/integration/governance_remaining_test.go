package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestGovernanceRemainingStructuredAndQuality(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "remaining_", "remaining-dept", []string{"admin"})
	cookie, p := login(t, router, name)
	seq := 0
	post := func(path string, body interface{}, status int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, p.CSRFToken, fmt.Sprint("remaining-", seq))
		if w.Code != status {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	idOf := func(value map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if e := json.Unmarshal(value["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "low", LogicalType: "integer"}, {Name: "high", LogicalType: "integer"}}
	model := workflow.GovernanceDefinition{Name: "结构化治理模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "low", LogicalType: "integer"}, {Name: "high", LogicalType: "integer"}}, Contract: workflow.GovernanceContract{MaxFailureRate: 0.5}}}
	mid := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+mid+"/versions/1/publish", map[string]interface{}{}, 200)
	check := workflow.Check{Field: "low", Kind: "compare_fields", Blocking: true, Parameters: map[string]json.RawMessage{"other_field": json.RawMessage(`"high"`), "operator": json.RawMessage(`"lte"`)}}
	refCheck := workflow.Check{Field: "id", Kind: "reference_exists", Blocking: true, Parameters: map[string]json.RawMessage{"other_field": json.RawMessage(`"id"`)}}
	template := workflow.GovernanceDefinition{Name: "上下限与跨表参照", OwnerID: uid, StewardID: uid, QualityTemplate: &workflow.GovernanceQualityTemplate{Fields: fields, ReferenceFields: fields[:1], Checks: []workflow.Check{check, refCheck}}}
	tid := idOf(post("quality-templates", map[string]interface{}{"definition": template}, 201))
	post("quality-templates/"+tid+"/versions/1/publish", map[string]interface{}{}, 200)
	storage, e := minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Region: "us-east-1"})
	if e != nil {
		t.Fatal(e)
	}
	const bucket = "datamesh-resources"
	exists, e := storage.BucketExists(context.Background(), bucket)
	if e != nil {
		t.Fatal("isolated file fixture storage unavailable")
	}
	if !exists {
		if e = storage.MakeBucket(context.Background(), bucket, minio.MakeBucketOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	key := "governance-test/" + uid
	data := []byte("id,low,high\n1,1,2\n2,3,2\n")
	sum := sha256.Sum256(data)
	if _, e = storage.PutObject(context.Background(), bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := storage.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{}); e != nil {
			t.Error("temporary file cleanup failed")
		}
	})
	fileID := "governance-file-" + uid
	cfg := workflow.FileConfig{Name: "limits.csv", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Key: key}
	if e = db.Create(&workflow.Record{Kind: "file_resource", ID: fileID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "ready", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: "结构化CSV", Adapter: "file", DataLayer: "raw", Fields: []workflow.Field{}, Config: jsonBody(t, cfg)})}).Error; e != nil {
		t.Fatal(e)
	}
	post("metadata/"+fileID+"/structured-contract", map[string]interface{}{"expected_version": 1, "model": workflow.GovernanceRef{ID: mid, Version: 1}, "header_row": 1, "sheet": ""}, 200)
	refID := "governance-reference-" + uid
	if e = db.Create(&workflow.ImportRow{BatchID: refID, RowNumber: 1, Data: json.RawMessage(`{"id":1}`)}).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Create(&workflow.Record{Kind: "table_resource", ID: refID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "ready", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: "参照数据", DataLayer: "raw", Adapter: "excel", Fields: fields[:1], Config: jsonBody(t, workflow.TableConfig{BatchID: refID, Fields: fields[:1]})})}).Error; e != nil {
		t.Fatal(e)
	}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: fileID, Version: 2}, Fields: []string{"id", "low", "high"}}}, {ID: "reference", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: refID, Version: 1}, Fields: []string{"id"}}}, {ID: "check", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}, {Node: "reference"}}, Template: &workflow.GovernanceRef{ID: tid, Version: 1}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "check", Port: "good"}}, Model: &workflow.GovernanceRef{ID: mid, Version: 1}}}}
	flow := workflow.GovernanceDefinition{Name: "文件跨表质量", OwnerID: uid, StewardID: uid, Dataflow: &graph}
	fid := idOf(post("dataflows", map[string]interface{}{"definition": flow}, 201))
	post("dataflows/"+fid+"/versions/1/publish", map[string]interface{}{}, 200)
	worker, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stop := worker.StartGovernanceWorkers(context.Background())
	defer stop()
	await := func(id, want string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(25 * time.Millisecond) {
			var run workflow.GovernanceRun
			if e := db.Where("id=?", id).Take(&run).Error; e != nil {
				t.Fatal(e)
			}
			if run.State == want {
				return run
			}
			if run.State == "failed" || run.State == "succeeded" || time.Now().After(deadline) {
				t.Fatalf("run state=%s code=%s", run.State, run.ErrorCode)
			}
		}
	}
	runID := idOf(post("dataflows/"+fid+"/versions/1/run", map[string]interface{}{}, 201))
	run := await(runID, "succeeded")
	var report struct{ OutputRows, RejectedRows int }
	if e = json.Unmarshal(run.Result, &report); e != nil {
		t.Fatal(e)
	}
	var payload map[string]interface{}
	if e = json.Unmarshal(run.Result, &payload); e != nil {
		t.Fatal(e)
	}
	if payload["output_rows"] != float64(1) || payload["rejected_rows"] != float64(1) || payload["snapshot_scope"] != "per_source_snapshot" {
		t.Fatalf("bad quality evidence %s", run.Result)
	}
	clone := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "governed_dataset", ID: runID, Version: 1}, Fields: []string{"id", "low", "high"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Model: &workflow.GovernanceRef{ID: mid, Version: 1}}}}
	copyID := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "成果继续计算", OwnerID: uid, StewardID: uid, Dataflow: &clone}}, 201))
	post("dataflows/"+copyID+"/versions/1/publish", map[string]interface{}{}, 200)
	await(idOf(post("dataflows/"+copyID+"/versions/1/run", map[string]interface{}{}, 201)), "succeeded")
	template.QualityTemplate.Checks[0].Parameters["operator"] = json.RawMessage(`"gt"`)
	post("quality-templates/"+tid+"/versions", map[string]interface{}{"base_version": 1, "definition": template}, 201)
	post("quality-templates/"+tid+"/versions/2/publish", map[string]interface{}{"migration_note": "验证失败门槛，原版本保留"}, 200)
	graph.Nodes[2].Template.Version = 2
	post("dataflows/"+fid+"/versions", map[string]interface{}{"base_version": 1, "definition": flow}, 201)
	post("dataflows/"+fid+"/versions/2/publish", map[string]interface{}{"migration_note": "质量模板升级"}, 200)
	failed := await(idOf(post("dataflows/"+fid+"/versions/2/run", map[string]interface{}{}, 201)), "failed")
	if failed.ErrorCode != "GOVERNANCE_QUALITY_BLOCKED" {
		t.Fatal("quality failure not recorded")
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/quality-trends?timezone=UTC&definition_id="+fid, nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("trend %d %s", w.Code, w.Body.String())
	}
	var trend struct {
		Items []struct{ Checked, Passed, Rejected int }
	}
	if e = json.Unmarshal(w.Body.Bytes(), &trend); e != nil || len(trend.Items) != 1 || trend.Items[0].Checked != 4 || trend.Items[0].Passed != 1 || trend.Items[0].Rejected != 3 {
		t.Fatalf("trend lacks real failed checks: %s", w.Body.String())
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "1" {
			http.Error(w, "bad query", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"low":1,"high":2}]`))
	}))
	defer upstream.Close()
	t.Setenv("API_ALLOWED_TARGETS", string(jsonBody(t, []workflow.APITarget{{Origin: upstream.URL, Addresses: []string{"127.0.0.1"}}})))
	apiID := "governance-api-" + uid
	apiConfig := workflow.APIConfig{URL: upstream.URL + "/rows", Method: "GET", AuthType: "none", Parameters: []workflow.APIParameter{{Name: "limit", Type: "integer", Required: true}}, ResponseFields: map[string]string{"id": "id", "low": "low", "high": "high"}}
	if e = db.Create(&workflow.Record{Kind: "api_resource", ID: apiID, DepartmentID: p.DepartmentID, OwnerID: uid, State: "ready", Version: 1, Body: jsonBody(t, workflow.ManagedResource{Name: "有限查询API", Adapter: "http", DataLayer: "raw", Fields: fields, Config: jsonBody(t, apiConfig)})}).Error; e != nil {
		t.Fatal(e)
	}
	clone.Nodes[0].Source = &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "api_resource", ID: apiID, Version: 1}, Fields: []string{"id", "low", "high"}, Parameters: map[string]string{"limit": "1"}}
	apiFlow := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "查询API治理", OwnerID: uid, StewardID: uid, Dataflow: &clone}}, 201))
	post("dataflows/"+apiFlow+"/versions/1/publish", map[string]interface{}{}, 200)
	await(idOf(post("dataflows/"+apiFlow+"/versions/1/run", map[string]interface{}{}, 201)), "succeeded")
}
