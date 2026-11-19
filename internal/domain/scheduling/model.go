// Package scheduling holds the pure domain of the multi-queue scheduling
// engine (ADR-0012 / design/12). Nothing here reads the database or HTTP
// state. Generic terms only: Site, Queue, Executor, Position, Pin, Dispatch,
// TaskType, AssignmentMethod. In this system Site maps to shops/shop_id,
// Executor maps to staff, and Position maps to queue_staff.position; the
// mapping is performed by the postgres adapter, never here (DA-01, C-10).
package scheduling

import "time"

// ---- Identifiers (public-id strings carried over the API) ------------------

type SiteID string
type QueueID string
type ExecutorID string
type EventID string
type RuleID string

// ---- Site ------------------------------------------------------------------

type TemplateMode string

const (
	TemplateCopy    TemplateMode = "copy"
	TemplateInherit TemplateMode = "inherit"
)

// Site is the isolation boundary (shops). One shop_id layer only (ADR-0003).
type Site struct {
	ID              SiteID
	Name            string
	Timezone        string
	TemplateMode    TemplateMode
	RotationEnabled bool
}

// ---- Queue -----------------------------------------------------------------

type QueueType string

const (
	QueueMain      QueueType = "main"
	QueueSecondary QueueType = "secondary"
	QueueTemporary QueueType = "temporary"
	QueueMirror    QueueType = "mirror"
)

type Queue struct {
	ID      QueueID
	SiteID  SiteID
	Name    string
	Seq     int
	Type    QueueType
	Group   string
	Enabled bool
	Frozen  bool
}

// ---- Executor (staff) -------------------------------------------------------

type ExecutorState string

const (
	StateStandby            ExecutorState = "standby"
	StateAvailable          ExecutorState = "available"
	StateExecuting          ExecutorState = "executing"
	StateDesignatedOccupied ExecutorState = "designated_occupied"
	StatePinned             ExecutorState = "pinned"
	StateTimeReserved       ExecutorState = "time_reserved"
	StateOvertime           ExecutorState = "overtime"
	StateDisabled           ExecutorState = "disabled"
	StateLeave              ExecutorState = "leave"
	StateOffDuty            ExecutorState = "off_duty"
	StateCrossGroupSupport  ExecutorState = "cross_group_support"
)

// Executor is a staff member in the scheduling sense. DutyStatus is the
// on/off switch from ADR-0010; State is the finer scheduling state machine.
type Executor struct {
	ID               ExecutorID
	SiteID           SiteID
	DisplayName      string
	Code             string
	SkillTags        []string
	Group            string
	DutyStatus       string
	Status           string // active / inactive
	State            ExecutorState
	StopRotation     bool
	PrimaryQueueID   QueueID
	CurrentEventID   EventID
	TimeReservations []TimeWindow
}

// IsDispatchable reports whether the executor may receive a task. A pinned
// executor still waits and can be dispatched; the pin only fixes position in
// ONE queue (C-04). DutyStatus must be on and employment status active.
func (e Executor) IsDispatchable() bool {
	if e.DutyStatus != "on" || e.Status != "active" || e.CurrentEventID != "" {
		return false
	}
	switch e.State {
	case StateAvailable, StateStandby, StateCrossGroupSupport, StatePinned:
		return true
	default:
		return false
	}
}

// ---- Queue membership ------------------------------------------------------

type PositionSource string

const (
	PositionAuto      PositionSource = "auto"
	PositionManual    PositionSource = "manual"
	PositionPin       PositionSource = "pin"
	PositionRestored  PositionSource = "restored"
	PositionInherited PositionSource = "inherited"
)

type PinType string

const (
	PinNone        PinType = "none"
	PinPermanent   PinType = "permanent"
	PinTimed       PinType = "timed"
	PinConditional PinType = "conditional"
)

// Pin keeps an executor at the head of ONE queue. Per-queue (C-04); no reason
// and no permission fields (C-02).
type Pin struct {
	Type          PinType
	EffectiveFrom time.Time
	ExpiresAt     *time.Time
}

func (p Pin) Active(t time.Time) bool {
	if p.Type == PinNone || p.Type == "" {
		return false
	}
	if p.ExpiresAt != nil && !t.Before(*p.ExpiresAt) {
		return false
	}
	return !t.Before(p.EffectiveFrom)
}

type MemberStatus string

