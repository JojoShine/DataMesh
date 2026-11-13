package integration

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/modules/workflow"
	"gorm.io/gorm"
)

type lightweightTestFile struct {
	FileID      string            `json:"file_id"`
	Name        string            `json:"name"`
	Rows        int               `json:"rows"`
	Fields      []workflow.Field  `json:"fields"`
	FieldLabels map[string]string `json:"field_labels"`
}
type lightweightTestUnit struct {
	ID      string                      `json:"id"`
	Version int                         `json:"version"`
	Graph   workflow.GovernanceDataflow `json:"graph"`
}

func lightweightTestUpload(t *testing.T, router http.Handler, cookie *http.Cookie, csrf, name, data string, want int) lightweightTestFile {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(part, data); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", origin+"/api/v1/lightweight-governance/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("upload=%d %s", w.Code, w.Body.String())
	}
	var file lightweightTestFile
	if want == 201 {
		if err = json.Unmarshal(w.Body.Bytes(), &file); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"resources/", "sha256", "object", "key"} {
			if strings.Contains(w.Body.String(), `"`+secret+`"`) {
				t.Fatal("private object metadata leaked")
			}
		}
	}
	return file
}
func TestLightweightGovernanceLifecycle(t *testing.T) {
	db, service, router := fixture(t)
	dept := "light-" + fmt.Sprint(time.Now().UnixNano())
	uid, name := businessUser(t, service, "light_owner_", dept, []string{"governor"})
	_, otherName := businessUser(t, service, "light_other_", dept, []string{"governor"})
	cookie, actor := login(t, router, name)
	other, otherActor := login(t, router, otherName)
	// This actor has governance only: uploads do not require exchange/resource roles.
	if err := db.Exec("DELETE FROM user_platform_access WHERE user_id=? AND platform<>'governance'", uid).Error; err != nil {
		t.Fatal(err)
	}
	var customers, orders strings.Builder
	customers.WriteString("\xef\xbb\xbf编号,姓名\n")
	orders.WriteString("编号,代码\n")
	for i := 1; i <= 150; i++ {
		name := " Alice "
		if i == 10 {
			name = " "
		}
		if i == 11 {
			name = ""
		}
		fmt.Fprintf(&customers, "%04d,%s\n", i, name)
		fmt.Fprintf(&orders, "%04d,%05d\n", i, i)
	}
	a := lightweightTestUpload(t, router, cookie, actor.CSRFToken, "客户.csv", customers.String(), 201)
	b := lightweightTestUpload(t, router, cookie, actor.CSRFToken, "代码.csv", orders.String(), 201)
	lightweightTestUpload(t, router, cookie, actor.CSRFToken, "bad.csv", "a,b\n1\n", 400)
	if a.Rows != 150 || a.Fields[0].LogicalType != "string" || a.FieldLabels[a.Fields[0].Name] != "编号" {
		t.Fatalf("file DTO wrong: %+v", a)
	}
	seq := 0
	post := func(path string, body interface{}, want int) []byte {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("light-%s-%d", uid, seq))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	base := "/api/v1/lightweight-governance"
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "customers", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: a.FileID, Version: 1}, Fields: []string{a.Fields[0].Name, a.Fields[1].Name}}},
		{ID: "codes", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: b.FileID, Version: 1}, Fields: []string{b.Fields[0].Name, b.Fields[1].Name}}},
		{ID: "joined", Kind: "join", Inputs: []workflow.GovernanceFlowEdge{{Node: "customers"}, {Node: "codes"}}, Join: &workflow.GovernanceFlowJoin{Kind: "inner", LeftKeys: []string{a.Fields[0].Name}, RightKeys: []string{b.Fields[0].Name}, Columns: []workflow.GovernanceFlowJoinColumn{{Side: "left", Source: a.Fields[0].Name, Target: "id"}, {Side: "left", Source: a.Fields[1].Name, Target: "name"}, {Side: "right", Source: b.Fields[1].Name, Target: "code"}}}},
		{ID: "clean", Kind: "transform", Inputs: []workflow.GovernanceFlowEdge{{Node: "joined"}}, Columns: []workflow.GovernanceFlowColumn{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}, {Source: "code", Target: "code", Rule: "copy"}}},
		{ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "clean"}}, Checks: []workflow.Check{{Kind: "required", Field: "name", Blocking: true}}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, InferModel: true},
	}}
	var unit lightweightTestUnit
	if err := json.Unmarshal(post(base+"/units", map[string]interface{}{"name": "双 CSV 清洗", "graph": graph}, 201), &unit); err != nil {
		t.Fatal(err)
	}
	// Another owner cannot reuse private IDs, including an upload with the shared-file type.
	w := workflowRequest(router, "POST", base+"/units", jsonBody(t, map[string]interface{}{"name": "窃取", "graph": graph}), other, otherActor.CSRFToken, "light-other-"+uid)
	if w.Code != 403 {
		t.Fatalf("other upload reuse=%d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{base + "/units/" + unit.ID, base + "/units/" + unit.ID + "/runs"} {
		w = workflowRequest(router, "GET", path, nil, other, "", "")
		if w.Code != 403 {
			t.Fatalf("other read=%d", w.Code)
		}
	}
	post(base+"/units/"+unit.ID+"/run", map[string]interface{}{"expected_version": 0}, 409)
	post(base+"/units/"+unit.ID+"/run", map[string]interface{}{"expected_version": 1, "workspace_id": "inject"}, 400)
	w = workflowRequest(router, "GET", base+"/files?workspace_id=inject", nil, cookie, "", "")
	if w.Code != 400 {
		t.Fatal("workspace query accepted")
	}
	var run workflow.GovernanceRun
	if err := json.Unmarshal(post(base+"/units/"+unit.ID+"/run", map[string]int{"expected_version": 1}, 201), &run); err != nil {
		t.Fatal(err)
	}
	wait := func(id string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			w := workflowRequest(router, "GET", base+"/runs/"+id, nil, cookie, "", "")
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var item workflow.GovernanceRun
			if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
				t.Fatal(err)
			}
			if item.State == "succeeded" {
				return item
			}
			if item.State == "failed" || item.State == "interrupted" || time.Now().After(deadline) {
				t.Fatalf("run state=%s code=%s", item.State, item.ErrorCode)
			}
		}
	}
	done := wait(run.ID)
	var report struct {
		OutputRows     int                     `json:"output_rows"`
		RejectedRows   int                     `json:"rejected_rows"`
		GeneratedModel *workflow.GovernanceRef `json:"generated_model"`
	}
	if err := json.Unmarshal(done.Result, &report); err != nil {
		t.Fatal(err)
	}
	if done.WorkspaceID != "" || report.OutputRows != 148 || report.RejectedRows != 2 || report.GeneratedModel != nil {
		t.Fatalf("wrong result: %s", done.Result)
	}
	var isNull bool
	if err := db.Raw("SELECT workspace_id IS NULL FROM governance_runs WHERE id=?", run.ID).Scan(&isNull).Error; err != nil || !isNull {
		t.Fatal("run did not persist NULL workspace", err)
	}
	for _, path := range []string{"", "/records", "/rejections", "/download"} {
		w = workflowRequest(router, "GET", base+"/runs/"+run.ID+path, nil, other, "", "")
		if w.Code != 403 {
			t.Fatalf("other result read %s=%d", path, w.Code)
		}
	}
	w = workflowRequest(router, "GET", base+"/runs/"+run.ID+"/records", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"0001"`) || !strings.Contains(w.Body.String(), `"code":"00001"`) {
		t.Fatalf("leading zeros lost: %d %s", w.Code, w.Body.String())
	}
	w = workflowRequest(router, "GET", base+"/runs/"+run.ID+"/rejections", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"node_id":"quality"`) || !strings.Contains(w.Body.String(), `"total":2`) || !strings.Contains(w.Body.String(), `"violations":[`) {
		t.Fatalf("rejection=%d %s", w.Code, w.Body.String())
	}
	var rejections struct {
		Items []struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rejections); err != nil {
		t.Fatal(err)
	}
	for _, item := range rejections.Items {
		if string(item.Data["name"]) != `""` {
			t.Fatalf("empty CSV text changed to null in rejection: %s", item.Data["name"])
		}
	}
	w = workflowRequest(router, "GET", base+"/runs/"+run.ID+"/download", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	records, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || len(records) != 149 || records[1][0] != "0001" || records[1][2] != "00001" || records[0][0] != "编号" {
		t.Fatalf("full CSV missing/changed rows=%d err=%v", len(records), err)
	}
	post("/api/v1/governance/runs/"+run.ID+"/model-draft", map[string]interface{}{}, 409)
	post("/api/v1/governance/runs/"+run.ID+"/retry", map[string]interface{}{}, 409)
	changed, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	var updated workflow.GovernanceDataflow
	if err = json.Unmarshal(changed, &updated); err != nil {
		t.Fatal(err)
	}
	output := updated.Nodes[len(updated.Nodes)-1]
	output.Inputs = []workflow.GovernanceFlowEdge{{Node: "joined"}}
	updated.Nodes = append(updated.Nodes[:3], output)
	post(base+"/units/"+unit.ID, map[string]interface{}{"expected_version": 1, "name": "新版本", "graph": updated}, 200)
	post(base+"/units/"+unit.ID, map[string]interface{}{"expected_version": 1, "name": "过时", "graph": updated}, 409)
	post(base+"/units/"+unit.ID+"/run", map[string]int{"expected_version": 1}, 409)
	var next workflow.GovernanceRun
	if err = json.Unmarshal(post(base+"/units/"+unit.ID+"/run", map[string]int{"expected_version": 2}, 201), &next); err != nil {
		t.Fatal(err)
	}
	next = wait(next.ID)
	var nextReport struct {
		OutputRows int `json:"output_rows"`
	}
	if err = json.Unmarshal(next.Result, &nextReport); err != nil || nextReport.OutputRows != 150 {
		t.Fatalf("new graph not used: %s", next.Result)
	}
	w = workflowRequest(router, "GET", base+"/runs/"+next.ID+"/records", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var copied struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	foundEmpty := false
	for _, row := range copied.Items {
		if string(row["id"]) == `"0011"` {
			foundEmpty = true
			if string(row["name"]) != `""` {
				t.Fatalf("copy changed empty CSV text: %s", row["name"])
			}
		}
	}
	if !foundEmpty {
		t.Fatal("empty CSV source row missing from copy preview")
	}
	var historical workflow.GovernanceRun
	if err = db.Where("id=?", run.ID).Take(&historical).Error; err != nil {
		t.Fatal(err)
	}
	var originalSnapshot, storedSnapshot map[string]interface{}
	if err = json.Unmarshal(done.Definition, &originalSnapshot); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(historical.Definition, &storedSnapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(originalSnapshot, storedSnapshot) {
		t.Fatal("historical graph drifted")
	}
	for _, query := range []string{"SELECT count(*) FROM governance_workspaces WHERE department_id=?", "SELECT count(*) FROM governance_definitions WHERE department_id=?", "SELECT count(*) FROM business_records WHERE department_id=? AND kind NOT IN ('lightweight_upload','lightweight_unit')", "SELECT count(*) FROM governance_outputs o JOIN governance_runs r ON r.id=o.run_id WHERE r.department_id=?"} {
		var count int64
		if err = db.Raw(query, dept).Scan(&count).Error; err != nil || count != 0 {
			t.Fatalf("lightweight created shared metadata: count=%d err=%v", count, err)
		}
	}
	// Private input object content is absent from PostgreSQL metadata rows.
	var upload workflow.Record
	if err = db.Where("kind='lightweight_upload' AND id=?", a.FileID).Take(&upload).Error; err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(upload.Body, []byte("Alice")) {
		t.Fatal("input data stored in metadata database")
	}
	lightweightAssertPrivateOperations(t, db, service, router, uid, dept, unit.ID, run.ID, next.ID, a.FileID, cookie, actor.CSRFToken)
}

func TestLightweightGovernanceLargerCSV(t *testing.T) {
	db, service, router := fixture(t)
	dept := "light-large-" + fmt.Sprint(time.Now().UnixNano())
	uid, name := businessUser(t, service, "light_large_", dept, []string{"governor"})
	cookie, actor := login(t, router, name)
	const count = 10001
	var input strings.Builder
	input.WriteString("id,name\n")
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&input, "%06d, Value \n", i)
	}
	file := lightweightTestUpload(t, router, cookie, actor.CSRFToken, "large.csv", input.String(), 201)
	if file.Rows != count {
		t.Fatalf("uploaded rows=%d", file.Rows)
	}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: file.FileID, Version: 1}, Fields: []string{"id", "name"}}},
		{ID: "clean", Kind: "transform", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Columns: []workflow.GovernanceFlowColumn{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}}},
		{ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "clean"}}, Checks: []workflow.Check{{Kind: "required", Field: "id", Blocking: true}}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, InferModel: true},
	}}
	base := "/api/v1/lightweight-governance"
	w := workflowRequest(router, "POST", base+"/units", jsonBody(t, map[string]interface{}{"name": "大文件验证", "graph": graph}), cookie, actor.CSRFToken, "large-unit-"+uid)
	if w.Code != 201 {
		t.Fatalf("create unit=%d %s", w.Code, w.Body.String())
	}
	var unit lightweightTestUnit
	if err := json.Unmarshal(w.Body.Bytes(), &unit); err != nil {
		t.Fatal(err)
	}
	w = workflowRequest(router, "POST", base+"/units/"+unit.ID+"/run", []byte(`{"expected_version":1}`), cookie, actor.CSRFToken, "large-run-"+uid)
	if w.Code != 201 {
		t.Fatalf("run=%d %s", w.Code, w.Body.String())
	}
	var run workflow.GovernanceRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		w = workflowRequest(router, "GET", base+"/runs/"+run.ID, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("read run=%d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.State == "succeeded" {
			break
		}
		if run.State == "failed" || run.State == "interrupted" || time.Now().After(deadline) {
			t.Fatalf("run state=%s error=%s", run.State, run.ErrorCode)
		}
	}
	w = workflowRequest(router, "GET", base+"/runs/"+run.ID+"/download", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("download=%d %s", w.Code, w.Body.String())
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || len(rows) != count+1 || rows[1][0] != "000001" || rows[count][0] != "010001" || rows[count][1] != "Value" {
		t.Fatalf("download truncated or changed: rows=%d err=%v", len(rows), err)
	}
	var spaces int64
	if err := db.Model(&workflow.GovernanceWorkspace{}).Where("department_id=?", dept).Count(&spaces).Error; err != nil || spaces != 0 || run.WorkspaceID != "" {
		t.Fatalf("unexpected workspace: count=%d err=%v", spaces, err)
	}
}

