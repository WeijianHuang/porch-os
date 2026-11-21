package card

import (
	"errors"
	"testing"
)

func TestIsValidStatus(t *testing.T) {
	for _, s := range []Status{StatusActive, StatusInactive} {
		if !IsValidStatus(s) {
			t.Fatalf("status %q should be valid", s)
		}
	}
	for _, s := range []Status{"", "held", "active ", "ACTIVE"} {
		if IsValidStatus(s) {
			t.Fatalf("status %q should be invalid", s)
		}
	}
}

func TestCanHold(t *testing.T) {
	tests := []struct {
		name         string
		status       Status
		held         bool
		staffHasCard bool
		want         error
	}{
		{"active free staff free", StatusActive, false, false, nil},
		{"inactive free", StatusInactive, false, false, ErrInactive},
		{"inactive held", StatusInactive, true, false, ErrInactive},
		{"active held", StatusActive, true, false, ErrCardHeld},
		{"staff already holds", StatusActive, false, true, ErrStaffHasCard},
		{"held plus staff holds", StatusActive, true, true, ErrStaffHasCard},
		{"bad status", "teleporting", false, false, ErrInvalidStatus},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CanHold(tc.status, tc.held, tc.staffHasCard)
			if !errors.Is(got, tc.want) {
				t.Fatalf("CanHold() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanDeactivate(t *testing.T) {
	if err := CanDeactivate(false); err != nil {
		t.Fatalf("free card should be deactivatable: %v", err)
	}
	if !errors.Is(CanDeactivate(true), ErrCardInUse) {
		t.Fatal("held card must not be silently deactivated")
	}
}
