package identity

import (
	"testing"
	"time"
)

func TestExperienceLimitBoundedAndReset(t *testing.T) {
	var limit experienceLimits
	for i := 0; i < 60; i++ {
		if !limit.allow("public-user", 60) {
			t.Fatal("legitimate window rejected")
		}
	}
	if limit.allow("public-user", 60) {
		t.Fatal("account quota bypassed")
	}
	if !limit.allow("another-user", 60) {
		t.Fatal("unrelated identities share quota")
	}
	limit.buckets["public-user"] = experienceWindow{Start: time.Now().Add(-2 * time.Minute), Count: 60}
	if !limit.allow("public-user", 60) {
		t.Fatal("expired window did not reset")
	}
}
