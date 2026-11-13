package workflow

import "testing"

func TestGovernanceFlowModeExclusive(t *testing.T) {
	for _, tc := range []struct {
		actual, requested string
		ok                bool
	}{
		{"", "batch", true}, {"batch", "batch", true}, {"continuous", "continuous", true},
		{"batch", "continuous", false}, {"continuous", "batch", false}, {"", "continuous", false},
	} {
		if e := requireGovernanceFlowMode(tc.actual, tc.requested); (e == nil) != tc.ok {
			t.Errorf("%q -> %q: %v", tc.actual, tc.requested, e)
		}
	}
}
