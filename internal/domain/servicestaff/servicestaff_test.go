package servicestaff

import (
	"reflect"
	"testing"
)

func TestRequiresStaff(t *testing.T) {
	cases := []struct {
		itemType string
		want     bool
	}{
		{ItemTypeService, true},
		{ItemTypeProduct, false},
		{"", false},
		{"SERVICE", false},
	}
	for _, tc := range cases {
		if got := RequiresStaff(tc.itemType); got != tc.want {
			t.Errorf("RequiresStaff(%q) = %v, want %v", tc.itemType, got, tc.want)
		}
	}
}

func TestCanAssign(t *testing.T) {
	cases := []struct {
		itemType, staff string
		want            bool
	}{
		{ItemTypeService, "", true},
		{ItemTypeService, "s-1", true},
		{ItemTypeProduct, "", true},
		{ItemTypeProduct, "s-1", false},
	}
	for _, tc := range cases {
		if got := CanAssign(tc.itemType, tc.staff); got != tc.want {
			t.Errorf("CanAssign(%q, %q) = %v, want %v", tc.itemType, tc.staff, got, tc.want)
		}
	}
}

func TestNormalizeItemIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"trim dedupe sort", []string{" b ", "a", "b", "", "  ", "c"}, []string{"a", "b", "c"}},
		{"stable for same set", []string{"z", "y", "z"}, []string{"y", "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeItemIDs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if again := NormalizeItemIDs(got); !reflect.DeepEqual(again, got) {
				t.Fatalf("normalization is not idempotent: %v vs %v", again, got)
			}
		})
	}
}
