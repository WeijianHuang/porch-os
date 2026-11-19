package scheduling

import "fmt"

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