func TestLightweightGovernanceQueuedCancellation(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "light_cancel_", "light-cancel-"+fmt.Sprint(time.Now().UnixNano()), []string{"governor"})
	cookie, actor := login(t, router, name)
	file := lightweightTestUpload(t, router, cookie, actor.CSRFToken, "cancel.csv", "id\n001\n", 201)
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "file_resource", ID: file.FileID, Version: 1}, Fields: []string{"id"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, InferModel: true}}}
	base := "/api/v1/lightweight-governance"
	w := workflowRequest(router, "POST", base+"/units", jsonBody(t, map[string]interface{}{"name": "取消", "graph": graph}), cookie, actor.CSRFToken, "light-cancel-unit-"+uid)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var unit lightweightTestUnit
	if err := json.Unmarshal(w.Body.Bytes(), &unit); err != nil {
		t.Fatal(err)
	}
	w = workflowRequest(router, "POST", base+"/units/"+unit.ID+"/run", []byte(`{"expected_version":1}`), cookie, actor.CSRFToken, "light-cancel-run-"+uid)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var run workflow.GovernanceRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	w = workflowRequest(router, "POST", base+"/runs/"+run.ID+"/cancel", []byte(`{}`), cookie, actor.CSRFToken, "light-cancel-"+uid)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := db.Where("id=?", run.ID).Take(&run).Error; err != nil || run.State != "cancelled" || run.WorkspaceID != "" {
		t.Fatalf("cancel failed %+v %v", run, err)
	}
	w = workflowRequest(router, "GET", base+"/runs/"+run.ID+"/download", nil, cookie, "", "")
	if w.Code != 409 {
		t.Fatal("cancelled run downloadable")
	}
	// Normal run and schedule inserts keep their workspace defaults.
	normal := workflow.GovernanceRun{ID: "light-normal-" + uid, Kind: "profile", DepartmentID: actor.DepartmentID, ActorID: uid, Definition: json.RawMessage(`{}`), Result: json.RawMessage(`null`), State: "cancelled"}
	if err := db.Create(&normal).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id=?", normal.ID).Take(&normal).Error; err != nil || normal.WorkspaceID == "" {
		t.Fatalf("normal default lost: %v", err)
	}
}

