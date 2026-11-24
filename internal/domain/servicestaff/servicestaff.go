// Package servicestaff holds the pure rules shared by qualification grants and
// order-line executor assignment (ADR-0023). It has no database or API
// dependency and is safe to unit test without a store.
package servicestaff

import "sort"

const (
	ItemTypeService = "service"
	ItemTypeProduct = "product"
)

// RequiresStaff reports whether an order line of this catalog item type must
// carry an executor before the order can be submitted. Only service lines do;
// product lines never require or accept one.
func RequiresStaff(itemType string) bool { return itemType == ItemTypeService }

// CanAssign reports whether a staff member may be attached to a line of the
// given type. A missing staff member is always fine at assignment time: the
// mandatory check happens at submit (ADR-0023 §3).
func CanAssign(itemType, staffPublicID string) bool {
	if staffPublicID == "" {
		return true
	}
	return RequiresStaff(itemType)
}

// NormalizeItemIDs trims, drops empty entries, de-duplicates and sorts a grant
// list. The caller uses the result for a full replacement, so two identical
// requests produce the same stored set and the same idempotency fingerprint is
// meaningful. Sorting is by the public_id text so the order is stable.
func NormalizeItemIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := trim(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func trim(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}
