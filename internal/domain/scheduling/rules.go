package scheduling

import (
	"fmt"
	"strconv"
	"time"
)

// ScopeLevel is the configuration layer a rule belongs to.
type ScopeLevel string

const (
	ScopeExecutor ScopeLevel = "executor"
	ScopeQueue    ScopeLevel = "queue"
	ScopeTaskType ScopeLevel = "task_type"
	ScopeSite     ScopeLevel = "site"
	ScopeGlobal   ScopeLevel = "global"
)

// Priority implements FR-14: executor > queue > task type > site > global.
func (s ScopeLevel) Priority() int {
	switch s {
	case ScopeExecutor:
		return 5
	case ScopeQueue:
		return 4
	case ScopeTaskType:
		return 3
	case ScopeSite:
		return 2
	case ScopeGlobal:
		return 1
	default:
		return 0
	}
}

type RuleType string

const (
	RuleExecutorStopped  RuleType = "executor_stopped_rotation"
	RuleRotationSwitch   RuleType = "rotation_master_switch"
	RuleAssignmentMethod RuleType = "assignment_method_allows_rotation"
	RuleTaskType         RuleType = "task_type_no_rotation"
	RuleOvertime         RuleType = "overtime_no_rotation"
	RuleTimeReserved     RuleType = "time_reserved"
	RuleGroupAToB        RuleType = "group_a_to_b"
	RuleGroupBToA        RuleType = "group_b_to_a"
)

var DefaultChainOrder = []RuleType{
	RuleExecutorStopped, RuleRotationSwitch, RuleAssignmentMethod, RuleTaskType,
	RuleOvertime, RuleTimeReserved, RuleGroupAToB, RuleGroupBToA,
}

// Rule is one configurable step. Hit action defaults to Hold.
type Rule struct {
	ID                RuleID
	SiteID            SiteID
	Type              RuleType
	Order             int
	Enabled           bool
	Scope             ScopeLevel
	ScopeID           string
	OnHit             Decision
	Window            *TimeWindow
	AssignmentMethods []AssignmentMethod
	FromGroup         string
	ToGroup           string
	NoRotation        bool
}

func (r Rule) AppliesTo(ctx DecisionContext) bool {
	if !r.Enabled {
		return false
	}
	if r.ScopeID == "" {
		return true
	}
	switch r.Scope {
	case ScopeExecutor:
		return r.ScopeID == string(ctx.Executor.ID)
	case ScopeQueue:
		return r.ScopeID == string(ctx.SourceQueueID) || r.ScopeID == string(ctx.PrimaryQueueID)
	case ScopeTaskType:
		return r.ScopeID == ctx.Task.ID
	case ScopeSite:
		return r.ScopeID == string(ctx.Site.ID)
	case ScopeGlobal:
		return true
	default:
		return false
	}
}

// TimeWindow is a configurable local-time range with optional weekday filter.
// Start/End are "HH:MM" in the site's timezone. Start > End crosses midnight.
type TimeWindow struct {
	Start string
	End   string
	Days  []time.Weekday
}

func (w *TimeWindow) Contains(t time.Time) bool {
	if w == nil {
		return false
	}
	if len(w.Days) > 0 {
		matched := false
		for _, d := range w.Days {
			if d == t.Weekday() {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	start, err := parseHHMM(w.Start)
	if err != nil {
		return false
	}
	end, err := parseHHMM(w.End)
	if err != nil {
		return false
	}
	cur := t.Hour()*60 + t.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}

func parseHHMM(s string) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("invalid HH:MM %q", s)
	}
	h, err := strconv.Atoi(s[:2])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid hour in %q", s)
	}
	m, err := strconv.Atoi(s[3:])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid minute in %q", s)
	}
	return h*60 + m, nil
}
