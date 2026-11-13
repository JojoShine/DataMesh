package workflow

import "testing"

func TestGovernanceWorkflowValidation(t *testing.T) {
	ref := GovernanceRef{ID: "flow", Version: 2}
	graph := GovernanceWorkflow{Tasks: []GovernanceWorkflowTask{{ID: "a", Dataflow: ref}, {ID: "b", Dataflow: ref, DependsOn: []string{"a"}, Condition: "all_failure"}, {ID: "c", Dataflow: ref, DependsOn: []string{"a"}, Condition: "all_success"}}}
	if err := validateGovernanceWorkflow(graph); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cycle", "missing", "duplicate", "version", "condition", "repeated_dependency"} {
		t.Run(name, func(t *testing.T) {
			bad, _ := decode[GovernanceWorkflow](encode(graph))
			switch name {
			case "cycle":
				bad.Tasks[0].DependsOn = []string{"b"}
			case "missing":
				bad.Tasks[1].DependsOn = []string{"absent"}
			case "duplicate":
				bad.Tasks[1].ID = "a"
			case "version":
				bad.Tasks[0].Dataflow.Version = 0
			case "condition":
				bad.Tasks[1].Condition = "expression"
			case "repeated_dependency":
				bad.Tasks[1].DependsOn = []string{"a", "a"}
			}
			if validateGovernanceWorkflow(bad) == nil {
				t.Fatal("unsafe workflow accepted")
			}
		})
	}
}
func TestGovernanceWorkflowConditions(t *testing.T) {
	for _, v := range []struct {
		condition   string
		states      map[string]string
		ready, skip bool
	}{
		{"all_success", map[string]string{"a": "running"}, false, false},
		{"all_success", map[string]string{"a": "succeeded"}, true, false},
		{"all_success", map[string]string{"a": "failed"}, false, true},
		{"all_failure", map[string]string{"a": "failed"}, true, false},
		{"all_failure", map[string]string{"a": "succeeded"}, false, true},
		{"all_done", map[string]string{"a": "cancelled"}, true, false},
		{"all_failure", map[string]string{"a": "skipped"}, false, true},
	} {
		ready, skip := governanceWorkflowReady(GovernanceWorkflowTask{DependsOn: []string{"a"}, Condition: v.condition}, v.states)
		if ready != v.ready || skip != v.skip {
			t.Fatalf("%s %v => %v %v", v.condition, v.states, ready, skip)
		}
	}
}
