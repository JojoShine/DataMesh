package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGovernanceScriptContract(t *testing.T) {
	node := GovernanceFlowNode{ID: "script", Kind: "script", Script: &GovernanceFlowScript{Language: "javascript", RuntimeVersion: "javascript@sha256:" + strings.Repeat("a", 64), Source: "function transform(row){return row}", Fields: []GovernanceField{{Name: "id", LogicalType: "integer"}, {Name: "amount", LogicalType: "decimal", Precision: 20, Scale: 2}, {Name: "name", LogicalType: "string", Nullable: true}}}}
	valid := DataRow{"id": json.RawMessage(`"9223372036854775807"`), "amount": json.RawMessage(`"9999999999999999.99"`), "name": json.RawMessage(`null`)}
	if _, e := normalizeScriptRow(node, valid); e != nil {
		t.Fatal(e)
	}
	for name, changes := range map[string]DataRow{"precision": {"amount": json.RawMessage(`"1.001"`)}, "float": {"amount": json.RawMessage(`1.01`)}, "unsafe_integer": {"id": json.RawMessage(`9223372036854775807`)}, "null": {"id": json.RawMessage(`null`)}, "extra": {"unknown": json.RawMessage(`1`)}} {
		t.Run(name, func(t *testing.T) {
			row := DataRow{"id": json.RawMessage(`"1"`), "amount": json.RawMessage(`"1.00"`), "name": json.RawMessage(`null`)}
			for key, value := range changes {
				row[key] = value
			}
			if _, e := normalizeScriptRow(node, row); e == nil {
				t.Fatal("invalid script row accepted")
			}
		})
	}
	input := []governanceFlowField{{Field: Field{Name: "secret", LogicalType: "string", Sensitive: true}, Origins: []GovernanceFieldOrigin{{Node: "source", Field: "secret"}}}}
	output, e := scriptOutputFields(node, input)
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range output {
		if !field.Field.Sensitive || len(field.Origins) != 1 {
			t.Fatal("script removed sensitive lineage")
		}
	}
	node.Script.RuntimeVersion = "latest"
	if validateGovernanceScript(node.Script) == nil {
		t.Fatal("mutable runtime accepted")
	}
}
