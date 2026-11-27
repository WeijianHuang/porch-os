// Package shifttemplate holds the pure rules for shop-scoped shift templates
// (ADR-0025). No database, no HTTP, no entity graph (ADR-0002 / DA-01).
package shifttemplate

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var hhmmPattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidateTime accepts only the 24-hour HH:MM wall-clock form stored by the
// `time` columns. Templates repeat daily, so no date and no timezone is used.
func ValidateTime(value string) error {
	if !hhmmPattern.MatchString(value) {
		return fmt.Errorf("time must be HH:MM (00:00-23:59): %q", value)
	}
	return nil
}

// ValidateLeadMinutes bounds the pre-shift check-in offset to one day.
func ValidateLeadMinutes(minutes int) error {
	if minutes < 0 || minutes > 1440 {
		return errors.New("check-in lead minutes must be between 0 and 1440")
	}
	return nil
}

// NormalizeName trims the display name and rejects blank values.
func NormalizeName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("shift template name must not be blank")
	}
	return trimmed, nil
}

// PlanAssignment is the pure diff used by the full-replace assignment endpoint.
// Duplicate IDs are collapsed; members not present in next are removed, new
// members are added. Both results keep a stable order for deterministic audits.
func PlanAssignment(current, next []string) (remove, add []string) {
	cur := uniqueSorted(current)
	nxt := uniqueSorted(next)
	for _, id := range cur {
		if !contains(nxt, id) {
			remove = append(remove, id)
		}
	}
	for _, id := range nxt {
		if !contains(cur, id) {
			add = append(add, id)
		}
	}
	return remove, add
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v != "" {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
