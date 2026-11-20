package scheduling

import (
	"errors"
	"testing"
	"time"
)

func nowUTC() time.Time { return time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC) }

func site() Site {
	return Site{ID: "shop-1", Name: "Site 1", Timezone: "Asia/Shanghai", RotationEnabled: true}
}

func baseRules() []Rule {
	out := make([]Rule, 0, len(DefaultChainOrder))
	for i, typ := range DefaultChainOrder {
		r := Rule{
			ID: RuleID(typ), SiteID: "shop-1", Type: typ,
			Order: i + 1, Enabled: true, Scope: ScopeSite, ScopeID: "shop-1",
			OnHit: DecisionHold,
		}
		if typ == RuleAssignmentMethod {
			r.AssignmentMethods = []AssignmentMethod{MethodRotational}
		}
		out = append(out, r)
	}
	return out
}

func ctx() DecisionContext {
	return DecisionContext{
		Site: site(), Executor: Executor{ID: "ex-1", State: StateAvailable},
		Task:          TaskType{ID: "t-1", Group: "B"},
		SourceQueueID: "q-1", PrimaryQueueID: "q-1",
		AssignmentMethod: MethodRotational, Now: nowUTC(),
	}
}

func TestDecideChainRotateByDefault(t *testing.T) {
	r := DecideChain(DecisionInput{Context: ctx(), Rules: baseRules()})
	if r.Decision != DecisionRotate {
		t.Fatalf("expected rotate, got %s", r.Decision)
	}
	if len(r.Trace) != 8 {
		t.Fatalf("expected 8 steps, got %d", len(r.Trace))
	}
}

func TestDecideChainHitAndStop(t *testing.T) {
	c := ctx()
	c.Executor.StopRotation = true
	r := DecideChain(DecisionInput{Context: c, Rules: baseRules()})
	if r.Decision != DecisionHold || r.HitRuleType != RuleExecutorStopped {
		t.Fatalf("expected hold on step 1, got %s / %s", r.Decision, r.HitRuleType)
	}
	if len(r.Trace) != 1 {
		t.Fatalf("chain must stop at first hit, got %d steps", len(r.Trace))
	}
}

func TestScopePriority(t *testing.T) {
	rules := baseRules()
	// A global rule and an executor rule for the same step; executor wins.
	g := rules[0]
	g.ID = "global-1"
	g.Scope = ScopeGlobal
	g.ScopeID = ""
	g.OnHit = DecisionRotate
	e := rules[0]
	e.ID = "exec-1"
	e.Scope = ScopeExecutor
	e.ScopeID = "ex-1"
	e.OnHit = DecisionHold
	c := ctx()
	c.Executor.StopRotation = true
	r := DecideChain(DecisionInput{Context: c, Rules: append(rules, g, e)})
	if r.HitRuleID != "exec-1" || r.Decision != DecisionHold {
		t.Fatalf("executor scope must win, got %s / %s", r.HitRuleID, r.Decision)
	}
}

func TestTimeWindowCrossMidnight(t *testing.T) {
	w := &TimeWindow{Start: "22:00", End: "02:00"}
	in := time.Date(2026, 11, 20, 23, 30, 0, 0, time.UTC)
	out := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if !w.Contains(in) || w.Contains(out) {
		t.Fatal("cross-midnight window check failed")
	}
}

func TestApplyActionLandsByOwnLength(t *testing.T) {
	members := []QueueMember{
		{QueueID: "q-2", ExecutorID: "a", Position: 1, Status: MemberActive},
		{QueueID: "q-2", ExecutorID: "b", Position: 2, Status: MemberActive},
		{QueueID: "q-2", ExecutorID: "ex-1", Position: 3, Status: MemberActive},
	}
	out, old, newPos, changed := ApplyAction(members, "ex-1", ActionMoveToTail, 0, nowUTC())
	if !changed || old != 3 || newPos != 3 {
		t.Fatalf("tail from tail: changed=%v old=%d new=%d", changed, old, newPos)
	}
	// Move 'a' to tail => position becomes 3, not copied from another queue.
	out, _, newPos, _ = ApplyAction(members, "a", ActionMoveToTail, 0, nowUTC())
	if newPos != 3 || out[2].ExecutorID != "a" {
		t.Fatalf("expected a at own tail 3, got %d / %s", newPos, out[2].ExecutorID)
	}
}

