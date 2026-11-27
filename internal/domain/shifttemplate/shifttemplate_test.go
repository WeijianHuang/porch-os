package shifttemplate

import (
	"reflect"
	"testing"
)

func TestValidateTime(t *testing.T) {
	for _, value := range []string{"00:00", "04:00", "11:59", "23:59"} {
		if err := ValidateTime(value); err != nil {
			t.Fatalf("ValidateTime(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range []string{"", "7:00", "24:00", "12:60", "12:00:00", "12-00"} {
		if err := ValidateTime(value); err == nil {
			t.Fatalf("ValidateTime(%q) = nil, want error", value)
		}
	}
}

func TestValidateLeadMinutes(t *testing.T) {
	if err := ValidateLeadMinutes(0); err != nil {
		t.Fatalf("zero minutes should be valid: %v", err)
	}
	if err := ValidateLeadMinutes(1440); err != nil {
		t.Fatalf("1440 minutes should be valid: %v", err)
	}
	for _, n := range []int{-1, 1441} {
		if err := ValidateLeadMinutes(n); err == nil {
			t.Fatalf("ValidateLeadMinutes(%d) = nil, want error", n)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	got, err := NormalizeName("  早班  ")
	if err != nil || got != "早班" {
		t.Fatalf("NormalizeName = %q, %v; want 早班", got, err)
	}
	if _, err := NormalizeName("   "); err == nil {
		t.Fatal("blank name should be rejected")
	}
}

func TestPlanAssignment(t *testing.T) {
	remove, add := PlanAssignment(
		[]string{"s1", "s2", "s2"},
		[]string{"s2", "s3", "s3"},
	)
	if !reflect.DeepEqual(remove, []string{"s1"}) {
		t.Fatalf("remove = %v, want [s1]", remove)
	}
	if !reflect.DeepEqual(add, []string{"s3"}) {
		t.Fatalf("add = %v, want [s3]", add)
	}
	remove, add = PlanAssignment(nil, nil)
	if len(remove) != 0 || len(add) != 0 {
		t.Fatalf("empty diff = %v / %v", remove, add)
	}
}
