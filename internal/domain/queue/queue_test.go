package queue

import (
	"reflect"
	"testing"
)

func TestValidateQueue(t *testing.T) {
	if err := ValidateQueue(1, "Queue A"); err != nil {
		t.Fatalf("valid queue rejected: %v", err)
	}
	if err := ValidateQueue(0, "Queue A"); err != ErrBadSeq {
		t.Fatalf("seq 0: got %v want ErrBadSeq", err)
	}
	if err := ValidateQueue(-1, "Queue A"); err != ErrBadSeq {
		t.Fatalf("seq -1: got %v want ErrBadSeq", err)
	}
	if err := ValidateQueue(1, "   "); err != ErrBadName {
		t.Fatalf("blank name: got %v want ErrBadName", err)
	}
}

func TestValidateStaff(t *testing.T) {
	if err := ValidateStaff("S1", "Alice"); err != nil {
		t.Fatalf("valid staff rejected: %v", err)
	}
	if err := ValidateStaff("", "Alice"); err != ErrBadCode {
		t.Fatalf("blank code: got %v want ErrBadCode", err)
	}
	if err := ValidateStaff("S1", " "); err != ErrBadName {
		t.Fatalf("blank name: got %v want ErrBadName", err)
	}
}

func assignments() []Assignment {
	return []Assignment{
		{QueuePublicID: "q2", QueueName: "Beta", QueueSeq: 2, StaffPublicID: "s1", StaffDisplayName: "Zoe"},
		{QueuePublicID: "q1", QueueName: "Alpha", QueueSeq: 1, StaffPublicID: "s1", StaffDisplayName: "Ann"},
		{QueuePublicID: "q1", QueueName: "Alpha", QueueSeq: 1, StaffPublicID: "s2", StaffDisplayName: "Bob"},
	}
}

func TestStaffByQueueSorted(t *testing.T) {
	got := StaffByQueue(assignments())
	want := map[string][]StaffRef{
		"q1": {{PublicID: "s2", DisplayName: "Bob"}, {PublicID: "s1", DisplayName: "Ann"}},
		"q2": {{PublicID: "s1", DisplayName: "Zoe"}},
	}
	// q1 sorted by display name: Ann before Bob
	want["q1"] = []StaffRef{{PublicID: "s1", DisplayName: "Ann"}, {PublicID: "s2", DisplayName: "Bob"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StaffByQueue: got %#v want %#v", got, want)
	}
}

func TestQueuesByStaffSortedBySeq(t *testing.T) {
	got := QueuesByStaff(assignments())
	want := []Ref{{PublicID: "q1", Name: "Alpha", Seq: 1}, {PublicID: "q2", Name: "Beta", Seq: 2}}
	if !reflect.DeepEqual(got["s1"], want) {
		t.Fatalf("QueuesByStaff[s1]: got %#v want %#v", got["s1"], want)
	}
}

func TestSortedQueuesNoMutation(t *testing.T) {
	in := []Ref{{PublicID: "q2", Name: "Beta", Seq: 2}, {PublicID: "q1", Name: "Alpha", Seq: 1}}
	got := SortedQueues(in)
	if got[0].PublicID != "q1" || in[0].PublicID != "q2" {
		t.Fatalf("SortedQueues mutated input or wrong order: got %#v in %#v", got, in)
	}
}
