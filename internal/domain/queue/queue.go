// Package queue holds pure rules for shop queues and their staff assignments.
// Nothing here reads the database or HTTP state (ADR-0002 / DA-01): folding a
// flat join into per-queue or per-staff views stays a dependency-free function.
package queue

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Ref is the minimal projection embedded in a detail response.
type Ref struct {
	PublicID string `json:"public_id"`
	Name     string `json:"name"`
	Seq      int    `json:"seq"`
}

// StaffRef is the minimal staff projection embedded in a queue detail.
// DutyStatus is the current on/off state; Position is the queue-local order
// (ADR-0010). Both come from flat join columns, never from an entity graph.
type StaffRef struct {
	PublicID    string `json:"public_id"`
	DisplayName string `json:"display_name"`
	DutyStatus  string `json:"duty_status"`
	Position    int    `json:"position"`
}

// Assignment is one flat row from the queue_staff join, carrying both ends.
type Assignment struct {
	QueuePublicID    string
	QueueName        string
	QueueSeq         int
	StaffPublicID    string
	StaffDisplayName string
	StaffDutyStatus  string
	Position         int
}

// StaffQueueRef is one queue a staff member belongs to, with the queue-local
// position and pin marker (ADR-0015). Rendered by the staff card menu.
type StaffQueueRef struct {
	PublicID string `json:"public_id"`
	Name     string `json:"name"`
	Seq      int    `json:"seq"`
	Position int    `json:"position"`
	PinType  string `json:"pin_type"`
}

// Membership is the flat row behind StaffQueueRef.
type Membership struct {
	StaffPublicID string
	QueuePublicID string
	QueueName     string
	QueueSeq      int
	Position      int
	PinType       string
}

// ValidateQueue checks the queue input invariants (ADR-0009 §2).
func ValidateQueue(seq int, name string) error {
	if seq <= 0 {
		return ErrBadSeq
	}
	if strings.TrimSpace(name) == "" {
		return ErrBadName
	}
	return nil
}

// ValidateStaff checks the staff input invariants.
func ValidateStaff(code, displayName string) error {
	if strings.TrimSpace(code) == "" {
		return ErrBadCode
	}
	if strings.TrimSpace(displayName) == "" {
		return ErrBadName
	}
	return nil
}

// ValidateAlias checks the optional display alias (ADR-0026). Aliases are
// display labels only: never an identity, authorization key or snapshot name.
func ValidateAlias(alias string) error {
	if utf8.RuneCountInString(strings.TrimSpace(alias)) > 60 {
		return ErrBadAlias
	}
	return nil
}

// ValidateGender checks the three-valued staff gender label (ADR-0027).
// 'unknown' is the explicit sentinel for "not provided", never NULL.
func ValidateGender(gender string) error {
	switch gender {
	case "unknown", "male", "female":
		return nil
	default:
		return ErrBadGender
	}
}

// ValidatePhone checks the optional staff contact phone (ADR-0027). Empty or
// whitespace-only input is valid. Otherwise it accepts 6-15 digits with an
// optional leading '+' and spaces, '-' or parentheses as separators; no
// region-specific number plan is assumed.
func ValidatePhone(phone string) error {
	value := strings.TrimSpace(phone)
	if value == "" {
		return nil
	}
	digits, plus := 0, false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' && !plus && digits == 0:
			plus = true
		case r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return ErrBadPhone
		}
	}
	if digits < 6 || digits > 15 {
		return ErrBadPhone
	}
	return nil
}

// ValidateDuty checks the current duty state against the employment status.
// A disabled staff member cannot be on duty (ADR-0010 §1).
func ValidateDuty(status, duty string) error {
	if status != "active" && status != "inactive" {
		return ErrBadStatus
	}
	if duty != "on" && duty != "off" {
		return ErrBadDuty
	}
	if status == "inactive" && duty == "on" {
		return ErrDutyConflict
	}
	return nil
}

// ValidateOrder checks that desired is exactly the current member set in a
// new order: no duplicates, no additions, no removals (ADR-0010 §2).
func ValidateOrder(current, desired []string) error {
	members := make(map[string]struct{}, len(current))
	for _, id := range current {
		members[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(desired))
	for _, id := range desired {
		if _, ok := members[id]; !ok {
			return ErrInvalidOrder
		}
		if _, dup := seen[id]; dup {
			return ErrInvalidOrder
		}
		seen[id] = struct{}{}
	}
	if len(seen) != len(members) {
		return ErrInvalidOrder
	}
	return nil
}

// StaffByQueue folds flat assignment rows into queue -> staff refs, sorted by
// the queue-local position so every replica renders the same order.
func StaffByQueue(rows []Assignment) map[string][]StaffRef {
	out := make(map[string][]StaffRef)
	for _, r := range rows {
		out[r.QueuePublicID] = append(out[r.QueuePublicID], StaffRef{
			PublicID: r.StaffPublicID, DisplayName: r.StaffDisplayName,
			DutyStatus: r.StaffDutyStatus, Position: r.Position,
		})
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			if out[id][i].Position != out[id][j].Position {
				return out[id][i].Position < out[id][j].Position
			}
			return out[id][i].PublicID < out[id][j].PublicID
		})
	}
	return out
}

// QueuesByStaff folds flat assignment rows into staff -> queue refs, sorted by
// the shop-local sequence number then name.
func QueuesByStaff(rows []Assignment) map[string][]Ref {
	out := make(map[string][]Ref)
	for _, r := range rows {
		out[r.StaffPublicID] = append(out[r.StaffPublicID], Ref{
			PublicID: r.QueuePublicID, Name: r.QueueName, Seq: r.QueueSeq,
		})
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			if out[id][i].Seq != out[id][j].Seq {
				return out[id][i].Seq < out[id][j].Seq
			}
			return out[id][i].Name < out[id][j].Name
		})
	}
	return out
}

// SortedQueues returns queues by sequence then name without mutating input.
func SortedQueues(refs []Ref) []Ref {
	out := append([]Ref(nil), refs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// MembershipsByStaff folds flat membership rows into staff -> queue refs,
// ordered by queue sequence then position so the menu lists queues in the same
// order the console shows them.
func MembershipsByStaff(rows []Membership) map[string][]StaffQueueRef {
	out := make(map[string][]StaffQueueRef)
	for _, r := range rows {
		out[r.StaffPublicID] = append(out[r.StaffPublicID], StaffQueueRef{
			PublicID: r.QueuePublicID, Name: r.QueueName, Seq: r.QueueSeq,
			Position: r.Position, PinType: r.PinType,
		})
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			if out[id][i].Seq != out[id][j].Seq {
				return out[id][i].Seq < out[id][j].Seq
			}
			return out[id][i].Position < out[id][j].Position
		})
	}
	return out
}

// Domain validation errors. Handlers map these to INVALID_ARGUMENT.
type validationError string

func (e validationError) Error() string { return string(e) }

const (
	ErrBadSeq       validationError = "seq must be a positive integer"
	ErrBadName      validationError = "name must not be blank"
	ErrBadCode      validationError = "code must not be blank"
	ErrBadAlias     validationError = "alias must be at most 60 characters"
	ErrBadGender    validationError = "gender must be unknown, male or female"
	ErrBadPhone     validationError = "phone must contain 6 to 15 digits"
	ErrBadStatus    validationError = "status must be active or inactive"
	ErrBadDuty      validationError = "duty_status must be on or off"
	ErrDutyConflict validationError = "inactive staff cannot be on duty"
	ErrInvalidOrder validationError = "staff_ids must be the full current member list without duplicates"
)
