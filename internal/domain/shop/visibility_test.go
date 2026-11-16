package shop

import "testing"

func TestDefaultIDUsesEarliestActiveGrant(t *testing.T) {
	shops := []Visible{
		{PublicID: "b", Status: "active", GrantedAtUnix: 20},
		{PublicID: "a", Status: "active", GrantedAtUnix: 10},
		{PublicID: "c", Status: "inactive", GrantedAtUnix: 1},
	}
	if got := DefaultID(shops); got != "a" {
		t.Fatalf("default = %q", got)
	}
}

func TestDefaultIDEmptyWhenNoActiveShop(t *testing.T) {
	shops := []Visible{{PublicID: "x", Status: "inactive", GrantedAtUnix: 1}}
	if got := DefaultID(shops); got != "" {
		t.Fatalf("default = %q", got)
	}
	if got := DefaultID(nil); got != "" {
		t.Fatalf("default = %q", got)
	}
}

func TestContainsRejectsInactiveAndUnknown(t *testing.T) {
	shops := []Visible{{PublicID: "a", Status: "active"}, {PublicID: "b", Status: "inactive"}}
	if !Contains(shops, "a") || Contains(shops, "b") || Contains(shops, "missing") {
		t.Fatal("visibility check failed")
	}
}
