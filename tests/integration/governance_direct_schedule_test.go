package integration

import (
	"context"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGovernanceDirectDataflowSchedule(t *testing.T) {
	db, service, router := fixture(t, false)
	uid, name := businessUser(t, service, "direct_schedule_", "direct-schedule-dept", []string{"admin"})
	cookie, actor := login(t, router, name)
	sourceID := "direct-source-" + uid
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}}
	for i := 1; i <= 2; i++ {
		if e := db.Create(&workflow.ImportRow{BatchID: sourceID, RowNumber: i, Data: json.RawMessage(fmt.Sprintf(`{"id":%d}`, i))}).Error; e != nil {
			t.Fatal(e)
		}
	}
	managed := workflow.ManagedResource{Name: "直接调度来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: sourceID, Fields: fields})}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: sourceID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	seq := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		seq++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprintf("direct-%d", seq))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	idOf := func(v map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if e := json.Unmarshal(v["id"], &id); e != nil || id == "" {
			t.Fatal("missing ID")
		}
		return id
	}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: sourceID, Version: 1}, Fields: []string{"id"}}},
		{ID: "filter", Kind: "filter", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Condition: &workflow.GovernanceFlowCondition{Field: "id", Operator: "gte", Parameter: "minimum", Value: json.RawMessage("1")}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "filter"}}, InferModel: true, DataLayer: "dws"},
	}}
	def := workflow.GovernanceDefinition{Name: "独立批量处理", OwnerID: uid, StewardID: uid, Dataflow: &graph}
	created := post("dataflows", map[string]interface{}{"definition": def}, 201)
	fid := idOf(created)
	if string(created["execution_mode"]) != `"batch"` {
		t.Fatal("legacy create must default to batch")
	}
	ref := workflow.GovernanceRef{ID: fid, Version: 1}
	body := func(r workflow.GovernanceRef) map[string]interface{} {
		return map[string]interface{}{"dataflow": r, "parameters": map[string]interface{}{"minimum": 2}, "timezone": "Asia/Shanghai", "interval_seconds": 3600, "concurrency_policy": "forbid", "missed_policy": "latest", "next_run_at": time.Now().Add(-time.Second)}
	}
	post("schedules", body(ref), 409)
	post("dataflows/"+fid+"/versions/1/publish", map[string]interface{}{}, 200)
	mismatch := post("dataflows/"+fid+"/versions/1/stream", map[string]interface{}{}, 409)
	if string(mismatch["code"]) != `"GOVERNANCE_FLOW_MODE_MISMATCH"` {
		t.Fatal("batch stream start not rejected by mode")
	}
	invalidMode := post("dataflows", map[string]interface{}{"definition": def, "execution_mode": "auto"}, 400)
	if len(invalidMode) == 0 {
		t.Fatal("missing invalid mode response")
	}
	stream := post("dataflows", map[string]interface{}{"definition": def, "execution_mode": "continuous"}, 201)
	streamID := idOf(stream)
	post("dataflows/"+streamID+"/versions/1/publish", map[string]interface{}{}, 200)
	filtered := workflowRequest(router, "GET", "/api/v1/governance/dataflows?state=published&execution_mode=batch", nil, cookie, "", "")
	var filteredFlows struct {
		Items []json.RawMessage
		Total int
	}
	if filtered.Code != 200 || json.Unmarshal(filtered.Body.Bytes(), &filteredFlows) != nil || len(filteredFlows.Items) != 1 || filteredFlows.Total != 1 {
		t.Fatalf("mode-filtered count: %s", filtered.Body.String())
	}
	for _, path := range []string{"run", "execution-plan?mode=batch"} {
		result := post("dataflows/"+streamID+"/versions/1/"+path, map[string]interface{}{}, 409)
		if string(result["code"]) != `"GOVERNANCE_FLOW_MODE_MISMATCH"` {
			t.Fatalf("wrong mode error: %s", result["code"])
		}
	}
	post("schedules", body(workflow.GovernanceRef{ID: streamID, Version: 1}), 409)
	inherited := post("dataflows/"+streamID+"/versions", map[string]interface{}{"definition": def, "base_version": 1}, 201)
	if string(inherited["execution_mode"]) != `"continuous"` {
		t.Fatal("revision changed mode implicitly")
	}
	revised := post("dataflows/"+streamID+"/versions", map[string]interface{}{"definition": def, "base_version": 2, "execution_mode": "batch"}, 201)
	if string(revised["execution_mode"]) != `"batch"` {
		t.Fatal("new version did not use requested mode")
	}
	var original workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=1", streamID).Take(&original).Error; e != nil || original.ExecutionMode != "continuous" {
		t.Fatal("original version changed")
	}
	wf := workflow.GovernanceDefinition{Name: "不允许常驻子任务", OwnerID: uid, StewardID: uid, Workflow: &workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{{ID: "stream", Dataflow: workflow.GovernanceRef{ID: streamID, Version: 1}}}}}
	post("workflows", map[string]interface{}{"definition": wf}, 409)
	mixed := body(ref)
	mixed["workflow"] = ref
	post("schedules", mixed, 400)
	params := body(ref)
	params["parameters"] = map[string]interface{}{"undeclared": 1}
	post("schedules", params, 400)
	schedule := post("schedules", body(ref), 201)
	sid := idOf(schedule)
	if string(schedule["target_kind"]) != `"dataflow"` {
		t.Fatal("missing dataflow target")
	}
	post("dataflows/"+fid+"/versions/1/retire", map[string]interface{}{}, 409)
	server, e := workflow.New(db)
	if e != nil {
		t.Fatal(e)
	}
	stopA := server.StartGovernanceOrchestrator(context.Background())
	defer stopA()
	stopB := server.StartGovernanceOrchestrator(context.Background())
	defer stopB()
	waitTrigger := func(id string, expected string) *string {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var rows []struct {
				Code  string
				RunID *string
			}
			if e := db.Table("governance_schedule_triggers").Where("schedule_id=?", id).Find(&rows).Error; e != nil {
				t.Fatal(e)
			}
			if len(rows) > 0 {
				if len(rows) != 1 || rows[0].Code != expected {
					t.Fatalf("trigger count=%d code=%s", len(rows), rows[0].Code)
				}
				return rows[0].RunID
			}
			if time.Now().After(deadline) {
				t.Fatal("schedule did not trigger")
			}
		}
	}
	runID := waitTrigger(sid, "SCHEDULE_TRIGGERED")
	if runID == nil {
		t.Fatal("missing run")
	}
	post("schedules/"+sid+"/pause", map[string]interface{}{"base_version": 1}, 200)
	var run workflow.GovernanceRun
	if e := db.Where("id=?", *runID).Take(&run).Error; e != nil || run.Kind != "dataflow" {
		t.Fatal("direct schedule did not enqueue dataflow")
	}
	if !strings.Contains(string(run.Definition), `"minimum": 2`) && !strings.Contains(string(run.Definition), `"minimum":2`) {
		t.Fatal("scheduled parameter override lost")
	}
	var wrappers int64
	if e := db.Model(&workflow.GovernanceRun{}).Where("actor_id=? AND kind='workflow'", uid).Count(&wrappers).Error; e != nil || wrappers != 0 {
		t.Fatal("direct schedule created workflow wrapper")
	}
	// The queued first run prevents a second plan targeting the same flow from overlapping.
	blocked := idOf(post("schedules", body(ref), 201))
	waitTrigger(blocked, "SCHEDULE_CONCURRENCY_SKIPPED")
	post("schedules/"+blocked+"/pause", map[string]interface{}{"base_version": 1}, 200)
	stopWorkers := server.StartGovernanceWorkers(context.Background())
	defer stopWorkers()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if e := db.Where("id=?", *runID).Take(&run).Error; e != nil {
			t.Fatal(e)
		}
		if run.State == "succeeded" {
			break
		}
		if run.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("dataflow execution state=%s code=%s", run.State, run.ErrorCode)
		}
	}
	records := workflowRequest(router, "GET", "/api/v1/governance/runs/"+*runID+"/records", nil, cookie, "", "")
	var result struct {
		Items []map[string]interface{}
		Total int
	}
	if records.Code != 200 || json.Unmarshal(records.Body.Bytes(), &result) != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0]["id"] != float64(2) {
		t.Fatalf("scheduled Doris result %d %s", records.Code, records.Body.String())
	}
	var report struct {
		DataLayer string `json:"data_layer"`
		Storage   struct {
			Database string `json:"database"`
		} `json:"storage"`
		GeneratedModel workflow.GovernanceRef `json:"generated_model"`
	}
	if e := json.Unmarshal(run.Result, &report); e != nil || report.DataLayer != "dws" || report.Storage.Database != "datamesh_dws" || report.GeneratedModel.ID == "" {
		t.Fatalf("scheduled output did not preserve selected purpose: %s", run.Result)
	}
	var generated workflow.GovernanceVersion
	if e := db.Where("definition_id=? AND version=?", report.GeneratedModel.ID, report.GeneratedModel.Version).Take(&generated).Error; e != nil {
		t.Fatal(e)
	}
	var generatedDefinition workflow.GovernanceDefinition
	if e := json.Unmarshal(generated.Definition, &generatedDefinition); e != nil || generatedDefinition.Model == nil || generatedDefinition.Model.DataLayer != "dws" {
		t.Fatal("generated model lost result purpose")
	}
	post("models/"+report.GeneratedModel.ID+"/versions/1/publish", map[string]interface{}{}, 200)
	graph.Nodes[2].InferModel = false
	graph.Nodes[2].DataLayer = ""
	graph.Nodes[2].Model = &report.GeneratedModel
	bound := idOf(post("dataflows", map[string]interface{}{"definition": def}, 201))
	post("dataflows/"+bound+"/versions/1/publish", map[string]interface{}{}, 200)
	boundRun := idOf(post("dataflows/"+bound+"/versions/1/run", map[string]interface{}{}, 201))
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var latest workflow.GovernanceRun
		if e := db.Where("id=?", boundRun).Take(&latest).Error; e != nil {
			t.Fatal(e)
		}
		if latest.State == "succeeded" {
			if e := json.Unmarshal(latest.Result, &report); e != nil || report.DataLayer != "dws" || report.Storage.Database != "datamesh_dws" {
				t.Fatalf("bound model did not determine Doris layer: %s", latest.Result)
			}
			break
		}
		if latest.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("bound model execution state=%s code=%s", latest.State, latest.ErrorCode)
		}
	}
	list := workflowRequest(router, "GET", "/api/v1/governance/schedules?target_kind=dataflow&target_id="+fid, nil, cookie, "", "")
	var plans struct {
		Items []workflow.GovernanceSchedule
		Total int
	}
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &plans) != nil || plans.Total != 2 || plans.Items[0].Dataflow == nil {
		t.Fatalf("shared schedule list %d %s", list.Code, list.Body.String())
	}
	post("dataflows/"+fid+"/versions/1/retire", map[string]interface{}{}, 200)
	post("schedules/"+sid+"/resume", map[string]interface{}{"base_version": 2}, 409)
}

