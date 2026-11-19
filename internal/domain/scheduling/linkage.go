package scheduling

import (
	"sort"
	"time"
)

type QueueTarget struct {
	QueueID  QueueID
	Action   QueueAction
	InsertAt int
}

// ResolveTargets maps the propagation modes to concrete queues. Pinned/frozen
// queues are never emitted: their position is forced to hold (FR-26).
func ResolveTargets(
	policy LinkagePolicy,
	source QueueID,
	queues []Queue,
	memberships []QueueMember,
	now time.Time,
) []QueueTarget {
	frozen := make(map[QueueID]bool, len(queues))
	group := make(map[QueueID]string, len(queues))
	for _, q := range queues {
		frozen[q.ID] = q.Frozen || !q.Enabled
		group[q.ID] = q.Group
	}
	candidates := make([]QueueID, 0, len(memberships))
	pinActive := make(map[QueueID]bool, len(memberships))
	for _, m := range memberships {
		candidates = append(candidates, m.QueueID)
		pinActive[m.QueueID] = m.Pin.Active(now)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })

	selected := make(map[QueueID]bool)
	switch policy.Mode {
	case ModeFrozen:
		return nil
	case ModeIndependent:
		selected[source] = true
	case ModeAll:
		for _, id := range candidates {
			selected[id] = true
		}
	case ModeSameGroup:
		g := policy.Group
		if g == "" {
			g = group[source]
		}
		for _, id := range candidates {
			if group[id] == g {
				selected[id] = true
			}
		}
	case ModeAllowlist:
		allowed := toSet(policy.Allowlist)
		for _, id := range candidates {
			if allowed[id] {
				selected[id] = true
			}
		}
	case ModeExclude:
		excluded := toSet(policy.Exclude)
		for _, id := range candidates {
			if !excluded[id] {
				selected[id] = true
			}
		}
	default:
		selected[source] = true
	}
	if policy.Mode != ModeFrozen {
		selected[source] = true
	}

	out := make([]QueueTarget, 0, len(selected))
	for id := range selected {
		if frozen[id] || pinActive[id] {
			continue
		}
		action := ActionMoveToTail
		if a, ok := policy.ActionMap[id]; ok {
			action = a
		}
		out = append(out, QueueTarget{QueueID: id, Action: action, InsertAt: policy.InsertAt[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QueueID < out[j].QueueID })
	return out
}

func toSet(ids []QueueID) map[QueueID]bool {
	m := make(map[QueueID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// ApplyAction recomputes one executor's position using ONLY that queue's own
// length (C-11/FR-23). It never copies the main queue's number.
func ApplyAction(
	members []QueueMember,
	executor ExecutorID,
	action QueueAction,
	insertAt int,
	now time.Time,
) ([]QueueMember, int, int, bool) {
	idx := -1
	for i, m := range members {
		if m.ExecutorID == executor {
			idx = i
			break
		}
	}
	if idx < 0 {
		return members, 0, 0, false
	}
	oldPos := members[idx].Position
	switch action {
	case ActionRestore:
		return Renumber(members, now), oldPos, oldPos, false
	case ActionSuspend:
		out := cloneMembers(members)
		out[idx].Status = MemberSuspended
		return Renumber(out, now), oldPos, oldPos, true
	case ActionRemove:
		out := append(cloneMembers(members[:idx]), members[idx+1:]...)
		return Renumber(out, now), oldPos, 0, true
	case ActionInsertAt:
		out := append(cloneMembers(members[:idx]), members[idx+1:]...)
		pos := insertAt
		if pos < 1 {
			pos = 1
		}
		if pos > len(out)+1 {
			pos = len(out) + 1
		}
		m := members[idx]
		m.Position = pos
		out = append(out, QueueMember{})
		copy(out[pos:], out[pos-1:])
		out[pos-1] = m
		return Renumber(out, now), oldPos, pos, true
	case ActionMoveToTail:
		fallthrough
	default:
		out := append(cloneMembers(members[:idx]), members[idx+1:]...)
		m := members[idx]
		m.Position = len(out) + 1
		out = append(out, m)
		return Renumber(out, now), oldPos, m.Position, true
	}
}

// Renumber produces dense 1..n positions. Active pins keep the head, ordered
// by effective time then executor ID (FR-11).
func Renumber(members []QueueMember, now time.Time) []QueueMember {
	pinned := make([]QueueMember, 0)
	rest := make([]QueueMember, 0, len(members))
	for _, m := range members {
		if m.Pin.Active(now) {
			pinned = append(pinned, m)
		} else {
			rest = append(rest, m)
		}
	}
	sort.SliceStable(pinned, func(i, j int) bool {
		if !pinned[i].Pin.EffectiveFrom.Equal(pinned[j].Pin.EffectiveFrom) {
			return pinned[i].Pin.EffectiveFrom.Before(pinned[j].Pin.EffectiveFrom)
		}
		return pinned[i].ExecutorID < pinned[j].ExecutorID
	})
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Position < rest[j].Position })
	out := make([]QueueMember, 0, len(members))
	for i, m := range pinned {
		m.Position = i + 1
		out = append(out, m)
	}
	for _, m := range rest {
		m.Position = len(out) + 1
		out = append(out, m)
	}
	return out
}

func cloneMembers(in []QueueMember) []QueueMember {
	out := make([]QueueMember, len(in))
	copy(out, in)
	return out
}
