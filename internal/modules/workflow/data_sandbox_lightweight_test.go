package workflow

import (
	"datamesh/internal/modules/identity"
	"testing"
)

func TestLightweightVisitorScope(t *testing.T) {
	first := identity.Principal{FullExperience: true, CSRFToken: "first"}
	second := identity.Principal{FullExperience: true, CSRFToken: "second"}
	scope := sandboxVisitorScope(first)
	if !lightweightScopeMatches(first, scope) {
		t.Fatal("own session denied")
	}
	if lightweightScopeMatches(second, scope) {
		t.Fatal("another session authorized")
	}
	if lightweightScopeMatches(first, "") {
		t.Fatal("legacy unscoped file authorized")
	}
	if lightweightScopeMatches(identity.Principal{FullExperience: true}, "") {
		t.Fatal("missing session authorized")
	}
	ordinary := identity.Principal{CSRFToken: "first"}
	if !lightweightScopeMatches(ordinary, "") {
		t.Fatal("ordinary file denied")
	}
	ordinary.CSRFToken = "second"
	if !lightweightScopeMatches(ordinary, "") {
		t.Fatal("ordinary login compatibility broken")
	}
	if lightweightScopeMatches(ordinary, scope) {
		t.Fatal("visitor object authorized outside visitor session")
	}
}
