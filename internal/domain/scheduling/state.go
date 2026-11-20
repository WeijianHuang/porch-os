package scheduling

import (
	"errors"
	"fmt"
)

// canTransition implements the executor state machine (design/12 §5). States
// flow one direction only; every transition is audited by the caller.
func canTransition(from, to ExecutorState) bool {
	if from == to {
		return true
	}
	allowed := map[ExecutorState][]ExecutorState{
		StateStandby: {
			StateAvailable, StateExecuting, StatePinned, StateTimeReserved,
			StateDisabled, StateLeave, StateOffDuty, StateCrossGroupSupport,
		},
		StateAvailable: {
			StateExecuting, StatePinned, StateTimeReserved, StateDisabled,
			StateLeave, StateOffDuty, StateCrossGroupSupport,
		},
		StateExecuting: {
			StateAvailable, StateDesignatedOccupied, StateOvertime, StateCrossGroupSupport,
		},
		StateDesignatedOccupied: {StateExecuting, StateAvailable},
		StatePinned:             {StateAvailable, StateOffDuty},
		StateTimeReserved:       {StateAvailable, StateOffDuty},
		StateOvertime:           {StateAvailable, StateOffDuty},
		StateDisabled:           {StateAvailable},
		StateLeave:              {StateAvailable},
		StateOffDuty:            {StateAvailable},
		StateCrossGroupSupport:  {StateAvailable, StateExecuting},
	}
	for _, target := range allowed[from] {
		if target == to {
			return true
		}
	}
	return false
}

type StateError struct{ From, To ExecutorState }

func (e StateError) Error() string {
	return fmt.Sprintf("transition %s -> %s is not allowed", e.From, e.To)
}

func ValidateTransition(from, to ExecutorState) error {
	if !canTransition(from, to) {
		return StateError{From: from, To: to}
	}
	return nil
}

// ---- Manual (operator-driven) state changes (ADR-0015) ----------------------
//
// The automatic table above belongs to the engine: only a dispatch may produce
// executing/designated_occupied, because those states imply a live event plus a
// snapshot. Operators get a separate, deliberately smaller policy so a manual
// click can never desynchronise events from the staff row.

// ManualTargets are the states an operator may set from the console.
var ManualTargets = []ExecutorState{
	StateAvailable, StateStandby, StateTimeReserved, StateOvertime, StateLeave, StateOffDuty,
}

// Manual state errors. Handlers map these to API codes (ADR-0015 §3.2).
var (
	ErrManualTarget     = errors.New("state cannot be set manually")
	ErrStateUnchanged   = errors.New("executor already in that state")
	ErrExecutorBusy     = errors.New("executor has a live event")
	ErrManualTransition = errors.New("manual transition not allowed")
)

// IsManualTarget reports whether to is one of the six operator-settable states.
func IsManualTarget(to ExecutorState) bool {
	for _, s := range ManualTargets {
		if s == to {
			return true
		}
	}
	return false
}

// isBusyState reports states that imply a live event or an off-site assignment.
// They may only be left through the dispatch lifecycle, never by hand.
func isBusyState(s ExecutorState) bool {
	switch s {
	case StateExecuting, StateDesignatedOccupied, StateCrossGroupSupport:
		return true
	default:
		return false
	}
}

// ValidateManualTransition checks an operator-driven state change. It is
// intentionally not folded into canTransition: mixing the two tables would let
// future engine edits silently widen the manual path.
func ValidateManualTransition(from, to ExecutorState) error {
	if !IsManualTarget(to) {
		return ErrManualTarget
	}
	if from == to {
		return ErrStateUnchanged
	}
	if isBusyState(from) {
		return ErrExecutorBusy
	}
	// A disabled (inactive) staff member can only be parked as off duty until
	// the employment status is restored (ADR-0010 §1).
	if from == StateDisabled && to != StateOffDuty {
		return ErrManualTransition
	}
	return nil
}

// DutyForState maps a state to the duty switch it implies, so the queue views
// and the board never show "on duty" for someone marked as leave/off.
func DutyForState(s ExecutorState) string {
	switch s {
	case StateLeave, StateOffDuty, StateDisabled:
		return "off"
	default:
		return "on"
	}
}
