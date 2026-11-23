// Package companion holds the pure state rules of a companion group
// (ADR-0022): a group is an open/closed/cancelled orchestration view over
// multiple already-card-bound orders. It never owns money and is not a
// payment subject.
package companion

import "errors"

type Status string

const (
	StatusOpen      Status = "open"
	StatusClosed    Status = "closed"
	StatusCancelled Status = "cancelled"
)

var (
	ErrNotOpen      = errors.New("companion group is not open")
	ErrNotCancel    = errors.New("companion group cannot be cancelled after a successful payment")
	ErrNotClose     = errors.New("companion group still has unsettled members")
	ErrOrderSettled = errors.New("order is already settled")
)

func IsValidStatus(s Status) bool {
	switch s {
	case StatusOpen, StatusClosed, StatusCancelled:
		return true
	default:
		return false
	}
}

// CanJoin accepts members only while the group is open.
func CanJoin(s Status) error {
	if s != StatusOpen {
		return ErrNotOpen
	}
	return nil
}

// CanLeave accepts a split only while the group is open.
func CanLeave(s Status) error {
	if s != StatusOpen {
		return ErrNotOpen
	}
	return nil
}

// CanClose requires every active member to be settled. An empty group may be
// closed as a no-op.
func CanClose(s Status, activeMembers, unsettledMembers int) error {
	if s != StatusOpen {
		return ErrNotOpen
	}
	if activeMembers > 0 && unsettledMembers > 0 {
		return ErrNotClose
	}
	return nil
}

// CanCancel requires no member to carry a successful payment.
func CanCancel(s Status, paidMembers int) error {
	if s != StatusOpen {
		return ErrNotOpen
	}
	if paidMembers > 0 {
		return ErrNotCancel
	}
	return nil
}

// IsSettledStatus reports whether an order has reached a settled state. Used
// to decide whether a member may join/leave and whether a group can close.
func IsSettledStatus(s string) bool {
	switch s {
	case "paid", "completed", "partially_refunded", "refunded":
		return true
	default:
		return false
	}
}
