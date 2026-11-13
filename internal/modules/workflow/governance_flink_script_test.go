package workflow

import (
	"strings"
	"testing"
)

func streamScriptFixture() governanceFlowExecution {
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &GovernanceFlowSource{Resource: Resource{Type: "raw_object", ID: "fixture", Version: 1}, Fields: []string{"id", "name"}}},
		{ID: "script", Kind: "script", Inputs: []GovernanceFlowEdge{{Node: "source"}}, Script: &GovernanceFlowScript{Language: "python", RuntimeVersion: "python@sha256:" + strings.Repeat("a", 64), Source: "def transform(row, context): return row", Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Nullable: true}}}},
		{ID: "output", Kind: "output", InferModel: true, Inputs: []GovernanceFlowEdge{{Node: "script"}}},
	}}
	return governanceFlowExecution{Flow: GovernanceRef{ID: "fixture", Version: 1}, Pinned: []governanceFlowPinned{{Kind: "dataflow", Ref: GovernanceRef{ID: "fixture", Version: 1}, Definition: GovernanceDefinition{Dataflow: &graph}}}, Inputs: map[string]governanceInput{"source": {Resource: Resource{Type: "raw_object", ID: "fixture", Version: 1}, Fields: []Field{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Nullable: true}}}}}
}

func TestGovernanceFlinkScriptPreservesIdentityAndNumericContract(t *testing.T) {
	id := strings.Repeat("a", 32)
	compiled, err := compileGovernanceFlink(streamScriptFixture(), map[string]string{"source": "input"}, "result", streamJobName(id, 2))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(compiled.Setup, "\n")
	for _, want := range []string{"datamesh.governance.StreamScriptFunction", "l.`_dm_key`", "'" + id + "',2,'script',l.`_dm_key`", "KEY 'id' VALUE CAST(l.`id` AS STRING)", "JSON_VALUE", "AS BIGINT) AS `id`"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing script contract %q in %s", want, sql)
		}
	}
	for _, want := range []string{"'$.id' RETURNING STRING ERROR ON EMPTY ERROR ON ERROR", "'lax $.name' RETURNING STRING NULL ON EMPTY ERROR ON ERROR"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing required/nullable extraction contract %q", want)
		}
	}
	if strings.Contains(sql, "def transform") || strings.Contains(sql, "sha256:") {
		t.Fatal("script source/runtime leaked to SQL")
	}
}

func TestGovernanceFlinkScriptRejectsUnboundJob(t *testing.T) {
	for _, name := range []string{"unbound", "datamesh-stream-" + strings.Repeat("a", 32) + "-0", "datamesh-stream-" + strings.Repeat("a", 32) + "-1-extra", "datamesh-stream-" + strings.Repeat("a", 32) + "-2147483648"} {
		if _, err := compileGovernanceFlink(streamScriptFixture(), map[string]string{"source": "input"}, "result", name); err == nil {
			t.Errorf("accepted unbound script job %q", name)
		}
	}
	if _, err := compileGovernanceFlink(streamScriptFixture(), map[string]string{"source": "input"}, "result", "plan"); err != nil {
		t.Fatal(err)
	}
}

func TestGovernanceFlinkScriptRejectsReservedOutput(t *testing.T) {
	def := streamScriptFixture()
	def.Pinned[0].Definition.Dataflow.Nodes[1].Script.Fields[0].Name = "_dm_key"
	if _, err := compileGovernanceFlink(def, map[string]string{"source": "input"}, "result", streamJobName(strings.Repeat("a", 32), 1)); err == nil {
		t.Fatal("reserved script output can overwrite stream identity")
	}
}

func TestGovernanceFlinkPreservesDecimalAndMicrosecondSerialization(t *testing.T) {
	precision, scale := 30, 6
	fields := []governanceFlowField{{Field: Field{Name: "amount", LogicalType: "decimal", Precision: &precision, Scale: &scale}}, {Field: Field{Name: "stamp", LogicalType: "datetime"}}}
	for _, payload := range []string{flinkJSON(fields, "l"), flinkScriptJSON(fields)} {
		if !strings.Contains(payload, "KEY 'amount' VALUE CAST(l.`amount` AS STRING)") {
			t.Fatal("decimal JSON must remain textual through Doris JSON ingestion")
		}
		if !strings.Contains(payload, "DATE_FORMAT(l.`stamp`, 'yyyy-MM-dd''T''HH:mm:ss.SSSSSS')") {
			t.Fatal("timestamp JSON must retain six fractional digits")
		}
	}
	cast, err := flinkCast("value", Field{LogicalType: "datetime"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cast, "TO_TIMESTAMP") || !strings.Contains(cast, "AS TIMESTAMP(6)") {
		t.Fatal("datetime cast must explicitly preserve six fractional digits")
	}
}

func TestGovernanceFlinkScriptGuardsDeletedSourceRows(t *testing.T) {
	compiled, err := compileGovernanceFlink(streamScriptFixture(), map[string]string{"source": "input"}, "result", streamJobName(strings.Repeat("a", 32), 1))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(compiled.Setup, "\n")
	for _, want := range []string{"CASE WHEN `_dm_deleted` THEN CAST(NULL AS STRING) ELSE `_dm_key` END AS `_dm_key`", "CASE WHEN l.`_dm_key` IS NULL THEN CAST(NULL AS STRING) ELSE `dm_stream_script`("} {
		if !strings.Contains(sql, want) {
			t.Fatalf("deleted rows can reach the required-field script bridge: missing %q", want)
		}
	}
}
