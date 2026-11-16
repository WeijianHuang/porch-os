package money

import (
	"encoding/json"
	"testing"
)

func TestStringRoundTrip(t *testing.T) {
	m, err := FromString("19.90")
	if err != nil {
		t.Fatal(err)
	}
	if m.String() != "19.90" {
		t.Fatalf("got %s", m.String())
	}
	b, err := json.Marshal(m)
	if err != nil || string(b) != `"19.90"` {
		t.Fatalf("json: %s %v", b, err)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil || back.String() != "19.90" {
		t.Fatalf("unmarshal: %s %v", back.String(), err)
	}
}

func TestAllocateSumsExactly(t *testing.T) {
	m, _ := FromString("10.00")
	parts, err := m.Allocate(3)
	if err != nil {
		t.Fatal(err)
	}
	sum := Zero()
	for _, p := range parts {
		sum = sum.Add(p)
	}
	if sum.String() != "10.00" {
		t.Fatalf("allocated sum = %s", sum.String())
	}
	if parts[0].String() != "3.34" || parts[2].String() != "3.33" {
		t.Fatalf("unexpected parts: %v %v %v", parts[0], parts[1], parts[2])
	}
}

func TestRejectNegative(t *testing.T) {
	m, _ := FromString("-0.01")
	if !m.IsNegative() {
		t.Fatal("expected negative")
	}
}
