package domain

import "testing"

func TestIdentityIsDeterministicAndPartBoundariesMatter(t *testing.T) {
	a := ID("session", "cursor", "revision")
	if a != ID("session", "cursor", "revision") || len(a) != 32 {
		t.Fatal("unstable persisted identity")
	}
	if ID("ab", "c") == ID("a", "bc") {
		t.Fatal("concatenated identities are ambiguous")
	}
	if ID("project", "one") == ID("session", "one") {
		t.Fatal("identity namespaces collide")
	}
}
