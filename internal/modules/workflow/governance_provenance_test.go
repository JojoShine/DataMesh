package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGovernanceTreatmentSummaryDoesNotExposeRuleValues(t *testing.T) {
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{
		{ID: "source", Kind: "source"},
		{ID: "clean", Kind: "transform", Columns: []GovernanceFlowColumn{{Source: "name", Target: "name", Rule: "trim"}}},
		{ID: "filter", Kind: "filter", Condition: &GovernanceFlowCondition{Field: "id", Operator: "eq", Value: json.RawMessage(`"private-condition"`)}},
		{ID: "quality", Kind: "quality", Checks: []Check{{Kind: "required", Field: "id", Blocking: true}}},
		{ID: "output", Kind: "output"},
	}}
	steps := governanceTreatmentSummary(graph)
	if len(steps) != 3 || steps[0].Name != "清理文字" || steps[1].Name != "筛选记录" || steps[2].Name != "检查数据质量" {
		t.Fatalf("unexpected business summary: %+v", steps)
	}
	if strings.Contains(string(encode(steps)), "private-condition") || !strings.Contains(steps[0].Description, "首尾空白") {
		t.Fatalf("summary must explain operations without private predicate values: %+v", steps)
	}
}

func TestGovernanceOutputDepartmentUsesImmutableSingleSourceProvenance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proof *GovernanceProvenance
		want  string
	}{
		{"legacy", nil, "governance-dept"},
		{"single source", &GovernanceProvenance{SingleSource: true, Source: &Resource{Type: "table_resource", ID: "original", Version: 1}, ProviderDepartmentID: "provider-dept"}, "provider-dept"},
		{"multiple sources", &GovernanceProvenance{ProviderDepartmentID: "provider-dept"}, "governance-dept"},
		{"missing provider", &GovernanceProvenance{SingleSource: true}, "governance-dept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := governanceOutputDepartment("governance-dept", tc.proof); got != tc.want {
				t.Fatalf("department=%s want=%s", got, tc.want)
			}
		})
	}
}
