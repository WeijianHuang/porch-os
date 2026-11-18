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

func TestValidateDuty(t *testing.T) {
	if err := ValidateDuty("active", "on"); err != nil {
		t.Fatalf("active on rejected: %v", err)
	}
	if err := ValidateDuty("active", "off"); err != nil {
		t.Fatalf("active off rejected: %v", err)
	}
	if err := ValidateDuty("inactive", "off"); err != nil {
		t.Fatalf("inactive off rejected: %v", err)
	}
	if err := ValidateDuty("inactive", "on"); err != ErrDutyConflict {
		t.Fatalf("inactive on: got %v want ErrDutyConflict", err)
	}
	if err := ValidateDuty("active", "break"); err != ErrBadDuty {
		t.Fatalf("bad duty: got %v want ErrBadDuty", err)
	}
	if err := ValidateDuty("frozen", "on"); err != ErrBadStatus {
		t.Fatalf("bad status: got %v want ErrBadStatus", err)
	}
}

func TestValidateOrder(t *testing.T) {
	current := []string{"s1", "s2", "s3"}
	if err := ValidateOrder(current, []string{"s3", "s1", "s2"}); err != nil {
		t.Fatalf("valid reorder rejected: %v", err)
	}
	if err := ValidateOrder(current, []string{"s3", "s1"}); err != ErrInvalidOrder {
		t.Fatalf("missing member: got %v want ErrInvalidOrder", err)
	}
	if err := ValidateOrder(current, []string{"s3", "s1", "s1"}); err != ErrInvalidOrder {
		t.Fatalf("duplicate: got %v want ErrInvalidOrder", err)
	}
	if err := ValidateOrder(current, []string{"s1", "s2", "s4"}); err != ErrInvalidOrder {
		t.Fatalf("unknown member: got %v want ErrInvalidOrder", err)
	}
	if err := ValidateOrder(nil, nil); err != nil {
		t.Fatalf("empty queue no-op rejected: %v", err)
	}
}

func assignments() []Assignment {
	return []Assignment{
		{QueuePublicID: "q2", QueueName: "Beta", QueueSeq: 2, StaffPublicID: "s1", StaffDisplayName: "Zoe", StaffDutyStatus: "on", Position: 1},
		{QueuePublicID: "q1", QueueName: "Alpha", QueueSeq: 1, StaffPublicID: "s1", StaffDisplayName: "Ann", StaffDutyStatus: "on", Position: 2},
		{QueuePublicID: "q1", QueueName: "Alpha", QueueSeq: 1, StaffPublicID: "s2", StaffDisplayName: "Bob", StaffDutyStatus: "off", Position: 1},
	}
}

func TestStaffByQueueSortedByPosition(t *testing.T) {
	got := StaffByQueue(assignments())
	want := map[string][]StaffRef{
		"q1": {
			{PublicID: "s2", DisplayName: "Bob", DutyStatus: "off", Position: 1},
			{PublicID: "s1", DisplayName: "Ann", DutyStatus: "on", Position: 2},
		},
		"q2": {{PublicID: "s1", DisplayName: "Zoe", DutyStatus: "on", Position: 1}},
	}
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
