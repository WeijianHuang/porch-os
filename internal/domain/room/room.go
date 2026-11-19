// Package room holds pure rules for shop rooms and their floor grouping.
// Nothing here reads the database or HTTP state (ADR-0002 / DA-01): folding
// a flat room list into floor groups stays a dependency-free function.
package room

import (
	"sort"
	"strings"
)

// FloorMin and FloorMax are the validation bounds for the floor scalar
// (ADR-0011 §2). They are data-validation limits, not a shop configuration.
const (
	FloorMin = -5
	FloorMax = 200
)

// Flat is one shop-scoped room row from a flat query (no entity graph).
type Flat struct {
	PublicID string `json:"public_id"`
	Floor    int    `json:"floor"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

// Group is one floor partition produced by GroupByFloor.
type Group struct {
	Floor int   `json:"floor"`
	Rooms []Flat `json:"rooms"`
}

// Validate checks the room input invariants (ADR-0011 §2 / design/11 §2).
func Validate(floor int, code, name string) error {
	if floor < FloorMin || floor > FloorMax {
		return ErrBadFloor
	}
	if strings.TrimSpace(code) == "" {
		return ErrBadCode
	}
	if strings.TrimSpace(name) == "" {
		return ErrBadName
	}
	return nil
}

// ValidStatus reports whether s is a legal room status.
func ValidStatus(s string) bool { return s == "active" || s == "inactive" }

// FloorLabel formats a floor number for display only (ADR-0011 §4). The label
// never round-trips to the backend; floor itself stays an integer.
func FloorLabel(floor int) string {
	switch {
	case floor < 0:
		return "B" + itoa(-floor)
	case floor == 0:
		return "GF"
	default:
		return itoa(floor) + "F"
	}
}

// GroupByFloor folds a flat room list into floor groups. Groups are ordered by
// floor descending (higher floors first, basement last); rooms inside a group
// are ordered by code ascending. Input is never mutated.
func GroupByFloor(rows []Flat) []Group {
	byFloor := make(map[int][]Flat, len(rows))
	for _, r := range rows {
		byFloor[r.Floor] = append(byFloor[r.Floor], r)
	}
	floors := make([]int, 0, len(byFloor))
	for f := range byFloor {
		floors = append(floors, f)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(floors)))
	groups := make([]Group, 0, len(floors))
	for _, f := range floors {
		members := byFloor[f]
		sort.Slice(members, func(i, j int) bool {
			if members[i].Code != members[j].Code {
				return members[i].Code < members[j].Code
			}
			return members[i].PublicID < members[j].PublicID
		})
		groups = append(groups, Group{Floor: f, Rooms: members})
	}
	return groups
}

// itoa converts a non-negative int to decimal without importing strconv,
// keeping this package dependency-light for the display helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Domain validation errors. Handlers map these to INVALID_ARGUMENT.
type validationError string

func (e validationError) Error() string { return string(e) }

const (
	ErrBadFloor validationError = "floor must be between -5 and 200"
	ErrBadCode  validationError = "code must not be blank"
	ErrBadName  validationError = "name must not be blank"
)
