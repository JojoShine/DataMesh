package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGovernanceDataflowPlan(t *testing.T) {
	customer := Resource{Type: "table_resource", ID: "customers", Version: 1}
	orders := Resource{Type: "table_resource", ID: "orders", Version: 1}
	modelRef := GovernanceRef{ID: "customer_model", Version: 2}
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{
		{ID: "customers", Kind: "source", Source: &GovernanceFlowSource{Resource: customer, Fields: []string{"id", "name"}}},
		{ID: "orders", Kind: "source", Source: &GovernanceFlowSource{Resource: orders, Fields: []string{"customer_id", "amount"}}},
		{ID: "joined", Kind: "join", Inputs: []GovernanceFlowEdge{{Node: "customers"}, {Node: "orders"}}, Join: &GovernanceFlowJoin{Kind: "left", LeftKeys: []string{"id"}, RightKeys: []string{"customer_id"}, Columns: []GovernanceFlowJoinColumn{{Side: "left", Source: "id", Target: "id"}, {Side: "left", Source: "name", Target: "name"}, {Side: "right", Source: "amount", Target: "amount"}}}},
		{ID: "clean", Kind: "transform", Inputs: []GovernanceFlowEdge{{Node: "joined"}}, Columns: []GovernanceFlowColumn{{Source: "id", Target: "id", Rule: "copy"}, {Source: "name", Target: "name", Rule: "trim"}, {Source: "amount", Target: "amount", Rule: "copy"}}},
		{ID: "totals", Kind: "aggregate", Inputs: []GovernanceFlowEdge{{Node: "clean"}}, GroupBy: []string{"id", "name"}, Aggregates: []GovernanceFlowAggregate{{Operation: "sum", Source: "amount", Target: "amount"}}},
		{ID: "quality", Kind: "quality", Inputs: []GovernanceFlowEdge{{Node: "totals"}}, Checks: []Check{{Kind: "required", Field: "amount", Blocking: true}}},
		{ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "quality", Port: "good"}}, Model: &modelRef},
	}}
	schemas := map[string][]Field{"customers": {{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Nullable: true, Sensitive: true}}, "orders": {{Name: "customer_id", LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal"}}}
	models := map[GovernanceRef]GovernanceModel{modelRef: {Layer: "logical", Fields: []GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "name", LogicalType: "string", Nullable: true, Sensitive: true}, {Name: "amount", LogicalType: "decimal", Precision: 18, Scale: 2}}}}
	plan, err := compileGovernanceDataflow(graph, schemas, models, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Order) != 7 || plan.Order[6] != "output" {
		t.Fatalf("wrong topological order: %v", plan.Order)
	}
	output := plan.Outputs[GovernanceFlowEdge{Node: "output"}]
	if len(output) != 3 || !output[1].Field.Sensitive || output[2].Field.Nullable {
		t.Fatalf("lost sensitivity or quality gate: %+v", output)
	}
	if len(output[2].Origins) != 1 || output[2].Origins[0].Resource != orders || output[2].Origins[0].Field != "amount" {
		t.Fatalf("aggregate lineage=%+v", output[2].Origins)
	}
	// Left joins introduce nulls; the good quality port narrows only explicitly required fields.
	if !plan.Outputs[GovernanceFlowEdge{Node: "joined"}][2].Field.Nullable || !plan.Outputs[GovernanceFlowEdge{Node: "quality", Port: "bad"}][2].Field.Nullable {
		t.Fatal("nullable join facts erased on bad branch")
	}
	for _, scenario := range []string{"cycle", "missing", "duplicate", "lower_sensitivity", "wrong_key_type", "unused", "quality_port", "quality_bad_output", "quality_bad_indirect"} {
		t.Run(scenario, func(t *testing.T) {
			bad, err := decode[GovernanceDataflow](encode(graph))
			if err != nil {
				t.Fatal(err)
			}
			currentModels := map[GovernanceRef]GovernanceModel{}
			for k, v := range models {
				v.Fields = append([]GovernanceField{}, v.Fields...)
				currentModels[k] = v
			}
			currentSchemas := map[string][]Field{}
			for k, v := range schemas {
				currentSchemas[k] = append([]Field{}, v...)
			}
			switch scenario {
			case "cycle":
				bad.Nodes[2].Inputs[0] = GovernanceFlowEdge{Node: "output"}
			case "missing":
				bad.Nodes[3].Inputs[0].Node = "absent"
			case "duplicate":
				bad.Nodes[1].ID = "customers"
			case "lower_sensitivity":
				model := currentModels[modelRef]
				model.Fields[1].Sensitive = false
				currentModels[modelRef] = model
			case "wrong_key_type":
				currentSchemas["orders"][0].LogicalType = "string"
			case "unused":
				bad.Nodes = append(bad.Nodes, GovernanceFlowNode{ID: "unused", Kind: "source", Source: &GovernanceFlowSource{Resource: customer, Fields: []string{"id"}}})
				currentSchemas["unused"] = schemas["customers"]
			case "quality_port":
				bad.Nodes[6].Inputs[0].Port = "data"
			case "quality_bad_output", "quality_bad_indirect":
				model := currentModels[modelRef]
				model.Fields[2].Nullable = true
				currentModels[modelRef] = model
				bad.Nodes[6].Inputs[0].Port = "bad"
				if scenario == "quality_bad_indirect" {
					bad.Nodes = append(bad.Nodes, GovernanceFlowNode{ID: "badcopy", Kind: "project", Inputs: []GovernanceFlowEdge{{Node: "quality", Port: "bad"}}, Columns: []GovernanceFlowColumn{{Source: "id", Target: "id"}, {Source: "name", Target: "name"}, {Source: "amount", Target: "amount"}}})
					bad.Nodes[6].Inputs = []GovernanceFlowEdge{{Node: "badcopy"}}
				}

			}
			if _, err := compileGovernanceDataflow(bad, currentSchemas, currentModels, nil); err == nil {
				t.Fatal("unsafe graph accepted")
			}
		})
	}
}

