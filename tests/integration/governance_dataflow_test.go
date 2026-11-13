package integration

import (
	"bytes"
	"compress/gzip"
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGovernanceDataflowExecution(t *testing.T) {
	parsed, err := url.Parse(os.Getenv("TEST_DSN"))
	if err != nil {
		t.Fatal("invalid isolated database")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOURCE_ALLOWED_TARGETS", string(jsonBody(t, []map[string]interface{}{{"kind": "postgres", "host": parsed.Hostname(), "port": port, "database": strings.TrimPrefix(parsed.Path, "/")}})))
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "flow_owner_", "flow-dept", []string{"admin"})
	cookie, actor := login(t, router, name)
	customerID := "flow-customers-" + uid
	orderID := "flow-orders-" + uid
	customers := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string"}}
	precision, scale := 18, 2
	orders := []workflow.Field{{Name: "customer_id", LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal", Precision: &precision, Scale: &scale}}
	for _, input := range []struct {
		ID     string
		Fields []workflow.Field
		Rows   []string
	}{{customerID, customers, []string{`{"id":1,"name":" Alice "}`, `{"id":2,"name":" Bob "}`}}, {orderID, orders, []string{`{"customer_id":1,"amount":"10.00"}`, `{"customer_id":1,"amount":"20.00"}`, `{"customer_id":2,"amount":"-2.00"}`}}} {
		for i, row := range input.Rows {
			if err := db.Create(&workflow.ImportRow{BatchID: input.ID, RowNumber: i + 1, Data: json.RawMessage(row)}).Error; err != nil {
				t.Fatal(err)
			}
		}
		managed := workflow.ManagedResource{Name: input.ID, DataLayer: "raw", Fields: input.Fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: input.ID, Fields: input.Fields})}
		if err := db.Create(&workflow.Record{Kind: "table_resource", ID: input.ID, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("flow-", serial))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		result := map[string]json.RawMessage{}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	idOf := func(value map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if err := json.Unmarshal(value["id"], &id); err != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	model := map[string]interface{}{"name": "客户汇总模型", "owner_id": uid, "steward_id": uid, "model": map[string]interface{}{"layer": "logical", "fields": []map[string]interface{}{{"name": "id", "logical_type": "integer", "primary_key": true}, {"name": "name", "logical_type": "string", "sensitive": true}, {"name": "amount", "logical_type": "decimal", "precision": 18, "scale": 2}}, "contract": map[string]interface{}{"max_failure_rate": 0.5}}}
	modelID := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	edge := func(node string) map[string]string { return map[string]string{"node": node} }
	graph := map[string]interface{}{"nodes": []map[string]interface{}{
		{"id": "customers", "kind": "source", "inputs": []interface{}{}, "source": map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: customerID, Version: 1}, "fields": []string{"id", "name"}}},
		{"id": "orders", "kind": "source", "inputs": []interface{}{}, "source": map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: orderID, Version: 1}, "fields": []string{"customer_id", "amount"}}},
		{"id": "joined", "kind": "join", "inputs": []interface{}{edge("customers"), edge("orders")}, "join": map[string]interface{}{"kind": "inner", "left_keys": []string{"id"}, "right_keys": []string{"customer_id"}, "columns": []map[string]interface{}{{"side": "left", "source": "id", "target": "id"}, {"side": "left", "source": "name", "target": "name"}, {"side": "right", "source": "amount", "target": "amount"}}}},
		{"id": "clean", "kind": "transform", "inputs": []interface{}{edge("joined")}, "columns": []map[string]string{{"source": "id", "target": "id", "rule": "copy"}, {"source": "name", "target": "name", "rule": "trim"}, {"source": "amount", "target": "amount", "rule": "copy"}}},
		{"id": "totals", "kind": "aggregate", "inputs": []interface{}{edge("clean")}, "group_by": []string{"id", "name"}, "aggregates": []map[string]string{{"operation": "sum", "source": "amount", "target": "amount"}}},
		{"id": "quality", "kind": "quality", "inputs": []interface{}{edge("totals")}, "checks": []workflow.Check{{Field: "amount", Kind: "required", Blocking: true}, {Field: "amount", Kind: "range", Blocking: true, Parameters: map[string]json.RawMessage{"min": json.RawMessage("0")}}}},
		{"id": "output", "kind": "output", "inputs": []interface{}{map[string]string{"node": "quality", "port": "good"}}, "model": workflow.GovernanceRef{ID: modelID, Version: 1}},
	}}
	definition := map[string]interface{}{"name": "客户双来源治理", "owner_id": uid, "steward_id": uid, "dataflow": graph}
	flowID := idOf(post("dataflows", map[string]interface{}{"definition": definition}, 201))
	post("dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
	post("models/"+modelID+"/versions/1/retire", map[string]interface{}{}, 409)
	runID := idOf(post("dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}, 201))
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID, nil, cookie, "", "")
		if w.Code != 200 {
			t.Fatalf("run read=%d %s", w.Code, w.Body.String())
		}
		var run struct {
			State     string `json:"state"`
			ErrorCode string `json:"error_code"`
			Result    struct {
				OutputRows   int64                  `json:"output_rows"`
				RejectedRows int64                  `json:"rejected_rows"`
				Model        workflow.GovernanceRef `json:"model"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.State == "failed" || run.State == "interrupted" {
			t.Fatalf("flow failed state=%s code=%s", run.State, run.ErrorCode)
		}
		if run.State == "succeeded" {
			if run.Result.OutputRows != 1 || run.Result.RejectedRows != 1 || run.Result.Model.ID != modelID || run.Result.Model.Version != 1 {
				t.Fatalf("quality/model result invalid: %s", w.Body.String())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dataflow did not complete")
		}
	}
	output := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"/records", nil, cookie, "", "")
	if output.Code != 200 || !strings.Contains(output.Body.String(), "Alice") || strings.Contains(output.Body.String(), "Bob") || !strings.Contains(output.Body.String(), "30.00") {
		t.Fatalf("real transformation=%d %s", output.Code, output.Body.String())
	}
	t.Run("result first then confirmed model", func(t *testing.T) {
		var exploratory map[string]interface{}
		if err := json.Unmarshal(jsonBody(t, definition), &exploratory); err != nil {
			t.Fatal(err)
		}
		nodes := exploratory["dataflow"].(map[string]interface{})["nodes"].([]interface{})
		out := nodes[len(nodes)-1].(map[string]interface{})
		delete(out, "model")
		out["infer_model"] = true
		flow := idOf(post("dataflows", map[string]interface{}{"definition": exploratory}, 201))
		post("dataflows/"+flow+"/versions/1/publish", map[string]interface{}{}, 200)
		waitResult := func(id string) workflow.GovernanceRun {
			t.Helper()
			for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
				var r workflow.GovernanceRun
				if err := db.Where("id=?", id).Take(&r).Error; err != nil {
					t.Fatal(err)
				}
				if r.State == "succeeded" {
					return r
				}
				if r.State == "failed" || time.Now().After(deadline) {
					t.Fatalf("run %s: %s %s", id, r.State, r.ErrorCode)
				}
			}
		}
		rid := idOf(post("dataflows/"+flow+"/versions/1/run", map[string]interface{}{}, 201))
		r := waitResult(rid)
		var report struct {
			Unmodeled      bool
			DatasetID      string                 `json:"dataset_id"`
			OutputRows     int                    `json:"output_rows"`
			RejectedRows   int                    `json:"rejected_rows"`
			GeneratedModel workflow.GovernanceRef `json:"generated_model"`
		}
		if err := json.Unmarshal(r.Result, &report); err != nil {
			t.Fatal(err)
		}
		if !report.Unmodeled || report.DatasetID != "" || report.OutputRows != 1 || report.RejectedRows != 1 {
			t.Fatalf("unexpected exploratory result: %s", r.Result)
		}
		if report.GeneratedModel.ID == "" || report.GeneratedModel.Version != 1 {
			t.Fatalf("successful result did not persist a model draft: %s", r.Result)
		}
		var draftHead workflow.GovernanceDefinitionRecord
		if err := db.Where("id=? AND kind='model'", report.GeneratedModel.ID).Take(&draftHead).Error; err != nil {
			t.Fatal(err)
		}
		if draftHead.WorkspaceID == nil || *draftHead.WorkspaceID != r.WorkspaceID || draftHead.DepartmentID != r.DepartmentID {
			t.Fatal("generated model did not inherit the execution workspace and department")
		}
		draft := workflowRequest(router, "GET", "/api/v1/governance/models/"+report.GeneratedModel.ID+"/versions/1", nil, cookie, "", "")
		if draft.Code != 200 || !strings.Contains(draft.Body.String(), `"state":"draft"`) {
			t.Fatalf("generated model not visible in modeling: %d %s", draft.Code, draft.Body.String())
		}
		var n int64
		if err := db.Table("governance_outputs").Where("run_id=?", rid).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("unconfirmed output registered as publishable")
		}
		rows := workflowRequest(router, "GET", "/api/v1/governance/runs/"+rid+"/records", nil, cookie, "", "")
		if rows.Code != 200 || !strings.Contains(rows.Body.String(), "Alice") {
			t.Fatalf("results unavailable: %s", rows.Body.String())
		}
		proposal := workflowRequest(router, "GET", "/api/v1/governance/runs/"+rid+"/model-proposal", nil, cookie, "", "")
		var generated struct {
			Definition     workflow.GovernanceDefinition `json:"definition"`
			GeneratedModel workflow.GovernanceRef        `json:"generated_model"`
		}
		if proposal.Code != 200 {
			t.Fatalf("proposal: %d %s", proposal.Code, proposal.Body.String())
		}
		if err := json.Unmarshal(proposal.Body.Bytes(), &generated); err != nil {
			t.Fatal(err)
		}
		if generated.GeneratedModel != report.GeneratedModel {
			t.Fatal("proposal did not resolve the persisted result model")
		}
		if generated.Definition.OwnerID != uid || !strings.Contains(generated.Definition.Description, rid) {
			t.Fatal("generated model lost execution ownership or run provenance")
		}
		if generated.Definition.Model == nil || len(generated.Definition.Model.Fields) != 3 {
			t.Fatal("result schema missing")
		}
		for _, f := range generated.Definition.Model.Fields {
			if f.PrimaryKey || !f.Nullable {
				t.Fatal("business constraints inferred without confirmation")
			}
		}
		t.Run("historical result materializes once without GET writes", func(t *testing.T) {
			// Simulate a successful result saved before automatic drafts existed.
			if err := db.Exec("UPDATE governance_runs SET result=result-'generated_model' WHERE id=?", rid).Error; err != nil {
				t.Fatal(err)
			}
			var before, after int64
			if err := db.Model(&workflow.GovernanceDefinitionRecord{}).Where("kind='model'").Count(&before).Error; err != nil {
				t.Fatal(err)
			}
			read := workflowRequest(router, "GET", "/api/v1/governance/runs/"+rid+"/model-proposal", nil, cookie, "", "")
			if read.Code != 200 {
				t.Fatalf("historical proposal: %d %s", read.Code, read.Body.String())
			}
			if err := db.Model(&workflow.GovernanceDefinitionRecord{}).Where("kind='model'").Count(&after).Error; err != nil || after != before {
				t.Fatal("reading a proposal wrote a model")
			}
			first := post("runs/"+rid+"/model-draft", map[string]interface{}{}, 201)
			second := post("runs/"+rid+"/model-draft", map[string]interface{}{}, 200)
			if idOf(first) != idOf(second) {
				t.Fatal("repeated draft creation produced duplicate models")
			}
			if err := db.Model(&workflow.GovernanceDefinitionRecord{}).Where("kind='model'").Count(&after).Error; err != nil || after != before+1 {
				t.Fatal("historical result did not materialize exactly once")
			}
		})
		t.Run("workflow child also persists result model", func(t *testing.T) {
			workflowDefinition := workflow.GovernanceDefinition{Name: "结果建模工作流", OwnerID: uid, StewardID: uid, Workflow: &workflow.GovernanceWorkflow{Tasks: []workflow.GovernanceWorkflowTask{{ID: "infer", Dataflow: workflow.GovernanceRef{ID: flow, Version: 1}}}}}
			wid := idOf(post("workflows", map[string]interface{}{"definition": workflowDefinition}, 201))
			post("workflows/"+wid+"/versions/1/publish", map[string]interface{}{}, 200)
			parent := idOf(post("workflows/"+wid+"/versions/1/run", map[string]interface{}{}, 201))
			waitResult(parent)
			var task workflow.GovernanceWorkflowTaskRun
			if err := db.Where("parent_run_id=? AND task_id='infer'", parent).Take(&task).Error; err != nil || task.ChildRunID == nil {
				t.Fatal("workflow child execution missing")
			}
			child := waitResult(*task.ChildRunID)
			var childReport struct {
				GeneratedModel *workflow.GovernanceRef `json:"generated_model"`
			}
			if err := json.Unmarshal(child.Result, &childReport); err != nil || childReport.GeneratedModel == nil || childReport.GeneratedModel.ID == "" {
				t.Fatalf("workflow child has no persisted model: %s", child.Result)
			}
			var modelHead workflow.GovernanceDefinitionRecord
			if err := db.Where("id=?", childReport.GeneratedModel.ID).Take(&modelHead).Error; err != nil || modelHead.WorkspaceID == nil || *modelHead.WorkspaceID != child.WorkspaceID || modelHead.DepartmentID != child.DepartmentID {
				t.Fatal("workflow generated model has wrong workspace or department")
			}
		})
		generated.Definition.Model.Contract.MaxFailureRate = 0.5
		mid := report.GeneratedModel.ID
		post("models/"+mid+"/versions", map[string]interface{}{"definition": generated.Definition, "base_version": 1}, 201)
		post("models/"+mid+"/versions/2/publish", map[string]interface{}{}, 200)
		delete(out, "infer_model")
		out["model"] = workflow.GovernanceRef{ID: mid, Version: 2}
		post("dataflows/"+flow+"/versions", map[string]interface{}{"definition": exploratory, "base_version": 1}, 201)
		post("dataflows/"+flow+"/versions/2/publish", map[string]interface{}{"migration_note": "Bind the confirmed model; keep exploratory run as evidence"}, 200)
		confirmed := idOf(post("dataflows/"+flow+"/versions/2/run", map[string]interface{}{}, 201))
		waitResult(confirmed)
		registered := workflowRequest(router, "GET", "/api/v1/governance/runs/"+confirmed+"/output", nil, cookie, "", "")
		if registered.Code != 200 {
			t.Fatalf("confirmed result not registered: %s", registered.Body.String())
		}
	})
	registered := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"/output", nil, cookie, "", "")
	if registered.Code != 200 || !strings.Contains(registered.Body.String(), `"governance_run_id":"`+runID+`"`) {
		t.Fatalf("automatic output registration=%d %s", registered.Code, registered.Body.String())
	}
	lineage := workflowRequest(router, "GET", "/api/v1/datasets/"+runID+"/lineage", nil, cookie, "", "")
	if lineage.Code != 200 || !strings.Contains(lineage.Body.String(), `"amount"`) || !strings.Contains(lineage.Body.String(), modelID) {
		t.Fatalf("output lineage=%d %s", lineage.Code, lineage.Body.String())
	}
	publish := workflowRequest(router, "POST", "/api/v1/datasets/"+runID+"/publish", []byte(`{}`), cookie, actor.CSRFToken, "publish-governance-output")
	if publish.Code != 200 {
		t.Fatalf("publish transformed output=%d %s", publish.Code, publish.Body.String())
	}
	// Governed outputs use the existing catalog/grant boundary for ordinary consumers.
	var datasetRecord workflow.Record
	if err := db.Where("kind='dataset' AND id=?", runID).Take(&datasetRecord).Error; err != nil {
		t.Fatal(err)
	}
	var dataset workflow.DatasetData
	if err := json.Unmarshal(datasetRecord.Body, &dataset); err != nil {
		t.Fatal(err)
	}
	resultRef := workflow.Resource{Type: "governed_dataset", ID: runID, Version: 1}
	sharing := seedSharingFixture(t, db, uid, actor.DepartmentID, resultRef, dataset.Definition.Fields)
	consumerID, consumerName := businessUser(t, service, "governance_consumer_", "output-consumer", []string{"consumer"})
	consumer, cp := login(t, router, consumerName)
	grant := workflow.GrantData{Snapshot: &sharing, ApplicationID: "output-application-" + uid, GranteeUserID: consumerID, Scope: workflow.GrantRequest{Resource: resultRef, DepartmentID: cp.DepartmentID, Purpose: "治理结果查询", Fields: []string{"id", "name"}, ExpiresAt: time.Now().Add(time.Hour)}, ApprovedBy: uid, ApprovedAt: time.Now()}
	grantID := "output-grant-" + uid
	if err := db.Create(&workflow.Record{Kind: "grant", ID: grantID, DepartmentID: cp.DepartmentID, OwnerID: consumerID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; err != nil {
		t.Fatal(err)
	}
	t.Run("AssetEvidenceFeedback", func(t *testing.T) {
		verifyGovernedAssetFeedback(t, router, cookie, actor, consumer, cp, resultRef, uid)
	})
	t.Run("DataServiceDelivery", func(t *testing.T) {
		verifyDataServiceDelivery(t, db, router, cookie, actor, consumer, cp, runID, dataset.Definition.Fields)
	})
	t.Run("LargeGovernedMount", func(t *testing.T) {
		batch := "large-mount-" + uid
		rows := make([]workflow.ImportRow, 1501)
		for i := range rows {
			rows[i] = workflow.ImportRow{BatchID: batch, RowNumber: i + 1, Data: jsonBody(t, map[string]interface{}{"id": i + 1, "name": fmt.Sprintf("person-%d", i+1)})}
		}
		if e := db.CreateInBatches(rows, 500).Error; e != nil {
			t.Fatal(e)
		}
		managed := workflow.ManagedResource{Name: "large source", DataLayer: "raw", Fields: customers, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: batch, Fields: customers})}
		if e := db.Create(&workflow.Record{Kind: "table_resource", ID: batch, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; e != nil {
			t.Fatal(e)
		}
		definition := map[string]interface{}{"name": "large model", "owner_id": uid, "steward_id": uid, "model": map[string]interface{}{"layer": "logical", "fields": []map[string]interface{}{{"name": "id", "logical_type": "integer", "primary_key": true}, {"name": "name", "logical_type": "string"}}, "contract": map[string]interface{}{"max_failure_rate": 0}}}
		model := idOf(post("models", map[string]interface{}{"definition": definition}, 201))
		post("models/"+model+"/versions/1/publish", map[string]interface{}{}, 200)
		flow := map[string]interface{}{"name": "large output", "owner_id": uid, "steward_id": uid, "dataflow": map[string]interface{}{"nodes": []map[string]interface{}{
			{"id": "source", "kind": "source", "source": map[string]interface{}{"resource": workflow.Resource{Type: "table_resource", ID: batch, Version: 1}, "fields": []string{"id", "name"}}},
			{"id": "output", "kind": "output", "inputs": []map[string]string{{"node": "source"}}, "model": workflow.GovernanceRef{ID: model, Version: 1}},
		}}}
		flowID := idOf(post("dataflows", map[string]interface{}{"definition": flow}, 201))
		post("dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
		run := idOf(post("dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}, 201))
		for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var execution workflow.GovernanceRun
			if e := db.Where("id=?", run).Take(&execution).Error; e != nil {
				t.Fatal(e)
			}
			if execution.State == "succeeded" {
				var report struct {
					Rows int `json:"output_rows"`
				}
				if e := json.Unmarshal(execution.Result, &report); e != nil || report.Rows != 1501 {
					t.Fatal("large result count differs")
				}
				break
			}
			if execution.State == "failed" || execution.State == "interrupted" || time.Now().After(deadline) {
				t.Fatalf("large execution state=%s code=%s", execution.State, execution.ErrorCode)
			}
		}
		w := workflowRequest(router, "POST", "/api/v1/datasets/"+run+"/publish", []byte(`{}`), cookie, actor.CSRFToken, "publish-large-"+uid)
		if w.Code != 200 {
			t.Fatalf("large publish=%d %s", w.Code, w.Body)
		}
		snapshot := seedSharingFixture(t, db, uid, actor.DepartmentID, workflow.Resource{Type: "governed_dataset", ID: run, Version: 1}, customers)
		w = workflowRequest(router, "POST", "/api/v1/resource-mounts", jsonBody(t, workflow.MountInput{DirectoryID: snapshot.DirectoryID, Resource: workflow.Resource{Type: "governed_dataset", ID: run, Version: 1}, Mapping: map[string]string{"id": "id", "name": "name"}}), cookie, actor.CSRFToken, "create-large-mount-"+uid)
		var mount struct {
			ID      string `json:"mount_id"`
			Version int    `json:"version"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &mount); e != nil || w.Code != 201 {
			t.Fatalf("large mount=%d %s", w.Code, w.Body)
		}
		w = workflowRequest(router, "POST", "/api/v1/resource-mounts/"+mount.ID+"/validate", jsonBody(t, workflow.VersionAction{ExpectedVersion: mount.Version}), cookie, actor.CSRFToken, "validate-large-mount-"+uid)
		var result struct {
			Definition workflow.MountRevisionData `json:"definition"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || w.Code != 200 || result.Definition.Validation == nil || !result.Definition.Validation.Passed {
			t.Fatalf("1501-row mount validation=%d %s", w.Code, w.Body)
		}
	})

	delivered := workflowRequest(router, "GET", "/delivery/v1/datasets/"+runID+"/records?contract_version=1&fields=id,name&limit=1", nil, consumer, "", "")
	if delivered.Code != 200 || strings.Contains(delivered.Body.String(), "Alice") || strings.Contains(delivered.Body.String(), `"amount"`) || !strings.Contains(delivered.Body.String(), `"id":1`) {
		t.Fatalf("derived delivery scope/masking=%d %s", delivered.Code, delivered.Body.String())
	}
	if w := workflowRequest(router, "GET", "/delivery/v1/datasets/"+runID+"/records?contract_version=1&fields=amount", nil, consumer, "", ""); w.Code != 403 {
		t.Fatalf("extra output field delivered=%d", w.Code)
	}
	if err := db.Model(&workflow.Record{}).Where("kind='grant' AND id=?", grantID).Update("state", "revoked").Error; err != nil {
		t.Fatal(err)
	}
	if w := workflowRequest(router, "GET", "/delivery/v1/datasets/"+runID+"/records?contract_version=1", nil, consumer, "", ""); w.Code != 403 {
		t.Fatalf("revoked output grant delivered=%d", w.Code)
	}
	// The bulk adapter must read the model output rather than the original unaggregated rows.
	schema := "governance_target_" + strings.ToLower(strings.ReplaceAll(uid, "-", "_"))
	if err := db.Exec("CREATE SCHEMA " + schema + "; CREATE TABLE " + schema + ".result(id bigint PRIMARY KEY,name text,amount numeric(18,2))").Error; err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	srcResponse := workflowRequest(router, "POST", "/api/v1/sources", jsonBody(t, workflow.SourceInput{Name: "治理成果交换目标", Kind: "postgres", Host: parsed.Hostname(), Port: port, Database: strings.TrimPrefix(parsed.Path, "/"), OwnerID: uid, Credentials: workflow.Credential{Username: parsed.User.Username(), Password: password}}), cookie, actor.CSRFToken, "output-target-source")
	var src struct {
		ID string `json:"source_id"`
	}
	if srcResponse.Code != 201 || json.Unmarshal(srcResponse.Body.Bytes(), &src) != nil {
		t.Fatalf("target registration=%d", srcResponse.Code)
	}
	etl := workflow.ETLDefinition{Name: "汇总成果交换", SourceResource: &resultRef, Target: workflow.TableConfig{SourceID: src.ID, Schema: schema, Table: "result"}, Mapping: []workflow.ETLMapping{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "copy"}, {Source: "amount", Target: "amount", Rule: "copy"}}}
	etlResponse := workflowRequest(router, "POST", "/api/v1/etl-tasks", jsonBody(t, etl), cookie, actor.CSRFToken, "output-etl-create")
	var etlTask struct {
		ID string `json:"id"`
	}
	if etlResponse.Code != 201 || json.Unmarshal(etlResponse.Body.Bytes(), &etlTask) != nil {
		t.Fatalf("output ETL task=%d %s", etlResponse.Code, etlResponse.Body.String())
	}
	started := workflowRequest(router, "POST", "/api/v1/etl-tasks/"+etlTask.ID+"/run", []byte(`{}`), cookie, actor.CSRFToken, "output-etl-run")
	var etlRun struct {
		ID string `json:"id"`
	}
	if started.Code != 201 || json.Unmarshal(started.Body.Bytes(), &etlRun) != nil {
		t.Fatalf("output ETL start=%d %s", started.Code, started.Body.String())
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var stored workflow.Record
		if err := db.Where("kind='etl_run' AND id=?", etlRun.ID).Take(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.State == "succeeded" {
			break
		}
		if stored.State == "failed" || stored.State == "unknown" || time.Now().After(deadline) {
			t.Fatalf("output ETL state=%s body=%s", stored.State, stored.Body)
		}
	}
	var actual struct {
		ID     int64
		Name   string
		Amount string
	}
	if err := db.Raw("SELECT id,name,amount::text AS amount FROM " + schema + ".result").Scan(&actual).Error; err != nil || actual.ID != 1 || actual.Name != "Alice" || actual.Amount != "30.00" {
		t.Fatalf("derived target mismatch=%+v err=%v", actual, err)
	}
	var controlRows int64
	if err := db.Table("governance_flow_rows").Where("run_id=?", runID).Count(&controlRows).Error; err != nil || controlRows != 0 {
		t.Fatalf("business rows must reside in Doris: rows=%d error=%v", controlRows, err)
	}
	var binding string
	if err := db.Raw("SELECT result->'storage'->>'engine' FROM governance_runs WHERE id=?", runID).Scan(&binding).Error; err != nil || binding != "doris" {
		t.Fatalf("missing Doris binding: %s %v", binding, err)
	}
	events := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"/events", nil, cookie, "", "")
	if events.Code != 200 || !strings.Contains(events.Body.String(), `"node_id":"joined"`) || !strings.Contains(events.Body.String(), "FLOW_COMPLETED") {
		t.Fatalf("node events=%d %s", events.Code, events.Body.String())
	}
	archive := workflowRequest(router, "POST", "/api/v1/governance/runs/"+runID+"/archive", []byte(`{}`), cookie, actor.CSRFToken, "flow-archive-"+uid)
	if archive.Code != 201 {
		t.Fatalf("archive=%d %s", archive.Code, archive.Body)
	}
	var exported workflow.GovernanceExport
	if e := json.Unmarshal(archive.Body.Bytes(), &exported); e != nil {
		t.Fatal(e)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(100 * time.Millisecond) {
		if e := db.Where("id=?", exported.ID).Take(&exported).Error; e != nil {
			t.Fatal(e)
		}
		if exported.State == "ready" {
			break
		}
		if exported.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("archive state=%s code=%s", exported.State, exported.ErrorCode)
		}
	}
	download := workflowRequest(router, "GET", "/api/v1/governance/exports/"+exported.ID+"/download", nil, cookie, "", "")
	if download.Code != 200 {
		t.Fatalf("download=%d %s", download.Code, download.Body)
	}
	compressed, e := gzip.NewReader(bytes.NewReader(download.Body.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	archived, e := io.ReadAll(compressed)
	compressed.Close()
	if e != nil || !bytes.Contains(archived, []byte("Alice")) || bytes.Contains(archived, []byte("Bob")) {
		t.Fatalf("archive differs from verified result: %s %v", archived, e)
	}
	var rawCount int64
	if err := db.Model(&workflow.ImportRow{}).Where("batch_id IN ?", []string{customerID, orderID}).Count(&rawCount).Error; err != nil || rawCount != 5 {
		t.Fatal("governance changed input data")
	}
}

func TestGovernanceDataflowOperators(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "flow_operators_", "flow-operators", []string{"admin"})
	cookie, actor := login(t, router, name)
	id := "operator-input-" + uid
	precision, scale := 12, 2
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "status", LogicalType: "string"}, {Name: "amount", LogicalType: "decimal", Precision: &precision, Scale: &scale}}
	rows := []string{`{"id":1,"status":"　a ","amount":"10.00"}`, `{"id":1,"status":"a","amount":"11.00"}`, `{"id":2,"status":"b","amount":"20.00"}`, `{"id":3,"status":"a","amount":"5.00"}`}
	for i, row := range rows {
		if err := db.Create(&workflow.ImportRow{BatchID: id, RowNumber: i + 1, Data: json.RawMessage(row)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	managed := workflow.ManagedResource{Name: "算子验证来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields})}
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
		t.Fatal(err)
	}
	serial := 0
	post := func(path string, body interface{}, want int) map[string]json.RawMessage {
		t.Helper()
		serial++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("operator-", serial))
		if w.Code != want {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		result := map[string]json.RawMessage{}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	idOf := func(value map[string]json.RawMessage) string {
		t.Helper()
		var id string
		if err := json.Unmarshal(value["id"], &id); err != nil || id == "" {
			t.Fatal("missing id")
		}
		return id
	}
	standard := workflow.GovernanceDefinition{Name: "客户状态标准", OwnerID: uid, StewardID: uid, Standard: &workflow.GovernanceStandard{Code: "operator.status", LogicalType: "string", Length: 8, Values: []string{"ACTIVE", "INACTIVE"}, ValueMeanings: map[string]string{"ACTIVE": "启用", "INACTIVE": "停用"}}}
	standardID := idOf(post("standards", map[string]interface{}{"definition": standard}, 201))
	post("standards/"+standardID+"/versions/1/publish", map[string]interface{}{}, 200)
	standardRef := workflow.GovernanceRef{ID: standardID, Version: 1}
	model := workflow.GovernanceDefinition{Name: "客户筛选模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "status", LogicalType: "string", Length: 8, Standard: &standardRef}, {Name: "amount", LogicalType: "decimal", Precision: 12, Scale: 2}}}}
	modelID := idOf(post("models", map[string]interface{}{"definition": model}, 201))
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{}, 200)
	modelRef := workflow.GovernanceRef{ID: modelID, Version: 1}
	edge := func(id string) []workflow.GovernanceFlowEdge { return []workflow.GovernanceFlowEdge{{Node: id}} }
	columns := []workflow.GovernanceFlowColumn{{Source: "id", Target: "id", Rule: "copy"}, {Source: "status", Target: "status", Rule: "trim"}, {Source: "amount", Target: "amount", Rule: "copy"}}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: workflow.Resource{Type: "table_resource", ID: id, Version: 1}, Fields: []string{"id", "status", "amount"}}},
		{ID: "trim", Kind: "transform", Inputs: edge("source"), Columns: columns},
		{ID: "codes", Kind: "code_map", Inputs: edge("trim"), Code: &workflow.GovernanceFlowCode{Field: "status", Standard: standardRef, Values: []workflow.GovernanceFlowCodeValue{{From: "a", To: "ACTIVE"}, {From: "b", To: "INACTIVE"}}, Unmatched: "reject"}},
		{ID: "filtered", Kind: "filter", Inputs: edge("codes"), Condition: &workflow.GovernanceFlowCondition{Field: "amount", Operator: "gte", Value: json.RawMessage("10")}},
		{ID: "dedup", Kind: "deduplicate", Inputs: edge("filtered"), Keys: []string{"id"}},
		{ID: "project", Kind: "project", Inputs: edge("dedup"), Columns: []workflow.GovernanceFlowColumn{{Source: "id", Target: "id"}, {Source: "status", Target: "status"}, {Source: "amount", Target: "amount"}}},
		{ID: "quality", Kind: "quality", Inputs: edge("project"), Checks: []workflow.Check{
			{Field: "id", Kind: "business_key", Blocking: true}, {Field: "id", Kind: "type", Blocking: true, Parameters: map[string]json.RawMessage{"type": json.RawMessage(`"integer"`)}},
			{Field: "status", Kind: "required", Blocking: true}, {Field: "status", Kind: "length", Blocking: true, Parameters: map[string]json.RawMessage{"min": json.RawMessage("1"), "max": json.RawMessage("8")}},
			{Field: "status", Kind: "enum", Blocking: true, Parameters: map[string]json.RawMessage{"values": json.RawMessage(`["ACTIVE","INACTIVE"]`)}},
			{Field: "amount", Kind: "range", Blocking: true, Parameters: map[string]json.RawMessage{"min": json.RawMessage("0"), "max": json.RawMessage("100")}},
		}},
		{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "quality", Port: "good"}}, Model: &modelRef},
	}}
	definition := workflow.GovernanceDefinition{Name: "码表筛选去重", OwnerID: uid, StewardID: uid, Dataflow: &graph}
	flowID := idOf(post("dataflows", map[string]interface{}{"definition": definition}, 201))
	post("dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{}, 200)
	wait := func(runID, state string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			var run workflow.GovernanceRun
			if err := db.Where("id=?", runID).Take(&run).Error; err != nil {
				t.Fatal(err)
			}
			if run.State == state {
				return run
			}
			if run.State == "failed" || run.State == "succeeded" {
				t.Fatalf("state=%s code=%s want=%s", run.State, run.ErrorCode, state)
			}
		}
		t.Fatal("flow did not finish")
		return workflow.GovernanceRun{}
	}
	runID := idOf(post("dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}, 201))
	wait(runID, "succeeded")
	w := workflowRequest(router, "GET", "/api/v1/governance/runs/"+runID+"/records", nil, cookie, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ACTIVE") || !strings.Contains(w.Body.String(), "INACTIVE") || !strings.Contains(w.Body.String(), "10.00") || strings.Contains(w.Body.String(), "11.00") || strings.Contains(w.Body.String(), "5.00") {
		t.Fatalf("operator outputs=%d %s", w.Code, w.Body.String())
	}
	var records struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &records); err != nil || records.Total != 2 {
		t.Fatalf("filter/dedup failed: %s", w.Body.String())
	}
	if err := db.Create(&workflow.ImportRow{BatchID: id, RowNumber: 5, Data: json.RawMessage(`{"id":4,"status":"unknown","amount":"10.00"}`)}).Error; err != nil {
		t.Fatal(err)
	}
	badID := idOf(post("dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}, 201))
	failed := wait(badID, "failed")
	if failed.ErrorCode != "GOVERNANCE_CODE_UNMATCHED" {
		t.Fatalf("unmatched code=%s", failed.ErrorCode)
	}
	var partial int64
	if err := db.Table("governance_flow_rows").Where("run_id=?", badID).Count(&partial).Error; err != nil || partial != 0 {
		t.Fatal("failed flow committed partial output")
	}
	if got := workflowRequest(router, "GET", "/api/v1/governance/runs/"+badID+"/records", nil, cookie, "", ""); got.Code != 409 {
		t.Fatalf("failed output disclosed=%d", got.Code)
	}
}

func TestGovernanceDataflowAuthorizationAndCancel(t *testing.T) {
	db, service, router := fixture(t)
	uid, name := businessUser(t, service, "flow_control_", "flow-control", []string{"admin"})
	cookie, actor := login(t, router, name)
	govID, govName := businessUser(t, service, "flow_control_gov_", "flow-control", []string{"governor"})
	govCookie, gov := login(t, router, govName)
	seedAssignment(t, db, govID, actor.DepartmentID, "govern_resource")
	id := "flow-control-input-" + uid
	if err := db.Exec("INSERT INTO resource_import_rows(batch_id,row_number,data) SELECT ?,n,jsonb_build_object('id',n,'name','restricted') FROM generate_series(1,50000) n", id).Error; err != nil {
		t.Fatal(err)
	}
	fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Sensitive: true}}
	managed := workflow.ManagedResource{Name: "治理控制来源", DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields})}
	if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
		t.Fatal(err)
	}
	ref := workflow.Resource{Type: "table_resource", ID: id, Version: 1}
	grantID := "flow-control-grant-" + uid
	grant := workflow.ProcessingAuthorization{UserID: govID, Resource: ref, Fields: []string{"id", "name"}, Purpose: "数据治理", ExpiresAt: time.Now().Add(time.Hour), ApprovalRef: "flow-test"}
	if err := db.Create(&workflow.Record{Kind: "processing_authorization", ID: grantID, DepartmentID: actor.DepartmentID, OwnerID: uid, ParentID: govID, State: "active", Version: 1, Body: jsonBody(t, grant)}).Error; err != nil {
		t.Fatal(err)
	}
	sequence := 0
	post := func(path string, body interface{}) map[string]json.RawMessage {
		t.Helper()
		sequence++
		w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("flow-control-", sequence))
		if w.Code != 201 && w.Code != 200 {
			t.Fatalf("%s=%d %s", path, w.Code, w.Body.String())
		}
		out := map[string]json.RawMessage{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	idOf := func(v map[string]json.RawMessage) string {
		var id string
		if err := json.Unmarshal(v["id"], &id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	model := workflow.GovernanceDefinition{Name: "脱敏治理模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Sensitive: true}}}}
	modelID := idOf(post("models", map[string]interface{}{"definition": model}))
	post("models/"+modelID+"/versions/1/publish", map[string]interface{}{})
	modelRef := workflow.GovernanceRef{ID: modelID, Version: 1}
	graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{{ID: "source", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: ref, Fields: []string{"id", "name"}}}, {ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "source"}}, Model: &modelRef}}}
	flowID := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "脱敏控制流程", OwnerID: uid, StewardID: uid, Dataflow: &graph}}))
	post("dataflows/"+flowID+"/versions/1/publish", map[string]interface{}{})
	path := "/api/v1/governance/dataflows/" + flowID + "/versions/1/run"
	w := workflowRequest(router, "POST", path, []byte(`{}`), govCookie, gov.CSRFToken, "no-derived-flow")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "GOVERNANCE_DERIVED_REQUIRED") {
		t.Fatalf("derivation did not need grant=%d %s", w.Code, w.Body.String())
	}
	grant.AllowDerived = true
	if err := db.Model(&workflow.Record{}).Where("kind='processing_authorization' AND id=?", grantID).Updates(map[string]interface{}{"body": jsonBody(t, grant), "version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	w = workflowRequest(router, "POST", path, []byte(`{}`), govCookie, gov.CSRFToken, "revoked-flow-run")
	if w.Code != 201 {
		t.Fatalf("enqueue=%d %s", w.Code, w.Body.String())
	}
	var queued workflow.GovernanceRun
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	waitProgress := func(runID string) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			var run workflow.GovernanceRun
			if err := db.Where("id=?", runID).Take(&run).Error; err != nil {
				t.Fatal(err)
			}
			if run.State == "running" && run.Processed > 0 && run.Processed < 50000 {
				return
			}
			if run.State == "failed" || run.State == "succeeded" {
				t.Fatalf("did not observe controlled execution: state=%s code=%s", run.State, run.ErrorCode)
			}
		}
		t.Fatal("no extraction progress")
	}
	waitTerminal := func(runID, state string) workflow.GovernanceRun {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			var run workflow.GovernanceRun
			if err := db.Where("id=?", runID).Take(&run).Error; err != nil {
				t.Fatal(err)
			}
			if run.State == state {
				return run
			}
			if run.State == "failed" || run.State == "succeeded" || run.State == "cancelled" {
				t.Fatalf("state=%s code=%s want=%s", run.State, run.ErrorCode, state)
			}
		}
		t.Fatal("flow did not terminate")
		return workflow.GovernanceRun{}
	}
	waitProgress(queued.ID)
	w = workflowRequest(router, "POST", "/api/v1/processing-authorizations/"+grantID+"/revoke", []byte(`{"expected_version":2,"reason":"验证运行期间撤销"}`), cookie, actor.CSRFToken, "revoke-running-flow")
	if w.Code != 200 {
		t.Fatalf("revoke=%d %s", w.Code, w.Body.String())
	}
	failed := waitTerminal(queued.ID, "failed")
	if failed.ErrorCode != "PROCESSING_AUTHORIZATION_REQUIRED" && failed.ErrorCode != "GOVERNANCE_AUTHORIZATION_CHANGED" {
		t.Fatalf("revoked flow failure=%s", failed.ErrorCode)
	}
	var partial int64
	if err := db.Table("governance_flow_rows").Where("run_id=?", queued.ID).Count(&partial).Error; err != nil || partial != 0 {
		t.Fatal("revoked flow committed data")
	}
	adminRunID := idOf(post("dataflows/"+flowID+"/versions/1/run", map[string]interface{}{}))
	waitProgress(adminRunID)
	w = workflowRequest(router, "POST", "/api/v1/governance/runs/"+adminRunID+"/cancel", []byte(`{}`), cookie, actor.CSRFToken, "cancel-dataflow")
	if w.Code != 200 {
		t.Fatalf("cancel=%d %s", w.Code, w.Body.String())
	}
	waitTerminal(adminRunID, "cancelled")
	if err := db.Table("governance_flow_rows").Where("run_id IN ?", []string{queued.ID, adminRunID}).Count(&partial).Error; err != nil || partial != 0 {
		t.Fatal("cancelled flow committed data")
	}
	// A small fresh observation proves that non-admin outputs keep the sensitive value masked.
	if err := db.Where("batch_id=? AND row_number>2", id).Delete(&workflow.ImportRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&workflow.Record{}).Where("kind='processing_authorization' AND id=?", grantID).Updates(map[string]interface{}{"state": "active", "version": 4, "body": jsonBody(t, grant)}).Error; err != nil {
		t.Fatal(err)
	}
	w = workflowRequest(router, "POST", path, []byte(`{}`), govCookie, gov.CSRFToken, "masked-flow-run")
	if w.Code != 201 {
		t.Fatalf("masked enqueue=%d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	waitTerminal(queued.ID, "succeeded")
	output := workflowRequest(router, "GET", "/api/v1/governance/runs/"+queued.ID+"/records", nil, govCookie, "", "")
	if output.Code != 200 || strings.Contains(output.Body.String(), "restricted") || !strings.Contains(output.Body.String(), "***") {
		t.Fatalf("sensitive output=%d %s", output.Code, output.Body.String())
	}
}
