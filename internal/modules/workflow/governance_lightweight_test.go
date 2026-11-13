package workflow

import (
	"fmt"
	"strings"
	"testing"
)

func TestLightweightCSVPreservesTextAndLabels(t *testing.T) {
	file, rows, err := parseLightweightCSV("数据.csv", []byte("\xef\xbb\xbf编号,name,column_1\r\n001,\" A, B \nC\",009\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if file.Rows != 1 || file.Fields[0].Name != "column_1_1" || file.FieldLabels["column_1_1"] != "编号" || rows[0][0] != "001" || rows[0][1] != " A, B \nC" || rows[0][2] != "009" {
		t.Fatalf("metadata=%+v rows=%q", file, rows)
	}
	for _, f := range file.Fields {
		if f.LogicalType != "string" {
			t.Fatalf("lost string typing: %+v", f)
		}
	}
}
func TestLightweightCSVRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", "a,a\n1,2\n", "a, \n1,2\n", "a,b\n1\n", "a\n1,2\n", "a,b\n\n1,2\n", "a\n\"unterminated", "a\n\xff", strings.Repeat("a,", 100) + "z\n", "a\n" + strings.Repeat("1\n", 100001), strings.Repeat("a", 20<<20+1)} {
		if _, _, err := parseLightweightCSV("input.csv", []byte(input)); err == nil {
			t.Fatalf("accepted invalid CSV len=%d", len(input))
		}
	}
}
func TestLightweightGraphRejectsExternalFeatures(t *testing.T) {
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{{ID: "source", Kind: "source", Source: &GovernanceFlowSource{Resource: Resource{Type: "file_resource", ID: "private", Version: 1}, Fields: []string{"id"}}}, {ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: "source"}}, InferModel: true}}}
	if err := validateLightweightGraph(graph); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*GovernanceDataflow){func(g *GovernanceDataflow) { g.Nodes[0].Source.Resource.Type = "table_resource" }, func(g *GovernanceDataflow) { g.Nodes[0].Source.Parameters = map[string]string{"x": "y"} }, func(g *GovernanceDataflow) { g.Nodes[1].Model = &GovernanceRef{ID: "other", Version: 1} }, func(g *GovernanceDataflow) { g.Nodes[1].Template = &GovernanceRef{ID: "other", Version: 1} }, func(g *GovernanceDataflow) { g.Nodes[1].Kind = "script" }} {
		copy, err := decode[GovernanceDataflow](encode(graph))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&copy)
		if err = validateLightweightGraph(copy); err == nil {
			t.Fatal("accepted forbidden external reference")
		}
	}
}

func TestLightweightCSVSafeIdentifiers(t *testing.T) {
	file, rows, err := parseLightweightCSV("case.csv", []byte("id,ID,备注\n001,002,说明\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(file.Fields[0].Name, file.Fields[1].Name) || rows[0][1] != "002" || file.FieldLabels[file.Fields[1].Name] != "ID" {
		t.Fatalf("unsafe physical columns: %+v", file)
	}
}
func TestLightweightEightSourcesAndNormalLimit(t *testing.T) {
	fields := []Field{{Name: "id", LogicalType: "string"}}
	graph := GovernanceDataflow{}
	inputs := map[string]governanceInput{}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("source_%d", i)
		ref := Resource{Type: "file_resource", ID: id, Version: 1}
		graph.Nodes = append(graph.Nodes, GovernanceFlowNode{ID: id, Kind: "source", Source: &GovernanceFlowSource{Resource: ref, Fields: []string{"id"}}})
		inputs[id] = governanceInput{Resource: ref, Fields: fields}
	}
	previous := "source_0"
	for i := 1; i < 8; i++ {
		id := fmt.Sprintf("join_%d", i)
		graph.Nodes = append(graph.Nodes, GovernanceFlowNode{ID: id, Kind: "join", Inputs: []GovernanceFlowEdge{{Node: previous}, {Node: fmt.Sprintf("source_%d", i)}}, Join: &GovernanceFlowJoin{Kind: "inner", LeftKeys: []string{"id"}, RightKeys: []string{"id"}, Columns: []GovernanceFlowJoinColumn{{Side: "left", Source: "id", Target: "id"}}}})
		previous = id
	}
	graph.Nodes = append(graph.Nodes, GovernanceFlowNode{ID: "output", Kind: "output", Inputs: []GovernanceFlowEdge{{Node: previous}}, InferModel: true})
	def := governanceFlowExecution{Lightweight: &governanceLightweightExecution{UnitID: "unit", Version: 1}, Pinned: []governanceFlowPinned{{Definition: GovernanceDefinition{Dataflow: &graph}}}, Inputs: inputs}
	if _, _, _, err := governanceExecutionPlan(def); err != nil {
		t.Fatal(err)
	}
	def.Lightweight = nil
	if _, _, _, err := governanceExecutionPlan(def); err == nil {
		t.Fatal("ordinary source limit widened")
	}
}

func TestLightweightRequiredChecksBlankTextOnlyInPrivatePlan(t *testing.T) {
	fields := []governanceFlowField{{Field: Field{Name: "name", LogicalType: "string", Nullable: true}}}
	node := GovernanceFlowNode{ID: "quality", Kind: "quality", Inputs: []GovernanceFlowEdge{{Node: "input"}}, Checks: []Check{{Kind: "required", Field: "name", Blocking: true}, {Kind: "business_key", Field: "name", Blocking: true}}}
	normal, _, err := governanceDorisSelect(node, governanceFlowPlan{Outputs: map[GovernanceFlowEdge][]governanceFlowField{{Node: "input"}: fields}})
	if err != nil {
		t.Fatal(err)
	}
	private, _, err := governanceDorisSelect(node, governanceFlowPlan{BlankTextMissing: true, Outputs: map[GovernanceFlowEdge][]governanceFlowField{{Node: "input"}: fields}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(normal, "NULLIF(TRIM(") || !strings.Contains(private, "NULLIF(TRIM(") {
		t.Fatalf("blank semantics not scoped: normal=%s private=%s", normal, private)
	}
}

func TestLightweightCSVPreservesEmbeddedCRLF(t *testing.T) {
	input := []byte("id,note\r\n001,\"Line 1\r\nLine \"\"2\"\"\r\nLine 3\"\r\n002,plain\r\n")
	file, rows, err := parseLightweightCSV("windows.csv", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0][1] != "Line 1\r\nLine \"2\"\r\nLine 3" || rows[1][0] != "002" || file.Size != int64(len(input)) {
		t.Fatalf("line ending drift %+v %q", file, rows)
	}
}

func TestLightweightCSVRowCapacity(t *testing.T) {
	for _, count := range []int{10001, 100000} {
		file, rows, err := parseLightweightCSV("large.csv", []byte("id\n"+strings.Repeat("001\n", count)))
		if err != nil {
			t.Fatalf("%d rows rejected: %v", count, err)
		}
		if file.Rows != count || len(rows) != count || rows[count-1][0] != "001" {
			t.Fatalf("%d rows truncated or changed", count)
		}
	}
	if _, _, err := parseLightweightCSV("over.csv", []byte("id\n"+strings.Repeat("001\n", 100001))); err == nil {
		t.Fatal("accepted more than 100000 rows")
	}
}
