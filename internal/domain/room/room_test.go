package room

import (
	"errors"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		floor   int
		code    string
		nameVal string
		want    error
	}{
		{"ok ground", 0, "101", "大厅", nil},
		{"ok basement", -1, "B01", "地下包间", nil},
		{"ok upper", 200, "2001", "顶层", nil},
		{"floor too low", -6, "101", "x", ErrBadFloor},
		{"floor too high", 201, "101", "x", ErrBadFloor},
		{"blank code", 1, "   ", "x", ErrBadCode},
		{"blank name", 1, "101", "  ", ErrBadName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate(tc.floor, tc.code, tc.nameVal)
			if !errors.Is(got, tc.want) {
				t.Fatalf("Validate(%d,%q,%q) = %v, want %v", tc.floor, tc.code, tc.nameVal, got, tc.want)
			}
		})
	}
}

func TestValidStatus(t *testing.T) {
	for _, s := range []string{"active", "inactive"} {
		if !ValidStatus(s) {
			t.Fatalf("ValidStatus(%q) = false", s)
		}
	}
	for _, s := range []string{"occupied", "cleaning", "", "ACTIVE"} {
		if ValidStatus(s) {
			t.Fatalf("ValidStatus(%q) = true", s)
		}
	}
}

func TestFloorLabel(t *testing.T) {
	cases := map[int]string{-2: "B2", -1: "B1", 0: "GF", 1: "1F", 12: "12F"}
	for floor, want := range cases {
		if got := FloorLabel(floor); got != want {
			t.Fatalf("FloorLabel(%d) = %q, want %q", floor, got, want)
		}
	}
}

func TestGroupByFloor(t *testing.T) {
	rows := []Flat{
		{PublicID: "c", Floor: 1, Code: "102"},
		{PublicID: "a", Floor: 2, Code: "201"},
		{PublicID: "d", Floor: 1, Code: "101"},
		{PublicID: "b", Floor: -1, Code: "B01"},
	}
	groups := GroupByFloor(rows)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}
	wantFloors := []int{2, 1, -1}
	for i, g := range groups {
		if g.Floor != wantFloors[i] {
			t.Fatalf("group %d floor = %d, want %d", i, g.Floor, wantFloors[i])
		}
	}
	// Floor 1: codes ascending 101(d) then 102(c).
	if groups[1].Rooms[0].Code != "101" || groups[1].Rooms[1].Code != "102" {
		t.Fatalf("floor 1 order = %v", groups[1].Rooms)
	}
	// Input must not be mutated.
	if rows[0].Code != "102" || rows[2].Code != "101" {
		t.Fatalf("GroupByFloor mutated input: %v", rows)
	}
}

func TestGroupByFloorEmpty(t *testing.T) {
	groups := GroupByFloor(nil)
	if len(groups) != 0 {
		t.Fatalf("got %d groups for empty input", len(groups))
	}
}
