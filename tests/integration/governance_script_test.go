package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGovernanceScriptExecution(t *testing.T) {
	origin := os.Getenv("TEST_CODE_RUNNER_URL")
	if origin == "" {
		t.Fatal("real code runner required: set TEST_CODE_RUNNER_URL and TEST_CODE_RUNNER_TOKEN_FILE")
	}
	t.Setenv("CODE_RUNNER_URL", origin)
	t.Setenv("CODE_RUNNER_TOKEN_FILE", os.Getenv("TEST_CODE_RUNNER_TOKEN_FILE"))
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "script_owner_", "script-dept", []string{"admin"})
	cookie, actor := login(t, router, name)
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("script-", serial))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var result map[string]json.RawMessage
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	idOf := func(value map[string]json.RawMessage) string {
		var id string
		if e := json.Unmarshal(value["id"], &id); e != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	wait := func(id string) workflow.GovernanceRun {
		t.Helper()
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+id, nil, cookie, "", "")
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var run workflow.GovernanceRun
			if e := json.Unmarshal(w.Body.Bytes(), &run); e != nil {
				t.Fatal(e)
			}
			if run.State != "queued" && run.State != "running" {
				return run
			}
		}
		t.Fatal("run did not finish")
		return workflow.GovernanceRun{}
	}
	runtimes := workflowRequest(router, "GET", "/api/v1/governance/code-runtimes", nil, cookie, "", "")
	if runtimes.Code != 200 {
		t.Fatal(runtimes.Body.String())
	}
	var inventory struct {
		Items []struct {
			Language  string `json:"language"`
			Version   string `json:"runtime_version"`
			Available bool   `json:"available"`
		} `json:"items"`
	}
	if e := json.Unmarshal(runtimes.Body.Bytes(), &inventory); e != nil {
		t.Fatal(e)
	}
	precision, scale := 20, 2
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}, {Name: "amount", LogicalType: "decimal", Precision: &precision, Scale: &scale}}
	source := "script-source-" + uid
	for i, row := range []string{`{"id":9223372036854775807,"name":" 客户甲 ","amount":"9999999999999999.99"}`, `{"id":2,"name":" 客户乙 ","amount":"-2.00"}`} {
		if e := db.Create(&workflow.ImportRow{BatchID: source, RowNumber: i + 1, Data: json.RawMessage(row)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	managed := workflow.ManagedResource{Name: source, DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: source, Fields: fields})}
	if e := db.Create(&workflow.Record{Kind: "table_resource", ID: source, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
		t.Fatal(e)
	}
	declared := []workflow.GovernanceField{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string"}, {Name: "amount", LogicalType: "decimal", Precision: 20, Scale: 2}}
	modelFields := append([]workflow.GovernanceField{}, declared...)
	modelFields[0].PrimaryKey = true
	model := workflow.GovernanceDefinition{Name: "代码客户模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: modelFields, Contract: workflow.GovernanceContract{MaxFailureRate: 0.5}}}
	modelID := idOf(post("/api/v1/governance/models", map[string]interface{}{"definition": model}, 201))
	post("/api/v1/governance/models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	if len(inventory.Items) != 2 {
		t.Fatal("two runtimes required")
	}
	for _, runtime := range inventory.Items {
		t.Run(runtime.Language, func(t *testing.T) {
			if !runtime.Available {
				t.Fatal("runtime unavailable")
			}
			code := "from decimal import Decimal\ndef transform(row, context):\n row['name']=row['name'].strip()\n row['amount']=str(Decimal(row['amount'])+Decimal('0.01'))\n return row"
			if runtime.Language == "javascript" {
				code = "function transform(row, context) {row.name=row.name.trim();row.amount=new Decimal(row.amount).plus('0.01').toFixed(2);return row;}"
			}
			graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Inputs: []workflow.GovernanceFlowEdge{}, Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: source, Version: 1}, Fields: []string{"id", "name", "amount"}}}, {ID: "script", Kind: "script", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Script: &workflow.GovernanceFlowScript{Language: runtime.Language, RuntimeVersion: runtime.Version, Source: code, Fields: declared}}, {ID: "quality", Kind: "quality", Inputs: []workflow.GovernanceFlowEdge{{Node: "script"}}, Checks: []workflow.Check{{Field: "amount", Kind: "range", Blocking: true, Parameters: map[string]json.RawMessage{"min": json.RawMessage("0")}}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, Model: &workflow.GovernanceRef{ID: modelID, Version: 1}}}}
			definition := workflow.GovernanceDefinition{Name: "真实代码治理 " + runtime.Language, OwnerID: uid, StewardID: uid, Dataflow: &graph}
			// Preview is valid before any downstream model has been configured.
			previewDef := definition
			prefix := graph
			prefix.Nodes = append([]workflow.GovernanceFlowNode{}, graph.Nodes[:2]...)
			previewDef.Dataflow = &prefix
			previewID := idOf(post("/api/v1/governance/dataflows/code-preview", map[string]interface{}{"definition": previewDef, "node_id": "script", "sample_limit": 20}, 201))
			preview := wait(previewID)
			if preview.State != "succeeded" {
				t.Fatalf("preview %s %s", preview.State, preview.ErrorCode)
			}
			samples := workflowRequest(router, "GET", "/api/v1/governance/runs/"+previewID+"/code-preview-records", nil, cookie, "", "")
			if samples.Code != 200 || !strings.Contains(samples.Body.String(), "10000000000000000.00") {
				t.Fatal(samples.Body.String())
			}
			var count int64
			if e := db.Table("governance_outputs").Where("run_id=?", previewID).Count(&count).Error; e != nil || count != 0 {
				t.Fatal("preview created output", e)
			}
			post("/api/v1/datasets/"+previewID+"/publish", map[string]interface{}{}, 404)
 _,foreignName:=businessUser(t,service,"script_foreign_","script-foreign",[]string{"governor"});foreignCookie,_:=login(t,router,foreignName);foreign:=workflowRequest(router,"GET","/api/v1/governance/runs/"+previewID+"/code-preview-records",nil,foreignCookie,"","");if foreign.Code!=403{t.Fatal("preview leaked across departments")}

			flowID := idOf(post("/api/v1/governance/dataflows", map[string]interface{}{"definition": definition}, 201))
			post("/api/v1/governance/dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
			runID := idOf(post("/api/v1/governance/dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}, 201))
			run := wait(runID)
			if run.State != "succeeded" {
				t.Fatalf("full run %s %s", run.State, run.ErrorCode)
			}
			records := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"/records", nil, cookie, "", "")
			if records.Code != 200 || !strings.Contains(records.Body.String(), "9223372036854775807") || !strings.Contains(records.Body.String(), "10000000000000000.00") || strings.Contains(records.Body.String(), "客户乙") {
				t.Fatal(records.Body.String())
			}
			post("/api/v1/datasets/"+runID+"/publish", map[string]interface{}{}, 200)
			graph.Nodes[1].Script.Source = "def transform(row, context):\n raise ValueError('SENSITIVE_VALUE')"
			if runtime.Language == "javascript" {
				graph.Nodes[1].Script.Source = "function transform(row, context) {\n throw new Error('SENSITIVE_VALUE');\n}"
			}
			failedID := idOf(post("/api/v1/governance/dataflows/code-preview", map[string]interface{}{"definition": definition, "node_id": "script", "sample_limit": 20}, 201))
			failed := wait(failedID)
			if failed.State != "failed" {
				t.Fatal("invalid code succeeded")
			}
			events := workflowRequest(router, "GET", "/api/v1/governance/runs/"+failedID+"/events", nil, cookie, "", "")
			if events.Code != 200 || strings.Contains(events.Body.String(), "SENSITIVE_VALUE") || !strings.Contains(events.Body.String(), `"error_line":2`) {
				t.Fatal(events.Body.String())
			}

 // Retry executes the stored failed code, even after the editor draft has been changed.
 graph.Nodes[1].Script.Source="def transform(row, context):\n return row";if runtime.Language=="javascript"{graph.Nodes[1].Script.Source="function transform(row, context){return row}"}
 retriedID:=idOf(post("/api/v1/governance/runs/"+failedID+"/retry",map[string]interface{}{},201));if retried:=wait(retriedID);retried.State!="failed"{t.Fatal("retry changed fixed code")}
 graph.Nodes[1].Script.Source="def transform(row, context):\n while True: pass";if runtime.Language=="javascript"{graph.Nodes[1].Script.Source="function transform(row, context){while(true){}}"}
 cancelledID:=idOf(post("/api/v1/governance/dataflows/code-preview",map[string]interface{}{"definition":definition,"node_id":"script","sample_limit":20},201))
 deadline:=time.Now().Add(10*time.Second);for time.Now().Before(deadline){var current workflow.GovernanceRun;if e:=db.Where("id=?",cancelledID).Take(&current).Error;e!=nil{t.Fatal(e)};if current.State=="running"{break};time.Sleep(50*time.Millisecond)}
 time.Sleep(300*time.Millisecond);post("/api/v1/governance/runs/"+cancelledID+"/cancel",map[string]interface{}{},200);if cancelled:=wait(cancelledID);cancelled.State!="cancelled"{t.Fatal("code cancellation did not stop run")};if e:=db.Table("governance_flow_rows").Where("run_id=?",cancelledID).Count(&count).Error;e!=nil||count!=0{t.Fatal("cancelled code retained partial rows",e)}

		})
	}
}
