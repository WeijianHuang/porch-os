// Package card holds the pure rules for ID cards (ADR-0017 / design/14).
// A card is a shop-scoped resource: many people may hold it over time, but at
// most one at any instant, and a staff member holds at most one card.
// Storage concerns stay in adapter/postgres; nothing here knows SQL.
package card

import "errors"

// Status of the card itself, independent of whether it is currently held.
type Status string

const (
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
)

var (
	ErrInactive      = errors.New("card is inactive")
	ErrCardHeld      = errors.New("card is held by someone else")
	ErrStaffHasCard  = errors.New("staff already holds a card")
	ErrCardInUse     = errors.New("release the card before deactivating it")
	ErrInvalidStatus = errors.New("invalid card status")
	ErrInvalidCode   = errors.New("card code must not be blank")
	ErrInvalidName   = errors.New("card name must not be blank")
	ErrInvalidReason = errors.New("reason is too long")
)

// IsValidStatus reports whether s is one of the two documented states.
func IsValidStatus(s Status) bool {
	return s == StatusActive || s == StatusInactive
}

// CanHold decides whether an active, currently unheld card may be taken.
// Both "held" and "inactive" are independent rejections: an inactive card is
// never handed out even when free, and releasing does not reactivate it.
func CanHold(status Status, held bool, staffHasCard bool) error {
	if !IsValidStatus(status) {
		return ErrInvalidStatus
	}
	if staffHasCard {
		return ErrStaffHasCard
	}
	if status != StatusActive {
		return ErrInactive
	}
	if held {
		return ErrCardHeld
	}
	return nil
}

// CanDeactivate enforces the documented boundary: deactivating does not
// silently release a person holding the card.
func CanDeactivate(held bool) error {
	if held {
		return ErrCardInUse
	}
	return nil
}
