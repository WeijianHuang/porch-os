package companion

import "testing"

func TestStatusRules(t *testing.T) {
	for _, s := range []Status{StatusOpen, StatusClosed, StatusCancelled} {
		if !IsValidStatus(s) {
			t.Fatalf("expected %q to be valid", s)
		}
	}
	if IsValidStatus("refunding") {
		t.Fatal("refunding must not be a companion group status")
	}
}

func TestCanJoinLeave(t *testing.T) {
	if err := CanJoin(StatusOpen); err != nil {
		t.Fatalf("open group must accept members: %v", err)
	}
	if err := CanJoin(StatusClosed); err != ErrNotOpen {
		t.Fatalf("closed group must reject members, got %v", err)
	}
	if err := CanLeave(StatusCancelled); err != ErrNotOpen {
		t.Fatalf("cancelled group must reject leave, got %v", err)
	}
}

func TestCanClose(t *testing.T) {
	if err := CanClose(StatusOpen, 0, 0); err != nil {
		t.Fatalf("empty open group may close: %v", err)
	}
	if err := CanClose(StatusOpen, 2, 0); err != nil {
		t.Fatalf("fully settled group may close: %v", err)
	}
	if err := CanClose(StatusOpen, 2, 1); err != ErrNotClose {
		t.Fatalf("unsettled member must block close, got %v", err)
	}
}

func TestCanCancel(t *testing.T) {
	if err := CanCancel(StatusOpen, 0); err != nil {
		t.Fatalf("unpaid group may cancel: %v", err)
	}
	if err := CanCancel(StatusOpen, 1); err != ErrNotCancel {
		t.Fatalf("paid member must block cancel, got %v", err)
	}
}

func TestIsSettledStatus(t *testing.T) {
	for _, s := range []string{"paid", "completed", "partially_refunded", "refunded"} {
		if !IsSettledStatus(s) {
			t.Fatalf("%s must be settled", s)
		}
	}
	for _, s := range []string{"draft", "open", "pending_payment", "cancelled"} {
		if IsSettledStatus(s) {
			t.Fatalf("%s must not be settled", s)
		}
	}
}
