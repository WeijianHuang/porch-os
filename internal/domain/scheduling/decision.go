package scheduling

import (
	"sort"
	"time"
)

// DecisionInput is the complete, immutable input for one main decision.
type DecisionInput struct {
	Context DecisionContext
	Rules   []Rule
}

type DecisionResult struct {
	Decision    Decision
	Trace       []ChainStep
	HitRuleID   RuleID
	HitRuleType RuleType
	HitScope    ScopeLevel
	FellBack    bool
	EvaluatedAt time.Time
}

// Explain renders the first step that stopped the chain (FR-19).
func (r DecisionResult) Explain() string {
	if r.Decision == DecisionRotate {
		return "all enabled steps passed; rotation"
	}
	for _, s := range r.Trace {
		if s.Hit {
			return string(s.Type) + ": " + s.Reason
		}
	}
	return "held"
}

// DecideChain evaluates the configurable chain (FR-13/14/22), pure and
// side-effect free. One rule per step wins at the highest applicable scope;
// winners run in Order; first hit stops and returns its OnHit action.
func DecideChain(in DecisionInput) DecisionResult {
	res := DecisionResult{Decision: DecisionRotate, EvaluatedAt: in.Context.Now}
	winners := selectWinners(in)
	sort.SliceStable(winners, func(i, j int) bool {
		if winners[i].Order != winners[j].Order {
			return winners[i].Order < winners[j].Order
		}
		if winners[i].Type != winners[j].Type {
			return winners[i].Type < winners[j].Type
		}
		return winners[i].ID < winners[j].ID
	})
	for _, r := range winners {
		if r.Scope == ScopeGlobal {
			res.FellBack = true
		}
		hit, reason := evaluateStep(r, in.Context)
		step := ChainStep{
			RuleID: r.ID, Type: r.Type, Scope: r.Scope,
			Hit: hit, Decision: res.Decision, Reason: reason,
		}
		res.Trace = append(res.Trace, step)
		if !hit {
			continue
		}
		action := r.OnHit
		if action == "" {
			action = DecisionHold
		}
		res.Decision = action
		res.HitRuleID = r.ID
		res.HitRuleType = r.Type
		res.HitScope = r.Scope
		res.Trace[len(res.Trace)-1].Decision = action
		break
	}
	return res
}

func selectWinners(in DecisionInput) []Rule {
	byType := make(map[RuleType]Rule)
	local := in.Context.LocalNow()
	for _, r := range in.Rules {
		if r.SiteID != in.Context.Site.ID && r.Scope != ScopeGlobal {
			continue
		}
		if !r.AppliesTo(in.Context) {
			continue
		}
		if r.Window != nil && !r.Window.Contains(local) {
			continue
		}
		cur, ok := byType[r.Type]
		if !ok || better(r, cur) {
			byType[r.Type] = r
		}
	}
	out := make([]Rule, 0, len(byType))
	for _, r := range byType {
		out = append(out, r)
	}
	return out
}

func better(candidate, current Rule) bool {
	cp, cu := candidate.Scope.Priority(), current.Scope.Priority()
	if cp != cu {
		return cp > cu
	}
	if candidate.Order != current.Order {
		return candidate.Order < current.Order
	}
	return candidate.ID < current.ID
}

func evaluateStep(r Rule, ctx DecisionContext) (bool, string) {
	switch r.Type {
	case RuleExecutorStopped:
		if ctx.Executor.StopRotation {
			return true, "executor stop-rotation flag set"
		}
	case RuleRotationSwitch:
		if !ctx.Site.RotationEnabled {
			return true, "rotation master switch off"
		}
	case RuleAssignmentMethod:
		if len(r.AssignmentMethods) > 0 {
			for _, m := range r.AssignmentMethods {
				if m == ctx.AssignmentMethod {
					return false, "assignment method allows rotation"
				}
			}
			return true, "assignment method not mapped to rotation"
		}
	case RuleTaskType:
		if ctx.Task.NoRotation {
			return true, "task type marked no-rotation"
		}
	case RuleOvertime:
		if r.NoRotation && ctx.Overtime {
			return true, "overtime window marked no-rotation"
		}
	case RuleTimeReserved:
		local := ctx.LocalNow()
		for _, w := range ctx.Executor.TimeReservations {
			if w.Contains(local) {
				return true, "executor in configurable time-reserved window"
			}
		}
	case RuleGroupAToB, RuleGroupBToA:
		if r.NoRotation && r.FromGroup == ctx.Executor.Group && r.ToGroup == ctx.Task.Group {
			return true, "cross-group rule marked no-rotation"
		}
	}
	return false, ""
}
