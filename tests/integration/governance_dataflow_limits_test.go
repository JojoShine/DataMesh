package integration

import (
	"datamesh/internal/modules/workflow"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGovernanceDataflowJoinBudget(t *testing.T) {
	for _, scenario := range []string{"rows", "bytes"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("ETL_MAX_ROWS", "1000")
			t.Setenv("ETL_MAX_BYTES", "1048576")
			db, service, router := fixture(t)
			uid, name := businessUser(t, service, "join_limit_", "join-budget", []string{"admin"})
			cookie, actor := login(t, router, name)
			count, payload := 33, "x"
			if scenario == "bytes" {
				count = 10
				payload = strings.Repeat("x", 16384)
			}
			fields := []workflow.Field{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "group_id", LogicalType: "integer"}, {Name: "payload", LogicalType: "string"}}
			refs := []workflow.Resource{}
			for _, side := range []string{"left", "right"} {
				id := side + "-" + uid
				if err := db.Exec("INSERT INTO resource_import_rows(batch_id,row_number,data) SELECT ?,n,jsonb_build_object('id',n,'group_id',1,'payload',?::text) FROM generate_series(1,?) n", id, payload, count).Error; err != nil {
					t.Fatal(err)
				}
				managed := workflow.ManagedResource{Name: side, DataLayer: "raw", Fields: fields, Adapter: "excel", Config: jsonBody(t, workflow.TableConfig{BatchID: id, Fields: fields})}
				if err := db.Create(&workflow.Record{Kind: "table_resource", ID: id, DepartmentID: actor.DepartmentID, OwnerID: uid, State: "available", Version: 1, Body: jsonBody(t, managed)}).Error; err != nil {
					t.Fatal(err)
				}
				refs = append(refs, workflow.Resource{Type: "table_resource", ID: id, Version: 1})
			}
			serial := 0
			post := func(path string, body interface{}) map[string]json.RawMessage {
				t.Helper()
				serial++
				w := workflowRequest(router, "POST", "/api/v1/governance/"+path, jsonBody(t, body), cookie, actor.CSRFToken, fmt.Sprint("budget-", serial))
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
			model := workflow.GovernanceDefinition{Name: "关联预算模型", OwnerID: uid, StewardID: uid, Model: &workflow.GovernanceModel{Layer: "logical", Fields: []workflow.GovernanceField{{Name: "left_id", LogicalType: "integer", PrimaryKey: true}, {Name: "right_id", LogicalType: "integer", PrimaryKey: true}, {Name: "left_payload", LogicalType: "string"}, {Name: "right_payload", LogicalType: "string"}}}}
			mid := idOf(post("models", map[string]interface{}{"definition": model}))
			post("models/"+mid+"/versions/1/publish", map[string]interface{}{})
			modelRef := workflow.GovernanceRef{ID: mid, Version: 1}
			graph := workflow.GovernanceDataflow{Nodes: []workflow.GovernanceFlowNode{
				{ID: "left", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: refs[0], Fields: []string{"id", "group_id", "payload"}}},
				{ID: "right", Kind: "source", Source: &workflow.GovernanceFlowSource{Resource: refs[1], Fields: []string{"id", "group_id", "payload"}}},
				{ID: "join", Kind: "join", Inputs: []workflow.GovernanceFlowEdge{{Node: "left"}, {Node: "right"}}, Join: &workflow.GovernanceFlowJoin{Kind: "inner", LeftKeys: []string{"group_id"}, RightKeys: []string{"group_id"}, Columns: []workflow.GovernanceFlowJoinColumn{{Side: "left", Source: "id", Target: "left_id"}, {Side: "right", Source: "id", Target: "right_id"}, {Side: "left", Source: "payload", Target: "left_payload"}, {Side: "right", Source: "payload", Target: "right_payload"}}}},
				{ID: "output", Kind: "output", Inputs: []workflow.GovernanceFlowEdge{{Node: "join"}}, Model: &modelRef},
			}}
			fid := idOf(post("dataflows", map[string]interface{}{"definition": workflow.GovernanceDefinition{Name: "受控关联", OwnerID: uid, StewardID: uid, Dataflow: &graph}}))
			post("dataflows/"+fid+"/versions/1/publish", map[string]interface{}{})
			runID := idOf(post("dataflows/"+fid+"/versions/1/run", map[string]interface{}{}))
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(25 * time.Millisecond) {
				var run workflow.GovernanceRun
				if err := db.Where("id=?", runID).Take(&run).Error; err != nil {
					t.Fatal(err)
				}
				if run.State == "failed" {
					if run.ErrorCode != "GOVERNANCE_JOIN_LIMIT" {
						t.Fatalf("join materialized before budget check: code=%s", run.ErrorCode)
					}
					break
				}
				if run.State == "succeeded" || time.Now().After(deadline) {
					t.Fatalf("join budget not enforced: state=%s", run.State)
				}
			}
			var partial int64
			if err := db.Table("governance_flow_rows").Where("run_id=?", runID).Count(&partial).Error; err != nil || partial != 0 {
				t.Fatal("over-budget join exposed output")
			}
		})
	}
}