const (
	MemberActive    MemberStatus = "active"
	MemberSuspended MemberStatus = "suspended"
)

// QueueMember is the executor × queue relation. Position is dense 1..n within
// its own queue, stored on queue_staff.position (ADR-0010).
type QueueMember struct {
	SiteID     SiteID
	QueueID    QueueID
	ExecutorID ExecutorID
	Position   int
	Source     PositionSource
	IsPrimary  bool
	Pin        Pin
	Status     MemberStatus
}

// ---- Task type & assignment method -----------------------------------------

type TaskType struct {
	ID         string
	Name       string
	Group      string
	NoRotation bool
}

type AssignmentMethod string

const (
	MethodRotational AssignmentMethod = "rotational"
	MethodDesignated AssignmentMethod = "designated"
	MethodReassigned AssignmentMethod = "reassigned"
	MethodDelayed    AssignmentMethod = "delayed"
	MethodSelected   AssignmentMethod = "selected"
	MethodCalled     AssignmentMethod = "called"
)

// ---- Decision & linkage ----------------------------------------------------

type Decision string

const (
	DecisionRotate Decision = "rotate"
	DecisionHold   Decision = "hold"
)

type PropagationMode string

const (
	ModeIndependent PropagationMode = "independent"
	ModeAll         PropagationMode = "all"
	ModeSameGroup   PropagationMode = "same_group"
	ModeAllowlist   PropagationMode = "allowlist"
	ModeExclude     PropagationMode = "exclude"
	ModeFrozen      PropagationMode = "frozen"
)

type QueueAction string

const (
	ActionMoveToTail QueueAction = "move_to_tail"
	ActionRestore    QueueAction = "restore"
	ActionInsertAt   QueueAction = "insert_at"
	ActionSuspend    QueueAction = "suspend"
	ActionRemove     QueueAction = "remove"
)

// LinkagePolicy is bound to (site, primary queue). Linkage is always
// single-direction: a secondary queue never triggers another round (FR-27).
type LinkagePolicy struct {
	SiteID         SiteID
	PrimaryQueueID QueueID
	Mode           PropagationMode
	Allowlist      []QueueID
	Exclude        []QueueID
	Group          string
	ActionMap      map[QueueID]QueueAction
	InsertAt       map[QueueID]int
}

// ---- Dispatch, changes & snapshots -----------------------------------------

type DispatchEvent struct {
	ID               EventID
	SiteID           SiteID
	ExecutorID       ExecutorID
	SourceQueueID    QueueID
	TaskTypeID       string
	AssignmentMethod AssignmentMethod
	Operator         string
}

type PositionChange struct {
	QueueID     QueueID    `json:"queue_id"`
	ExecutorID  ExecutorID `json:"executor_id"`
	OldPosition int        `json:"old_position"`
	NewPosition int        `json:"new_position"`
	Action      string     `json:"action"`
	Source      string     `json:"source"`
}

type DecisionSnapshot struct {
	EventID EventID
	SiteID  SiteID
	Before  map[QueueID][]QueueMember
	After   map[QueueID][]QueueMember
}

type ChainStep struct {
	RuleID   RuleID     `json:"rule_id"`
	Type     RuleType   `json:"type"`
	Scope    ScopeLevel `json:"scope"`
	Hit      bool       `json:"hit"`
	Decision Decision   `json:"decision"`
	Reason   string     `json:"reason"`
}

type DecisionRecord struct {
	EventID       EventID          `json:"event_id"`
	SiteID        SiteID           `json:"site_id"`
	SourceQueueID QueueID          `json:"source_queue_id"`
	Decision      Decision         `json:"decision"`
	HitRuleID     RuleID           `json:"hit_rule_id"`
	HitRuleType   RuleType         `json:"hit_rule_type"`
	HitScope      ScopeLevel       `json:"hit_scope"`
	Chain         []ChainStep      `json:"chain"`
	FellBack      bool             `json:"fell_back"`
	Changes       []PositionChange `json:"changes"`
	Explain       string           `json:"explain"`
	Operator      string           `json:"operator"`
}

func (r DecisionRecord) ExplainText() string {
	if r.Decision == DecisionRotate {
		return "all enabled steps passed; rotation"
	}
	for _, s := range r.Chain {
		if s.Hit {
			return string(s.Type) + ": " + s.Reason
		}
	}
	return "held"
}