func TestResolveTargetsFrozenAndPin(t *testing.T) {
	queues := []Queue{
		{ID: "q-1", Group: "A", Enabled: true},
		{ID: "q-2", Group: "A", Enabled: true},
		{ID: "q-3", Group: "A", Enabled: false},
	}
	ms := []QueueMember{
		{QueueID: "q-1", ExecutorID: "ex-1"},
		{QueueID: "q-2", ExecutorID: "ex-1", Pin: Pin{Type: PinPermanent, EffectiveFrom: nowUTC().Add(-time.Hour)}},
		{QueueID: "q-3", ExecutorID: "ex-1"},
	}
	targets := ResolveTargets(LinkagePolicy{Mode: ModeAll}, "q-1", queues, ms, nowUTC())
	if len(targets) != 1 || targets[0].QueueID != "q-1" {
		t.Fatalf("only active unpinned source queue should be targeted, got %+v", targets)
	}
	frozen := ResolveTargets(LinkagePolicy{Mode: ModeFrozen}, "q-1", queues, ms, nowUTC())
	if len(frozen) != 0 {
		t.Fatalf("frozen mode must emit no targets, got %+v", frozen)
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition(StateExecuting, StateAvailable); err != nil {
		t.Fatalf("executing -> available must be legal: %v", err)
	}
	if err := ValidateTransition(StateOffDuty, StateExecuting); err == nil {
		t.Fatal("off_duty -> executing must be illegal")
	}
}

func TestExecutorDispatchable(t *testing.T) {
	e := Executor{State: StateAvailable, DutyStatus: "on", Status: "active"}
	if !e.IsDispatchable() {
		t.Fatal("available/on/active must be dispatchable")
	}
	e.DutyStatus = "off"
	if e.IsDispatchable() {
		t.Fatal("off duty must not be dispatchable")
	}
	e.DutyStatus = "on"
	e.State = StatePinned
	if !e.IsDispatchable() {
		t.Fatal("pinned executor still waits and can be dispatched")
	}
}

func TestValidateManualTransition(t *testing.T) {
	// The six documented targets are accepted from any idle state.
	for _, target := range ManualTargets {
		for _, from := range ManualTargets {
			if from == target {
				continue
			}
			if err := ValidateManualTransition(from, target); err != nil {
				t.Fatalf("%s -> %s should be allowed, got %v", from, target, err)
			}
		}
	}

	// Engine-owned states cannot be set by hand.
	for _, target := range []ExecutorState{StateExecuting, StateDesignatedOccupied, StateCrossGroupSupport} {
		if err := ValidateManualTransition(StateAvailable, target); !errors.Is(err, ErrManualTarget) {
			t.Fatalf("target %s must be rejected, got %v", target, err)
		}
	}

	// A live event blocks every manual change.
	for _, from := range []ExecutorState{StateExecuting, StateDesignatedOccupied, StateCrossGroupSupport} {
		if err := ValidateManualTransition(from, StateOffDuty); !errors.Is(err, ErrExecutorBusy) {
			t.Fatalf("%s must report busy, got %v", from, err)
		}
	}

	// No-op changes are rejected so the caller does not write a hollow audit.
	if err := ValidateManualTransition(StateAvailable, StateAvailable); !errors.Is(err, ErrStateUnchanged) {
		t.Fatalf("same state must be rejected, got %v", err)
	}

	// Disabled staff can only be parked off duty.
	if err := ValidateManualTransition(StateDisabled, StateOffDuty); err != nil {
		t.Fatalf("disabled -> off_duty should be allowed, got %v", err)
	}
	if err := ValidateManualTransition(StateDisabled, StateAvailable); !errors.Is(err, ErrManualTransition) {
		t.Fatalf("disabled -> available must be rejected, got %v", err)
	}
}

func TestDutyForState(t *testing.T) {
	for _, s := range []ExecutorState{StateLeave, StateOffDuty, StateDisabled} {
		if got := DutyForState(s); got != "off" {
			t.Fatalf("%s duty = %s, want off", s, got)
		}
	}
	for _, s := range []ExecutorState{StateAvailable, StateStandby, StateTimeReserved, StateOvertime} {
		if got := DutyForState(s); got != "on" {
			t.Fatalf("%s duty = %s, want on", s, got)
		}
	}
}