func TestGovernanceDataflowRejectsUnimplementedOrInvalidOperators(t *testing.T) {
	schema := map[string][]Field{"input": {{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Nullable: true}}}
	source := GovernanceFlowNode{ID: "input", Kind: "source", Source: &GovernanceFlowSource{Resource: Resource{Type: "table_resource", ID: "rows", Version: 1}, Fields: []string{"id", "name"}}}
	modelRef := GovernanceRef{ID: "model", Version: 1}
	models := map[GovernanceRef]GovernanceModel{modelRef: {Layer: "logical", Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}}}}
	for _, node := range []GovernanceFlowNode{
		{ID: "step", Kind: "sql"},
		{ID: "step", Kind: "transform", Columns: []GovernanceFlowColumn{{Source: "id", Target: "id", Rule: "trim"}}},
		{ID: "step", Kind: "project", Columns: []GovernanceFlowColumn{{Source: "name", Target: "id"}, {Source: "id", Target: "id"}}},
		{ID: "step", Kind: "deduplicate", Keys: []string{"missing"}},
		{ID: "step", Kind: "filter", Condition: &GovernanceFlowCondition{Field: "id", Operator: "eq", Value: encode("1;DROP TABLE users")}},
		{ID: "step", Kind: "aggregate", Aggregates: []GovernanceFlowAggregate{{Operation: "avg", Source: "name", Target: "id"}}},
	} {
		t.Run(node.Kind+strings.Join(node.Keys, ","), func(t *testing.T) {
			node.Inputs = []GovernanceFlowEdge{{Node: "input"}}
			graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{source, node, {ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "step"}}, Model: &modelRef}}}
			if _, err := compileGovernanceDataflow(graph, schema, models, nil); err == nil {
				t.Fatal("invalid or unavailable node accepted")
			}
		})
	}
}

