// Package shop holds pure rules for the visible shop set and current-shop
// selection. Nothing here reads the database or HTTP state: ADR-0006 defines
// the current shop as a request-scoped client context, never a stored entity.
package shop

import "sort"

// Visible is the minimal projection returned by /me/shops and login.
type Visible struct {
	PublicID      string
	Name          string
	Status        string
	GrantedAtUnix int64
}

// DefaultID returns the earliest-granted active shop, or "" for an empty set.
// Ties are broken by public_id so the result is deterministic across replicas.
func DefaultID(shops []Visible) string {
	var best *Visible
	for i := range shops {
		v := &shops[i]
		if v.Status != "active" {
			continue
		}
		if best == nil || v.GrantedAtUnix < best.GrantedAtUnix ||
			(v.GrantedAtUnix == best.GrantedAtUnix && v.PublicID < best.PublicID) {
			best = v
		}
	}
	if best == nil {
		return ""
	}
	return best.PublicID
}

// Contains reports whether publicID is in the visible set. Callers pass only
// rows from user_shops, which is the single source of truth (ADR-0003/0006).
func Contains(shops []Visible, publicID string) bool {
	for _, v := range shops {
		if v.PublicID == publicID && v.Status == "active" {
			return true
		}
	}
	return false
}

// SortedByGrant returns a stable copy for API responses.
func SortedByGrant(shops []Visible) []Visible {
	out := append([]Visible(nil), shops...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].GrantedAtUnix != out[j].GrantedAtUnix {
			return out[i].GrantedAtUnix < out[j].GrantedAtUnix
		}
		return out[i].PublicID < out[j].PublicID
	})
	return out
}