func lightweightAssertPrivateOperations(t *testing.T, db *gorm.DB, service *identity.Service, router http.Handler, uid, dept, unitID, runID, nextID, uploadID string, cookie *http.Cookie, csrf string) {
	t.Helper()
	adminID, adminName := businessUser(t, service, "light_normal_admin_", "light-admin-"+uid, []string{"admin"})
	adminCookie, _ := login(t, router, adminName)
	for _, path := range []string{"/api/v1/governance/operations", "/api/v1/governance/lineage?resource_id=" + uploadID, "/api/v1/governance/quality-trends?definition_id=" + unitID} {
		w := workflowRequest(router, "GET", path, nil, adminCookie, "", "")
		if w.Code != 200 {
			t.Fatalf("normal private filtering: %s=%d %s", path, w.Code, w.Body.String())
		}
		if bytes.Contains(w.Body.Bytes(), []byte(runID)) || bytes.Contains(w.Body.Bytes(), []byte(nextID)) {
			t.Fatalf("private run appeared in normal view %s", path)
		}
		if strings.Contains(path, "operations") {
			var value struct {
				States []struct {
					Count int64 `json:"count"`
				} `json:"run_states"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			var actual, expected int64
			for _, state := range value.States {
				actual += state.Count
			}
			if err := db.Model(&workflow.GovernanceRun{}).Where("workspace_id IS NOT NULL").Count(&expected).Error; err != nil {
				t.Fatal(err)
			}
			if actual != expected {
				t.Fatalf("private run counted in normal operations: %d/%d", actual, expected)
			}
		}
		if strings.Contains(path, "lineage") || strings.Contains(path, "quality-trends") {
			var page struct {
				Items []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Items) != 0 {
				t.Fatalf("private run counted in normal trend/lineage: %s", w.Body.String())
			}
		}
	}
	w := workflowRequest(router, "GET", "/api/v1/governance/dashboard", nil, adminCookie, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var dashboard struct {
		StartedAt   time.Time `json:"started_at"`
		ObservedAt  time.Time `json:"observed_at"`
		LineageRuns int64     `json:"lineage_runs"`
		Runs        []struct {
			Count int64 `json:"count"`
		} `json:"runs"`
		Quality []struct {
			Checked int64 `json:"checked"`
		} `json:"quality_trend"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	var expectedRuns, expectedLineage, expectedChecked int64
	if err := db.Model(&workflow.GovernanceRun{}).Where("workspace_id IS NOT NULL AND created_at>=? AND created_at<=? AND kind<>'dataflow_preview'", dashboard.StartedAt, dashboard.ObservedAt).Count(&expectedRuns).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&workflow.GovernanceRun{}).Where("workspace_id IS NOT NULL AND kind='dataflow' AND state='succeeded'").Count(&expectedLineage).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("governance_run_events e").Joins("JOIN governance_runs r ON r.id=e.run_id").Where("r.workspace_id IS NOT NULL AND e.code='QUALITY_CHECKED' AND e.created_at>=? AND e.created_at<=?", dashboard.StartedAt, dashboard.ObservedAt).Select("COALESCE(sum(e.input_count),0)").Scan(&expectedChecked).Error; err != nil {
		t.Fatal(err)
	}
	var runs, checked int64
	for _, item := range dashboard.Runs {
		runs += item.Count
	}
	for _, item := range dashboard.Quality {
		checked += item.Checked
	}
	if runs != expectedRuns || dashboard.LineageRuns != expectedLineage || checked != expectedChecked {
		t.Fatalf("private dashboard contamination runs=%d/%d lineage=%d/%d checked=%d/%d", runs, expectedRuns, dashboard.LineageRuns, expectedLineage, checked, expectedChecked)
	}
	w = workflowRequest(router, "POST", "/api/v1/governance/quality-issues", jsonBody(t, map[string]interface{}{"run_id": runID, "assignee_id": uid, "due_at": time.Now().Add(24 * time.Hour)}), cookie, csrf, "light-no-formal-issue-"+uid)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "LIGHTWEIGHT_QUALITY_ISSUE_UNAVAILABLE") {
		t.Fatalf("private run entered formal quality workflow: %d %s", w.Code, w.Body.String())
	}
	var spaces int64
	if err := db.Model(&workflow.GovernanceWorkspace{}).Where("department_id=?", dept).Count(&spaces).Error; err != nil || spaces != 0 {
		t.Fatalf("quality issue check created space: %d %v", spaces, err)
	}
	// The sentinel alert proves one complete alert scan reached the private rule.
	// It belongs to another department and deliberately uses a normal run.
	normal := workflow.GovernanceRun{ID: "light-alert-sentinel-" + uid, ActorID: adminID, DepartmentID: "light-admin-" + uid, Kind: "profile", State: "failed", Definition: json.RawMessage(`{}`), Result: json.RawMessage(`null`), ErrorCode: "SENTINEL"}
	if err := db.Create(&normal).Error; err != nil {
		t.Fatal(err)
	}
	privateRule := workflow.GovernanceAlertRule{ID: "light-private-alert-" + uid, ActorID: uid, DepartmentID: dept, Name: "private exclusion", Enabled: true, Failure: true, QualityRate: 0, Version: 1, CursorAt: time.Unix(0, 0), UpdatedAt: time.Now().Add(-time.Second)}
	sentinelRule := workflow.GovernanceAlertRule{ID: "light-sentinel-alert-" + uid, ActorID: adminID, DepartmentID: normal.DepartmentID, Name: "sentinel", Enabled: true, Failure: true, QualityRate: 1, Version: 1, CursorAt: time.Unix(0, 0), UpdatedAt: time.Now()}
	if err := db.Create(&privateRule).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&sentinelRule).Error; err != nil {
		t.Fatal(err)
	}
	worker, err := workflow.New(db)
	if err != nil {
		t.Fatal(err)
	}
	stop := worker.StartGovernanceMaintenanceWorker(context.Background())
	defer stop()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		var count int64
		if err = db.Model(&workflow.GovernanceAlert{}).Where("rule_id=?", sentinelRule.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sentinel alert scan did not complete")
		}
	}
	var leaked int64
	if err = db.Model(&workflow.GovernanceAlert{}).Where("rule_id=?", privateRule.ID).Count(&leaked).Error; err != nil || leaked != 0 {
		t.Fatalf("private run queued normal alert: %d %v", leaked, err)
	}
}
