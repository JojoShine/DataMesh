package workflow

import (
	"encoding/json"
	"testing"
)

func TestQualityRetestKeepsExecutionScope(t *testing.T) {
	original := governanceFlowExecution{Flow: GovernanceRef{ID: "flow", Version: 1}, Parameters: map[string]json.RawMessage{"limit": json.RawMessage(`15`)}, Inputs: map[string]governanceInput{"source": {Resource: Resource{ID: "child", Type: "governed_dataset", Version: 1}, WorkflowParent: "parent", ResourceFingerprint: "contract", SourceFingerprint: "source"}}}
	if !governanceIssueFlowMatches(original, original) {
		t.Fatal("same evidence rejected")
	}
	for name, change := range map[string]func(*governanceFlowExecution){
		"parameters": func(v *governanceFlowExecution) { v.Parameters = nil },
		"binding": func(v *governanceFlowExecution) {
			input := v.Inputs["source"]
			input.WorkflowParent = "other"
			v.Inputs["source"] = input
		},
		"contract": func(v *governanceFlowExecution) {
			input := v.Inputs["source"]
			input.ResourceFingerprint = "changed"
			v.Inputs["source"] = input
		},
		"trial": func(v *governanceFlowExecution) { v.Trial = &governanceTrial{} },
	} {
		t.Run(name, func(t *testing.T) {
			copy, e := decode[governanceFlowExecution](encode(original))
			if e != nil {
				t.Fatal(e)
			}
			change(&copy)
			if governanceIssueFlowMatches(copy, original) {
				t.Fatal("changed scope accepted")
			}
		})
	}
}

func TestMasterBulkDoesNotInferMaskedEquality(t *testing.T) {
	model := GovernanceModel{Fields: []GovernanceField{{Name: "id"}, {Name: "name", Sensitive: true}}}
	row := governanceMasterRow{Values: DataRow{"id": json.RawMessage(`"P1"`), "name": json.RawMessage(`"***"`)}}
	if _, e := masterBatchChoices([]governanceMasterRow{row}, model); e != nil {
		t.Fatal("single source should not require equality comparison", e)
	}
	if _, e := masterBatchChoices([]governanceMasterRow{row, row}, model); e == nil {
		t.Fatal("masked placeholders treated as identical originals")
	}
	model.Fields[1].Sensitive = false
	other := governanceMasterRow{Values: DataRow{"id": json.RawMessage(`"P1"`), "name": json.RawMessage(`"other"`)}}
	if _, e := masterBatchChoices([]governanceMasterRow{row, other}, model); e == nil {
		t.Fatal("conflicting values accepted")
	}
	if _, e := masterBatchChoices([]governanceMasterRow{row, row}, model); e != nil {
		t.Fatal(e)
	}
}
