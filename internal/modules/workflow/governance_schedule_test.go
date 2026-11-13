package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"testing"
	"time"
)

func TestGovernanceScheduleDuePolicy(t *testing.T) {
	due := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	s := GovernanceSchedule{Workflow: GovernanceRef{ID: "wf", Version: 1}, Timezone: "Asia/Shanghai", IntervalSeconds: 60, ConcurrencyPolicy: "forbid", MissedPolicy: "latest", NextRunAt: due}
	if err := validateGovernanceSchedule(s); err != nil {
		t.Fatal(err)
	}
	fire, next, ok := governanceScheduleDue(s, due.Add(190*time.Second))
	if !ok || !fire.Equal(due.Add(180*time.Second)) || !next.Equal(due.Add(240*time.Second)) {
		t.Fatalf("latest %v %v %v", fire, next, ok)
	}
	s.MissedPolicy = "skip"
	_, next, ok = governanceScheduleDue(s, due.Add(190*time.Second))
	if ok || !next.Equal(due.Add(240*time.Second)) {
		t.Fatalf("skip %v %v", next, ok)
	}
	if _, _, ok = governanceScheduleDue(s, due.Add(-time.Second)); ok {
		t.Fatal("future schedule fired")
	}
	for _, zone := range []string{"Local", "+08:00", "Mars/Olympus"} {
		s.Timezone = zone
		if validateGovernanceSchedule(s) == nil {
			t.Fatalf("zone accepted %s", zone)
		}
	}
}

func TestGovernanceScheduleDirectTarget(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		valid        bool
	}{
		{"dataflow", `"dataflow":{"id":"flow","version":2}`, true},
		{"workflow", `"workflow":{"id":"wf","version":1}`, true},
		{"both", `"workflow":{"id":"wf","version":1},"dataflow":{"id":"flow","version":2}`, false},
		{"missing", `"parameters":{}`, false},
		{"invalid dataflow version", `"dataflow":{"id":"flow","version":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var row GovernanceSchedule
			if err := json.Unmarshal([]byte(`{`+tc.target+`,"timezone":"UTC","interval_seconds":60,"concurrency_policy":"forbid","missed_policy":"skip","next_run_at":"2026-10-27T00:00:00Z"}`), &row); err != nil {
				t.Fatal(err)
			}
			if err := validateGovernanceSchedule(row); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestGovernanceSchedulePreservesSharedParameterOverride(t *testing.T) {
	def := GovernanceDefinition{Dataflow: &GovernanceDataflow{Nodes: []GovernanceFlowNode{
		{ID: "code", Kind: "script", Script: &GovernanceFlowScript{Parameters: map[string]json.RawMessage{"minimum": json.RawMessage("1")}}},
		{ID: "filter", Kind: "filter", Condition: &GovernanceFlowCondition{Field: "id", Operator: "gte", Parameter: "minimum", Value: json.RawMessage("1")}},
	}}}
	values, e := governanceScheduleParameters(nil, identity.Principal{}, "dataflow", GovernanceVersion{ExecutionMode: "batch"}, def, map[string]json.RawMessage{"minimum": json.RawMessage("2")})
	if e != nil {
		t.Fatal(e)
	}
	if string(values["minimum"]) != "2" {
		t.Fatal("scheduled override lost")
	}
}
