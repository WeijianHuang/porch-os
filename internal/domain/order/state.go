// Package order holds dependency-free domain rules for the R3 order domain:
// state machines and pricing/allocation pure functions (ADR-0002, DA-01).
// No adapter, platform package or SQL lives here; every rule is unit-testable
// without a database.
package order

import "fmt"

// Order status values (ADR-0014 design/01 §5.1). There is deliberately no
// "refunding" state: a refund completes in one transaction, so outside it an
// order is only ever paid/completed/partially_refunded/refunded.
const (
	StatusDraft             = "draft"
	StatusOpen              = "open"
	StatusPendingPayment    = "pending_payment"
	StatusPaid              = "paid"
	StatusCompleted         = "completed"
	StatusCancelled         = "cancelled"
	StatusRefunded          = "refunded"
	StatusPartiallyRefunded = "partially_refunded"
)

// Payment / Refund share the same pending → succeeded|failed machine; failed
// is terminal and a retry is a NEW row (ADR-0013 / 0014 design/01 §5.2).
const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

var orderTransitions = map[string]map[string]bool{
	StatusDraft:          {StatusOpen: true, StatusCancelled: true},
	StatusOpen:           {StatusPendingPayment: true, StatusCancelled: true},
	StatusPendingPayment: {StatusPaid: true, StatusCancelled: true},
	StatusPaid:           {StatusCompleted: true, StatusRefunded: true, StatusPartiallyRefunded: true},
	StatusCompleted:      {StatusRefunded: true, StatusPartiallyRefunded: true},
	// A later refund can exhaust the remaining balance (ADR-0014).
	StatusPartiallyRefunded: {StatusRefunded: true, StatusPartiallyRefunded: true},
	StatusCancelled:         {},
	StatusRefunded:          {},
}

// InvalidTransition is the typed error returned for an illegal state move.
// The HTTP layer maps it to 412 PRECONDITION_FAILED (design/06 F-01).
type InvalidTransition struct{ From, To string }

func (e InvalidTransition) Error() string {
	return fmt.Sprintf("invalid transition %s -> %s", e.From, e.To)
}

// CanOrderTransition validates an order status move. DP-05: a paid-or-later
// order never goes back to open; a correction is a refund + a new order.
func CanOrderTransition(from, to string) error {
	if !orderTransitions[from][to] {
		return InvalidTransition{From: from, To: to}
	}
	return nil
}

// CanFinish validates a pending → succeeded|failed move for payments and
// refunds. Any other source/target (including a failed retry) is invalid.
func CanFinish(from, to string) error {
	if from != StatusPending || (to != StatusSucceeded && to != StatusFailed) {
		return InvalidTransition{From: from, To: to}
	}
	return nil
}

// RefundOrderStatus picks the order status after a refund given whether the
// full refundable balance is now exhausted. It returns an error when the
// source status cannot initiate a refund (design/06 F-02).
func RefundOrderStatus(from string, fullyRefunded bool) (string, error) {
	if from != StatusPaid && from != StatusCompleted && from != StatusPartiallyRefunded {
		return "", InvalidTransition{From: from, To: "<refund>"}
	}
	to := StatusPartiallyRefunded
	if fullyRefunded {
		to = StatusRefunded
	}
	if err := CanOrderTransition(from, to); err != nil {
		return "", err
	}
	return to, nil
}
