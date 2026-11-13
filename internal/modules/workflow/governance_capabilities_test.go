package workflow

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCapabilityContracts(t *testing.T) {
	metric := GovernanceMetric{Source: GovernanceFlowSource{Resource: Resource{Type: "table_resource", ID: "rows", Version: 1}, Fields: []string{"id", "amount", "updated"}}, Dimensions: []string{"id"}, Period: "range", TimeField: "updated", From: "2026-01-01T00:00:00+08:00", Until: "2027-01-01T00:00:00+08:00", Aggregates: []GovernanceFlowAggregate{{Operation: "sum", Source: "amount", Target: "total"}}}
	graph, e := metricGraph(metric)
	if e != nil || len(graph.Nodes) != 5 || graph.Nodes[1].Condition.Operator != "gte" || graph.Nodes[2].Condition.Operator != "lt" || graph.Nodes[4].DataLayer != "dws" {
		t.Fatalf("incorrect fixed metric graph: %+v %v", graph, e)
	}
	metric.TimeType = "date"
	metric.From = "2026-01-01"
	metric.Until = "2027-01-01"
	if _, e = metricGraph(metric); e != nil {
		t.Fatal("date window rejected")
	}
	metric.TimeType = ""
	metric.From = "2026-01-01T00:00:00Z"
	metric.Until = metric.From
	if _, e = metricGraph(metric); e == nil {
		t.Fatal("empty interval accepted")
	}
	metric.Period = "all_time"
	if _, e = metricGraph(metric); e == nil {
		t.Fatal("ignored time filter accepted")
	}
	def := GovernanceMaster{Keys: []string{"id"}, Model: GovernanceModel{Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string"}}}}
	numericKey, missing, e := masterMatchKey(def, DataRow{"id": json.RawMessage(`1`)})
	if e != nil || missing {
		t.Fatal("valid numeric key rejected")
	}
	stringKey, missing, e := masterMatchKey(def, DataRow{"id": json.RawMessage(`"1"`)})
	if e != nil || missing || string(encode(numericKey)) != string(encode(stringKey)) {
		t.Fatal("typed equivalent keys did not match")
	}

	rows := []governanceMasterRow{{Source: "registration", RecordID: "1", Values: DataRow{"id": json.RawMessage(`1`), "name": json.RawMessage(`"甲"`)}}, {Source: "business", RecordID: "88", Values: DataRow{"id": json.RawMessage(`1`), "name": json.RawMessage(`"乙"`)}}}
	conflicts := masterConflicts(rows, def.Model.Fields)
	if len(conflicts) != 1 || conflicts[0] != "name" {
		t.Fatalf("incorrect conflicts %+v", conflicts)
	}
	selected, e := masterSelection(rows, def, map[string]int{"id": 0, "name": 1})
	if e != nil || string(selected["name"]) != `"乙"` {
		t.Fatal("source choice lost")
	}
	for _, choices := range []map[string]int{{"id": 0}, {"id": 0, "name": 2}, {"id": 0, "name": -1}, {"id": 0, "name": 1, "injected": 0}} {
		if _, e = masterSelection(rows, def, choices); e == nil {
			t.Fatal("invalid field choice accepted")
		}
	}
}
func TestCapabilityReadContextAndResponse(t *testing.T) {
	original := context.WithValue(context.Background(), governanceWorkspaceContextKey{}, governanceWorkspaceScope{ID: "space", Internal: true, Mutation: true})
	read := governanceReadContext(original)
	if workspaceScope(read).Mutation || workspaceScope(read).ID != "space" || !workspaceScope(read).Internal || !workspaceScope(original).Mutation {
		t.Fatal("authorization context lost isolation or retained transaction locks")
	}
	var response map[string]json.RawMessage
	if e := json.Unmarshal(governanceCapabilityRecord(Record{ID: "request", State: "submitted", Version: 1, Body: json.RawMessage(`{"reason":"调整口径"}`)}), &response); e != nil {
		t.Fatal(e)
	}
	if string(response["id"]) != `"request"` || string(response["version"]) != "1" || response["body"] == nil {
		t.Fatal("capability response differs from frontend contract")
	}
}

func TestScenarioPreservesInputProtectionAndContract(t *testing.T) {
	expected := []Field{{Name: "name", LogicalType: "string", PrimaryKey: true, Sensitive: true}}
	if !governanceScenarioFieldsCompatible(expected, expected) {
		t.Fatal("same contract rejected")
	}
	for _, field := range []Field{{Name: "name", LogicalType: "string", PrimaryKey: true}, {Name: "name", LogicalType: "string", PrimaryKey: true, Sensitive: true, Nullable: true}, {Name: "name", LogicalType: "string", Sensitive: true}} {
		if governanceScenarioFieldsCompatible([]Field{field}, expected) {
			t.Fatal("weaker input protection or constraints accepted")
		}
	}
}