func TestGovernanceDirectScheduleMigration(t *testing.T) {
	db, _, _ := fixture(t, false)
	migration, e := os.ReadFile("../../migrations/000032_governance_direct_schedules.up.sql")
	if e != nil {
		t.Fatal(e)
	}
	e = db.Transaction(func(tx *gorm.DB) error {
		// Temp fixtures shadow public tables so no existing definitions are changed.
		for _, query := range []string{
			`CREATE TEMP TABLE governance_versions(definition_id text,version integer,state text,definition jsonb) ON COMMIT DROP`,
			`CREATE TEMP TABLE governance_definitions(id text,kind text) ON COMMIT DROP`,
			`CREATE TEMP TABLE governance_streams(flow_id text,flow_version integer) ON COMMIT DROP`,
			`CREATE TEMP TABLE governance_schedules(id text) ON COMMIT DROP`,
			`INSERT INTO governance_definitions VALUES('wf','workflow'),('flow','dataflow'),('batch','dataflow')`,
			`INSERT INTO governance_versions VALUES('wf',1,'published','{"workflow":{"tasks":[{"dataflow":{"id":"flow","version":1}}]}}'),('flow',1,'published','{"name":"original stream"}'),('batch',1,'published','{"name":"original batch"}')`,
			`INSERT INTO governance_streams VALUES('flow',1)`,
			`INSERT INTO governance_schedules VALUES('old')`,
		} {
			if e := tx.Exec(query).Error; e != nil {
				return e
			}
		}
		e := tx.Transaction(func(nested *gorm.DB) error { return nested.Exec(string(migration)).Error })
		if e == nil || !strings.Contains(e.Error(), "GOVERNANCE_MODE_MIGRATION_CONFLICT") {
			t.Fatalf("mixed mode migration must stop: %v", e)
		}
		if e := tx.Exec(`UPDATE governance_versions SET state='retired' WHERE definition_id='wf'`).Error; e != nil {
			return e
		}
		if e := tx.Exec(string(migration)).Error; e != nil {
			return e
		}
		var modes []struct {
			DefinitionID  string
			ExecutionMode string
			Definition    string
		}
		if e := tx.Table("governance_versions").Select("definition_id,execution_mode,definition::text").Order("definition_id").Find(&modes).Error; e != nil {
			return e
		}
		if len(modes) != 3 || modes[0].ExecutionMode != "batch" || modes[1].ExecutionMode != "continuous" || modes[1].Definition != `{"name": "original stream"}` {
			t.Fatalf("migration altered history: %+v", modes)
		}
		var kind string
		if e := tx.Table("governance_schedules").Select("target_kind").Scan(&kind).Error; e != nil {
			return e
		}
		if kind != "workflow" {
			t.Fatal("old schedule target changed")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