func TestGovernanceModeledOutputValues(t *testing.T) {
	model := GovernanceModel{Layer: "logical", Fields: []GovernanceField{{Name: "id", LogicalType: "integer", PrimaryKey: true}, {Name: "amount", LogicalType: "decimal", Precision: 5, Scale: 2}, {Name: "enabled", LogicalType: "boolean"}}}
	row := DataRow{"id": encode("9223372036854775807"), "amount": encode("1.0"), "enabled": encode("true")}
	if err := validateGovernanceOutputRow(row, model, nil); err != nil {
		t.Fatal(err)
	}
	if string(row["id"]) != "9223372036854775807" || string(row["amount"]) != `"1.00"` || string(row["enabled"]) != "true" {
		t.Fatalf("modeled values not canonical: %s", encode(row))
	}
	for _, scenario := range []struct {
		field string
		value string
	}{{"id", "9223372036854775808"}, {"id", "1.5"}, {"amount", "1000.00"}, {"amount", "0.001"}, {"enabled", "not-a-boolean"}} {
		t.Run(scenario.field+scenario.value, func(t *testing.T) {
			candidate := DataRow{"id": encode("1"), "amount": encode("1.00"), "enabled": encode(true)}
			candidate[scenario.field] = encode(scenario.value)
			if err := validateGovernanceOutputRow(candidate, model, nil); err == nil {
				t.Fatal("lossy or invalid modeled value accepted")
			}
		})
	}
}

func TestGovernanceFlowQualityExactBoundsAndBusinessKey(t *testing.T) {
	input := []governanceFlowField{{Field: Field{Name: "amount", LogicalType: "decimal"}}, {Field: Field{Name: "business_id", LogicalType: "string", Nullable: true}}}
	node := GovernanceFlowNode{ID: "quality", Kind: "quality", Inputs: []GovernanceFlowEdge{{Node: "input"}}, Checks: []Check{{Field: "amount", Kind: "range", Blocking: true, Parameters: map[string]json.RawMessage{"min": encode("0.123456789012345678"), "max": encode("999999999999999999.123456789012345678")}}, {Field: "business_id", Kind: "business_key", Blocking: true}}}
	if _, err := compileGovernanceFlowNode(node, [][]governanceFlowField{input}, nil, nil, nil); err != nil {
		t.Fatalf("explicit business key/exact numeric bounds rejected: %v", err)
	}
	for _, raw := range []json.RawMessage{encode("1e10000000"), encode("0;DROP TABLE users"), encode(strings.Repeat("9", 129))} {
		node.Checks[0].Parameters["min"] = raw
		if _, err := compileGovernanceFlowNode(node, [][]governanceFlowField{input}, nil, nil, nil); err == nil {
			t.Fatal("unbounded or non-numeric quality bound accepted")
		}
	}
}

func TestGovernanceResultFirstOutput(t *testing.T) {
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &GovernanceFlowSource{Resource: Resource{Type: "table_resource", ID: "source", Version: 1}, Fields: []string{"id"}}},
		{ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "source"}}, InferModel: true},
	}}
	schemas := map[string][]Field{"source": {{Name: "id", LogicalType: "integer", PrimaryKey: true, Sensitive: true}}}
	plan, err := compileGovernanceDataflow(graph, schemas, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := plan.Outputs[GovernanceFlowEdge{Node: "output"}]
	model := governanceResultModel(fields)
	if len(model.Fields) != 1 || model.Fields[0].PrimaryKey || !model.Fields[0].Nullable || !model.Fields[0].Sensitive {
		t.Fatal("inferred model must not assume business keys or required values, and must preserve sensitivity")
	}
	graph.Nodes[1].Model = &GovernanceRef{ID: "model", Version: 1}
	if _, err = compileGovernanceDataflow(graph, schemas, nil, nil); err == nil {
		t.Fatal("both output modes accepted")
	}
	graph.Nodes[1].Model = nil
	graph.Nodes[1].InferModel = false
	if _, err = compileGovernanceDataflow(graph, schemas, nil, nil); err == nil {
		t.Fatal("missing explicit output mode accepted")
	}
}
