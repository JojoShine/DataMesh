package workflow

import (
	"testing"
	"time"
)

func TestMonitorEvaluationAndUnknownEvidence(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)
	dups := int64(2)
	policy := GovernanceMonitorPolicy{TimeField: "updated", FreshnessSeconds: 3600, MaxNullRate: .1, MaxDuplicateRate: .1, MaxVolumeChange: .2, MaxDistributionChange: .2}
	baseline := ProfileReport{Coverage: "full", Rows: 10, TotalRows: 10, Fields: []ProfileField{{Name: "value", Frequencies: []ProfileFrequency{{Value: "A", Count: 10}}}}}
	businessTime := now.Add(-2 * time.Hour)
	current := ProfileReport{BusinessTime: &businessTime, BusinessTimeStatus: "verified", Coverage: "full", Rows: 20, TotalRows: 20, Fields: []ProfileField{{Name: "value", NullCount: 4, DuplicateCount: &dups, Frequencies: []ProfileFrequency{{Value: "B", Count: 16}}}, {Name: "updated", Max: &old}}}
	result := evaluateMonitor(policy, current, &baseline, now)
	if result.FreshnessStatus != "stale" || result.VolumeChange == nil || *result.VolumeChange != 1 || result.Fields[0].DistributionChange == nil || *result.Fields[0].DistributionChange < .99 {
		t.Fatalf("wrong evidence %+v", result)
	}
	current.Fields[0].Redacted = true
	current.Fields[1].Redacted = true
	current.BusinessTime = nil
	current.BusinessTimeStatus = "redacted"
	result = evaluateMonitor(policy, current, &baseline, now)
	if result.FreshnessStatus != "unknown" || result.Fields[0].DistributionChange != nil || result.Fields[0].DistributionStatus != "redacted" {
		t.Fatal("sensitive or missing evidence inferred")
	}
	current.Fields[0].Redacted = false
	current.Fields[0].Frequencies = []ProfileFrequency{{Value: "B", Count: 1}}
	result = evaluateMonitor(policy, current, &baseline, now)
	if result.Fields[0].DistributionChange != nil || result.Fields[0].DistributionStatus != "incomplete" {
		t.Fatal("top values misrepresented as full distribution")
	}
}
func TestMonitorPolicyBoundary(t *testing.T) {
	p := GovernanceMonitorPolicy{Resource: Resource{Type: "table_resource", ID: "source", Version: 1}, Fields: []string{"time"}, TimeField: "time", FreshnessSeconds: 3600, MaxVolumeChange: .2}
	if e := validateMonitorPolicy(p); e != nil {
		t.Fatal(e)
	}
	p.Fields = []string{"time", "time"}
	if validateMonitorPolicy(p) == nil {
		t.Fatal("duplicate fields accepted")
	}
	p.Fields = []string{"other"}
	if validateMonitorPolicy(p) == nil {
		t.Fatal("unselected business time accepted")
	}
}
