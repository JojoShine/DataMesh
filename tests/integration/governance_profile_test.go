package integration

import (
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestGovernanceProfileLifecycle(t *testing.T) {
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
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "profile_admin_", "profile-dept", []string{"admin"})
	governorID, governorName := businessUser(t, service, "profile_governor_", "profile-dept", []string{"governor"})
	cookie, actor := login(t, router, name)
	governor, gov := login(t, router, governorName)
	seedAssignment(t, db, governorID, "profile-dept", "govern_resource")
	pass, _ := u.User.Password()
	input := workflow.SourceInput{Name: "剖析来源", Kind: "postgres", Host: u.Hostname(), Port: port, Database: database, OwnerID: uid, Credentials: workflow.Credential{Username: u.User.Username(), Password: pass}}
	w := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, input), cookie, actor.CSRFToken, "profile-source")
	if w.Code != 201 {
		t.Fatalf("source=%d %s", w.Code, w.Body.String())
	}
	var source struct {
		ID string `json:"source_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE TABLE public.governance_profile_fixture (id bigint PRIMARY KEY, score numeric(12,2), secret text); INSERT INTO public.governance_profile_fixture VALUES (1,10,'private-first'),(2,2,'private-second'),(3,2,NULL),(4,NULL,'private-second'); GRANT SELECT ON public.governance_profile_fixture TO datamesh_app`).Error; err != nil {
		t.Fatal(err)
	}
	intPointer := func(v int) *int { return &v }
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "score", LogicalType: "decimal", Nullable: true, Precision: intPointer(12), Scale: intPointer(2)}, {Name: "secret", LogicalType: "string", Nullable: true, Sensitive: true}}
	id := "profile-table-" + uid
	cfg := workflow.TableConfig{SourceID: source.ID, Schema: "public", Table: "governance_profile_fixture", Fields: fields, Keys: []string{"id"}}
	// The mount marks a sensitive field; the physical source discovery need not do so.
	cfg.Fields = append([]workflow.Field{}, fields...)
	cfg.Fields[2].Sensitive = false
	managed := workflow.ManagedResource{Name: "剖析库表", DataLayer: "raw", Fields: fields, Adapter: "database", Config: jsonBody(t, cfg)}
	if err = db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
		t.Fatal(err)
	}
	request := map[string]interface{}{"resource": map[string]interface{}{"resource_type": "table_resource", "resource_id": id, "contract_version": 1}, "fields": []string{"id", "score", "secret"}, "mode": "full"}
	forbidden := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, request), governor, gov.CSRFToken, "no-profile-grant")
	if forbidden.Code != 403 {
		t.Fatalf("unapproved profiling=%d %s", forbidden.Code, forbidden.Body.String())
	}
	create := func(body interface{}, key string) string {
		t.Helper()
		w := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, body), cookie, actor.CSRFToken, key)
		if w.Code != 201 {
			t.Fatalf("enqueue=%d %s", w.Code, w.Body.String())
		}
		var run struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil || run.ID == "" {
			t.Fatal("missing run id")
		}
		return run.ID
	}
	wait := func(runID string) map[string]json.RawMessage {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID, nil, cookie, "", "")
			if w.Code != 200 {
				t.Fatalf("run=%d %s", w.Code, w.Body.String())
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var state string
			_ = json.Unmarshal(result["state"], &state)
			if state == "succeeded" {
				return result
			}
			if state == "failed" || state == "interrupted" {
				t.Fatalf("profile failed: %s", w.Body.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("profile did not finish")
		return nil
	}
	fullID := create(request, "full-profile")
	result := wait(fullID)
	_, reviewerName := businessUser(t, service, "profile_reviewer_", "profile-dept", []string{"admin"})
	reviewer, _ := login(t, router, reviewerName)
	if response := workflowRequest(router, "GET", "/api/v1/governance/runs/"+fullID, nil, reviewer, "", ""); response.Code != 200 {
		t.Fatalf("administrator report review=%d %s", response.Code, response.Body.String())
	}

	var report struct {
		Mode     string `json:"mode"`
		Coverage string `json:"coverage"`
		Rows     int    `json:"rows"`
		Total    int    `json:"total_rows"`
		Fields   []struct {
			Name       string  `json:"name"`
			Nulls      int     `json:"null_count"`
			Distinct   *int    `json:"distinct_count"`
			Duplicates *int    `json:"duplicate_count"`
			Min        *string `json:"min"`
			Max        *string `json:"max"`
			Redacted   bool    `json:"redacted"`
		} `json:"fields"`
	}
	if err = json.Unmarshal(result["result"], &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "full" || report.Coverage != "full" || report.Rows != 4 || report.Total != 4 {
		t.Fatalf("coverage=%s", result["result"])
	}
	for _, f := range report.Fields {
		if f.Name == "score" && (f.Nulls != 1 || f.Distinct == nil || *f.Distinct != 2 || f.Duplicates == nil || *f.Duplicates != 1 || f.Min == nil || *f.Min != "2.00" || f.Max == nil || *f.Max != "10.00") {
			t.Fatalf("numeric ordering/statistics=%+v", f)
		}
		if f.Name == "secret" && (!f.Redacted || f.Nulls != 1 || f.Distinct != nil || f.Min != nil) {
			t.Fatal("sensitive statistics not suppressed")
		}
	}
	if strings.Contains(string(result["result"]), "private-") {
		t.Fatal("sensitive sample leaked")
	}
	request["mode"] = "sample"
	request["sample_rows"] = 2
	sample := wait(create(request, "sample-profile"))
	if err = json.Unmarshal(sample["result"], &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "sample" || report.Coverage != "sample" || report.Rows != 2 || report.Total != 4 {
		t.Fatalf("sample falsely labeled full: %s", sample["result"])
	}
	events := workflowRequest(router, "GET", "/api/v1/governance/runs/"+fullID+"/events", nil, cookie, "", "")
	if events.Code != 200 || !strings.Contains(events.Body.String(), "PROFILE_COMPLETED") || strings.Contains(events.Body.String(), "private-") {
		t.Fatalf("events=%d %s", events.Code, events.Body.String())
	}
	if err = db.Exec("TRUNCATE public.governance_profile_fixture").Error; err != nil {
		t.Fatal(err)
	}
	request["mode"] = "full"
	delete(request, "sample_rows")
	empty := wait(create(request, "empty-profile"))
	if err = json.Unmarshal(empty["result"], &report); err != nil || report.Rows != 0 || report.Total != 0 || report.Coverage != "full" {
		t.Fatalf("empty=%s", empty["result"])
	}
	if w := workflowRequest(router, "POST", "/api/v1/governance/runs/"+fullID+"/cancel", []byte(`{}`), cookie, actor.CSRFToken, "cancel-completed"); w.Code != 409 {
		t.Fatalf("cancel terminal=%d %s", w.Code, w.Body.String())
	}
}

func TestGovernanceProfileExcelPrecision(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "profile_excel_", "profile-excel", []string{"admin"})
	cookie, actor := login(t, router, name)
	batch := "profile-excel-" + uid
	for i, raw := range []string{`{"id":9007199254740993,"amount":"1.0"}`, `{"id":9007199254740995,"amount":"1.00"}`} {
		if err := db.Create(&workflow.ImportRow{BatchID: batch, RowNumber: i + 1, Data: json.RawMessage(raw)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	precision, scale := 12, 2
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "decimal", Precision: &precision, Scale: &scale}}
	managed := workflow.ManagedResource{Name: "精确数值导入", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: batch, Fields: fields, Keys: []string{"id"}})}
	id := "profile-import-" + uid
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
		t.Fatal(err)
	}
	body := map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: id, Version: 1}, "fields": []string{"id", "amount"}, "mode": "full"}
	w := workflowRequest(router, "POST", "/api/v1/governance/profiles", jsonBody(t, body), cookie, actor.CSRFToken, "precise-profile")
	if w.Code != 201 {
		t.Fatalf("enqueue=%d %s", w.Code, w.Body.String())
	}
	var queued struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		w = workflowRequest(router, "GET", "/api/v1/governance/runs/"+queued.ID, nil, cookie, "", "")
		var run struct {
			State  string                 `json:"state"`
			Result workflow.ProfileReport `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil || w.Code != 200 {
			t.Fatalf("run=%d %s", w.Code, w.Body.String())
		}
		if run.State == "failed" {
			t.Fatalf("run failed: %s", w.Body.String())
		}
		if run.State != "succeeded" {
			continue
		}
		if len(run.Result.Anomalies) != 1 || run.Result.Anomalies[0].Code != "duplicate_value" || run.Result.Anomalies[0].Field != "amount" || run.Result.Anomalies[0].Ordinal != 2 {
			t.Fatalf("missing duplicate anomaly locations: %+v", run.Result.Anomalies)
		}
		for _, field := range run.Result.Fields {
			if field.Name == "id" && (field.Min == nil || *field.Min != "9007199254740993" || field.Max == nil || *field.Max != "9007199254740995") {
				t.Fatalf("integer precision lost: %+v", field)
			}
			if field.Name == "amount" && (field.DistinctCount == nil || *field.DistinctCount != 1 || field.DuplicateCount == nil || *field.DuplicateCount != 1) {
				t.Fatalf("numeric equivalent values counted differently: %+v", field)
			}
		}
		return
	}
	t.Fatal("profile did not finish")
}
