package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/WeijianHuang/porch-os/internal/domain/scheduling"
	"github.com/jackc/pgx/v5"
)

// SchedulingError is a typed engine error mapped by the HTTP layer.
type SchedulingError struct {
	Code string
	Msg  string
}

func (e *SchedulingError) Error() string { return e.Code + ": " + e.Msg }

func schedErr(code, format string, args ...any) error {
	return &SchedulingError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ---- Internal load helpers (all queries go through the same tx) ------------

func loadSite(ctx context.Context, tx pgx.Tx, shopID int64) (scheduling.Site, error) {
	var pub, name string
	if err := tx.QueryRow(ctx,
		`SELECT public_id::text, name FROM shops WHERE id=$1`, shopID).Scan(&pub, &name); err != nil {
		return scheduling.Site{}, err
	}
	// Scheduling knobs live in shop_settings key='scheduling' (design/12 §2);
	// defaults: Asia/Shanghai timezone, rotation enabled (ADR-0004).
	tz := "Asia/Shanghai"
	rotation := true
	var value []byte
	err := tx.QueryRow(ctx,
		`SELECT value FROM shop_settings WHERE shop_id=$1 AND key='scheduling'`, shopID).Scan(&value)
	if err == nil {
		var cfg map[string]any
		if json.Unmarshal(value, &cfg) == nil {
			if s, ok := cfg["timezone"].(string); ok && s != "" {
				tz = s
			}
			if b, ok := cfg["rotation_enabled"].(bool); ok {
				rotation = b
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return scheduling.Site{}, err
	}
	return scheduling.Site{
		ID: scheduling.SiteID(pub), Name: name, Timezone: tz,
		RotationEnabled: rotation, TemplateMode: scheduling.TemplateCopy,
	}, nil
}

// loadQueues loads queues with their group/frozen flags from shop settings or
// defaults. Group is derived from the queue name's stable hash only when not
// configured; for the reference build all queues share one group unless a
// scheduling policy sets it (design/12 §4).
func loadQueues(ctx context.Context, tx pgx.Tx, shopID int64) ([]scheduling.Queue, error) {
	rows, err := tx.Query(ctx, `
		SELECT public_id::text, name, status, seq FROM queues
		WHERE shop_id=$1 ORDER BY seq, name`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduling.Queue{}
	for rows.Next() {
		var pub, name, status string
		var seq int
		if err := rows.Scan(&pub, &name, &status, &seq); err != nil {
			return nil, err
		}
		out = append(out, scheduling.Queue{
			ID: scheduling.QueueID(pub), SiteID: scheduling.SiteID(""),
			Name: name, Seq: seq, Type: scheduling.QueueMain,
			Enabled: status == "active", Group: "default",
		})
	}
	return out, rows.Err()
}

func loadExecutor(ctx context.Context, tx pgx.Tx, shopID int64, staffPub string) (scheduling.Executor, error) {
	var id int64
	var pub, code, name, duty, status, state string
	var stop bool
	var primaryPub string
	var reservations []byte
	err := tx.QueryRow(ctx, `
		SELECT st.id, st.public_id::text, st.code, st.display_name, st.duty_status, st.status,
		       st.executor_state, st.stop_rotation, st.time_reservations,
		       COALESCE(pq.public_id::text,'')
		FROM staff st LEFT JOIN queues pq ON pq.id=st.primary_queue_id AND pq.shop_id=st.shop_id
		WHERE st.shop_id=$1 AND st.public_id::text=$2`, shopID, staffPub).
		Scan(&id, &pub, &code, &name, &duty, &status, &state, &stop, &reservations, &primaryPub)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return scheduling.Executor{}, schedErr("EXECUTOR_NOT_FOUND", "executor %s not found", staffPub)
		}
		return scheduling.Executor{}, err
	}
	var windows []scheduling.TimeWindow
	_ = json.Unmarshal(reservations, &windows)
	return scheduling.Executor{
		ID: scheduling.ExecutorID(pub), Code: code, DisplayName: name,
		DutyStatus: duty, Status: status, State: scheduling.ExecutorState(state),
		StopRotation: stop, PrimaryQueueID: scheduling.QueueID(primaryPub),
		TimeReservations: windows, Group: "default",
	}, nil
}

func loadMemberships(ctx context.Context, tx pgx.Tx, shopID int64, staffPub string) ([]scheduling.QueueMember, error) {
	rows, err := tx.Query(ctx, `
		SELECT q.public_id::text, qs.position, COALESCE(qs.source,'auto'),
		       COALESCE(qs.pin_type,'none'), COALESCE(qs.pin_effective_from, TIMESTAMPTZ '2000-01-01 00:00:00+00'), qs.pin_expires_at,
		       COALESCE(st.primary_queue_id = qs.queue_id, false)
		FROM queue_staff qs
		JOIN queues q ON q.id=qs.queue_id AND q.shop_id=qs.shop_id
		JOIN staff st ON st.id=qs.staff_id AND st.shop_id=qs.shop_id
		WHERE qs.shop_id=$1 AND st.public_id::text=$2
		ORDER BY q.seq`, shopID, staffPub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduling.QueueMember{}
	for rows.Next() {
		var pub, source, pinType string
		var pos int
		var eff time.Time
		var exp *time.Time
		var primary bool
		if err := rows.Scan(&pub, &pos, &source, &pinType, &eff, &exp, &primary); err != nil {
			return nil, err
		}
		m := scheduling.QueueMember{
			QueueID: scheduling.QueueID(pub), ExecutorID: scheduling.ExecutorID(staffPub),
			Position: pos, Source: scheduling.PositionSource(source),
			IsPrimary: primary, Status: scheduling.MemberActive,
			Pin: scheduling.Pin{Type: scheduling.PinType(pinType), EffectiveFrom: eff, ExpiresAt: exp},
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func loadMembers(ctx context.Context, tx pgx.Tx, shopID int64, queuePub string) ([]scheduling.QueueMember, error) {
	rows, err := tx.Query(ctx, `
		SELECT st.public_id::text, qs.position, COALESCE(qs.source,'auto'),
		       COALESCE(qs.pin_type,'none'), COALESCE(qs.pin_effective_from, TIMESTAMPTZ '2000-01-01 00:00:00+00'), qs.pin_expires_at,
		       COALESCE(st.primary_queue_id = qs.queue_id, false)
		FROM queue_staff qs
		JOIN staff st ON st.id=qs.staff_id AND st.shop_id=qs.shop_id
		JOIN queues q ON q.id=qs.queue_id AND q.shop_id=qs.shop_id
		WHERE qs.shop_id=$1 AND q.public_id::text=$2
		ORDER BY qs.position`, shopID, queuePub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduling.QueueMember{}
	for rows.Next() {
		var pub, source, pinType string
		var pos int
		var eff time.Time
		var exp *time.Time
		var primary bool
		if err := rows.Scan(&pub, &pos, &source, &pinType, &eff, &exp, &primary); err != nil {
			return nil, err
		}
		out = append(out, scheduling.QueueMember{
			QueueID: scheduling.QueueID(queuePub), ExecutorID: scheduling.ExecutorID(pub),
			Position: pos, Source: scheduling.PositionSource(source), IsPrimary: primary,
			Status: scheduling.MemberActive,
			Pin:    scheduling.Pin{Type: scheduling.PinType(pinType), EffectiveFrom: eff, ExpiresAt: exp},
		})
	}
	return out, rows.Err()
}

func loadRules(ctx context.Context, tx pgx.Tx, shopID int64) ([]scheduling.Rule, error) {
	rows, err := tx.Query(ctx, `
		SELECT public_id::text, type, ord, enabled, scope, scope_id, on_hit,
		       time_window, assignments, COALESCE(from_group,''), COALESCE(to_group,''), no_rotation
		FROM scheduling_rules WHERE shop_id=$1`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduling.Rule{}
	for rows.Next() {
		var r scheduling.Rule
		var tw, am []byte
		if err := rows.Scan(&r.ID, &r.Type, &r.Order, &r.Enabled, &r.Scope, &r.ScopeID,
			&r.OnHit, &tw, &am, &r.FromGroup, &r.ToGroup, &r.NoRotation); err != nil {
			return nil, err
		}
		r.SiteID = scheduling.SiteID("") // filled by caller; scope site rules use scope_id
		if len(tw) > 0 {
			var w scheduling.TimeWindow
			if json.Unmarshal(tw, &w) == nil {
				r.Window = &w
			}
		}
		_ = json.Unmarshal(am, &r.AssignmentMethods)
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadTaskType(ctx context.Context, tx pgx.Tx, shopID int64, code string) (scheduling.TaskType, error) {
	var name, grp string
	var noRot bool
	err := tx.QueryRow(ctx, `
		SELECT name, COALESCE(grp,''), no_rotation FROM scheduling_task_types
		WHERE shop_id=$1 AND public_id::text=$2`, shopID, code).Scan(&name, &grp, &noRot)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return scheduling.TaskType{}, schedErr("TASK_TYPE_NOT_FOUND", "task type %s not found", code)
		}
		return scheduling.TaskType{}, err
	}
	return scheduling.TaskType{ID: code, Name: name, Group: grp, NoRotation: noRot}, nil
}

func loadPolicy(ctx context.Context, tx pgx.Tx, shopID int64, primaryPub string) (scheduling.LinkagePolicy, bool, error) {
	var mode, group string
	var allow, excl, am, ia []byte
	err := tx.QueryRow(ctx, `
		SELECT mode, allowlist, exclude, COALESCE(grp,''), action_map, insert_at
		FROM scheduling_policies WHERE shop_id=$1 AND primary_queue_id IN
		  (SELECT id FROM queues WHERE shop_id=$1 AND public_id::text=$2)`,
		shopID, primaryPub).Scan(&mode, &allow, &excl, &group, &am, &ia)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Default: independent (source only), move to tail.
			return scheduling.LinkagePolicy{
				SiteID: scheduling.SiteID(""), PrimaryQueueID: scheduling.QueueID(primaryPub),
				Mode: scheduling.ModeIndependent,
			}, false, nil
		}
		return scheduling.LinkagePolicy{}, false, err
	}
	p := scheduling.LinkagePolicy{
		SiteID: scheduling.SiteID(""), PrimaryQueueID: scheduling.QueueID(primaryPub),
		Mode: scheduling.PropagationMode(mode), Group: group,
	}
	_ = json.Unmarshal(allow, &p.Allowlist)
	_ = json.Unmarshal(excl, &p.Exclude)
	_ = json.Unmarshal(am, &p.ActionMap)
	_ = json.Unmarshal(ia, &p.InsertAt)
	return p, true, nil
}

// ---- Dispatch ---------------------------------------------------------------

type DispatchInput struct {
	EventID     string
	StaffPub    string
	SourceQueue string
	TaskType    string
	Method      scheduling.AssignmentMethod
	Operator    string
}

// Dispatch runs the full pipeline inside one transaction. The caller (HTTP)
// has already wrapped this in IdempotentWrite, so the tx here is the same one
// that stores the idempotency record: business effects and key commit together.
func (s *Store) Dispatch(ctx context.Context, tx pgx.Tx, shopID int64, in DispatchInput) (scheduling.DecisionRecord, error) {
	var rec scheduling.DecisionRecord
	// Executor-level mutual exclusion via a transaction-scoped advisory lock
	// (ADR-0008: coordination lives in PG only). The lock is released on commit
	// or rollback; a second concurrent dispatch for the same executor waits,
	// then sees executor_state='executing' and is rejected as EXECUTOR_BUSY.
	var staffRowID int64
	if err := tx.QueryRow(ctx,
		`SELECT id FROM staff WHERE shop_id=$1 AND public_id::text=$2`, shopID, in.StaffPub).Scan(&staffRowID); err != nil {
		return rec, schedErr("EXECUTOR_NOT_FOUND", "executor %s not found", in.StaffPub)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, $2::int)`, shopID, staffRowID); err != nil {
		return rec, err
	}
	site, err := loadSite(ctx, tx, shopID)
	if err != nil {
		return rec, schedErr("SITE_NOT_FOUND", "site not found")
	}
	ex, err := loadExecutor(ctx, tx, shopID, in.StaffPub)
	if err != nil {
		return rec, err
	}
	if ex.PrimaryQueueID == "" {
		return rec, schedErr("NO_PRIMARY_QUEUE", "executor %s has no primary queue", in.StaffPub)
	}
	if !ex.IsDispatchable() {
		return rec, schedErr("EXECUTOR_BUSY", "executor %s is not dispatchable", in.StaffPub)
	}
	memberships, err := loadMemberships(ctx, tx, shopID, in.StaffPub)
	if err != nil {
		return rec, err
	}
	sourceFound := false
	for _, m := range memberships {
		if string(m.QueueID) == in.SourceQueue {
			sourceFound = true
		}
	}
	if !sourceFound {
		return rec, schedErr("QUEUE_NOT_FOUND", "source queue %s not in memberships", in.SourceQueue)
	}
	task, err := loadTaskType(ctx, tx, shopID, in.TaskType)
	if err != nil {
		return rec, err
	}
	rules, err := loadRules(ctx, tx, shopID)
	if err != nil {
		return rec, err
	}
	// Site-scope rules compare scope_id against the site public id.
	sitePID := string(site.ID)
	for i := range rules {
		rules[i].SiteID = scheduling.SiteID(sitePID)
		if rules[i].Scope == scheduling.ScopeSite && rules[i].ScopeID == "" {
			rules[i].ScopeID = sitePID
		}
	}
	queues, err := loadQueues(ctx, tx, shopID)
	if err != nil {
		return rec, err
	}
	policy, _, err := loadPolicy(ctx, tx, shopID, string(ex.PrimaryQueueID))
	if err != nil {
		return rec, err
	}
	now := time.Now().UTC()
	dctx := scheduling.DecisionContext{
		Site: site, Executor: ex, Task: task,
		SourceQueueID:    scheduling.QueueID(in.SourceQueue),
		PrimaryQueueID:   ex.PrimaryQueueID,
		AssignmentMethod: in.Method,
		Overtime:         ex.State == scheduling.StateOvertime,
		Now:              now,
	}
	result := scheduling.DecideChain(scheduling.DecisionInput{Context: dctx, Rules: rules})

	// Load all affected memberships by queue and apply linkage in memory first.
	membersByQueue := make(map[scheduling.QueueID][]scheduling.QueueMember)
	for _, m := range memberships {
		list, err := loadMembers(ctx, tx, shopID, string(m.QueueID))
		if err != nil {
			return rec, err
		}
		membersByQueue[m.QueueID] = list
	}
	before := cloneMemberMap(membersByQueue)
	changes := []scheduling.PositionChange{}
	if result.Decision == scheduling.DecisionRotate {
		for _, target := range scheduling.ResolveTargets(policy, scheduling.QueueID(in.SourceQueue), queues, memberships, now) {
			list := membersByQueue[target.QueueID]
			updated, oldPos, newPos, _ := scheduling.ApplyAction(list, ex.ID, target.Action, target.InsertAt, now)
			membersByQueue[target.QueueID] = updated
			changes = append(changes, scheduling.PositionChange{
				QueueID: target.QueueID, ExecutorID: ex.ID,
				OldPosition: oldPos, NewPosition: newPos,
				Action: string(target.Action), Source: "linkage",
			})
		}
	}
	// Persist every touched queue in this transaction.
	qids := make([]scheduling.QueueID, 0, len(membersByQueue))
	for qid := range membersByQueue {
		qids = append(qids, qid)
	}
	sort.Slice(qids, func(i, j int) bool { return qids[i] < qids[j] })
	for _, qid := range qids {
		if err := putMembers(ctx, tx, shopID, string(qid), membersByQueue[qid]); err != nil {
			return rec, err
		}
	}

	// Executor -> executing (validate state machine first).
	if err := scheduling.ValidateTransition(ex.State, scheduling.StateExecuting); err != nil {
		return rec, schedErr("INVALID_STATE", "transition not allowed")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff SET executor_state='executing', updated_at=now()
		WHERE shop_id=$1 AND public_id::text=$2`, shopID, in.StaffPub); err != nil {
		return rec, err
	}

	// Event row + snapshot.
	if _, err := tx.Exec(ctx, `
		INSERT INTO scheduling_events
		  (shop_id,event_id,staff_id,source_queue_id,task_type_id,method,decision,hit_rule,explain,operator,occurred_at)
		VALUES ($1,$2,(SELECT id FROM staff WHERE shop_id=$1 AND public_id::text=$3),
		        (SELECT id FROM queues WHERE shop_id=$1 AND public_id::text=$4),
		        (SELECT id FROM scheduling_task_types WHERE shop_id=$1 AND public_id::text=$5),
		        $6,$7,$8,$9,$10,$11)`,
		shopID, in.EventID, in.StaffPub, in.SourceQueue, in.TaskType,
		string(in.Method), string(result.Decision), string(result.HitRuleID),
		result.Explain(), in.Operator, now); err != nil {
		return rec, err
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(cloneMemberMap(membersByQueue))
	if _, err := tx.Exec(ctx, `
		INSERT INTO scheduling_snapshots (shop_id,event_id,before,after) VALUES ($1,$2,$3,$4)`,
		shopID, in.EventID, beforeJSON, afterJSON); err != nil {
		return rec, err
	}

	// Append-only audit (target_type namespaced for the scheduling board).
	if err := appendSchedulingAudit(ctx, tx, shopID, in.EventID, in.StaffPub,
		string(result.Decision), "decision", in.Operator, map[string]any{
			"decision": string(result.Decision), "hit_rule": string(result.HitRuleID),
			"explain": result.Explain(),
		}, now); err != nil {
		return rec, err
	}

	rec = scheduling.DecisionRecord{
		EventID: scheduling.EventID(in.EventID), SiteID: site.ID,
		SourceQueueID: scheduling.QueueID(in.SourceQueue),
		Decision:      result.Decision, HitRuleID: result.HitRuleID,
		HitRuleType: result.HitRuleType, HitScope: result.HitScope,
		Chain: result.Trace, FellBack: result.FellBack,
		Changes: changes, Explain: result.Explain(), Operator: in.Operator,
	}
	return rec, nil
}

func cloneMemberMap(in map[scheduling.QueueID][]scheduling.QueueMember) map[scheduling.QueueID][]scheduling.QueueMember {
	out := make(map[scheduling.QueueID][]scheduling.QueueMember, len(in))
	for k, v := range in {
		out[k] = append([]scheduling.QueueMember(nil), v...)
	}
	return out
}

func putMembers(ctx context.Context, tx pgx.Tx, shopID int64, queuePub string, members []scheduling.QueueMember) error {
	// Full replacement for one queue: delete rows then re-insert with new
	// positions/pins, all in the same tx. Composite FK keeps it shop-local.
	if _, err := tx.Exec(ctx, `DELETE FROM queue_staff WHERE shop_id=$1 AND queue_id=(SELECT id FROM queues WHERE shop_id=$1 AND public_id::text=$2)`, shopID, queuePub); err != nil {
		return err
	}
	for _, m := range members {
		pinType := m.Pin.Type
		if pinType == "" {
			pinType = scheduling.PinNone // zero-value Pin must still satisfy ck_qs_pin_type
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO queue_staff
			  (shop_id,queue_id,staff_id,position,source,pin_type,pin_effective_from,pin_expires_at)
			VALUES ($1,(SELECT id FROM queues WHERE shop_id=$1 AND public_id::text=$2),
			        (SELECT id FROM staff WHERE shop_id=$1 AND public_id::text=$3),
			        $4,$5,$6,$7,$8)`,
			shopID, queuePub, string(m.ExecutorID), m.Position,
			string(m.Source), string(pinType), m.Pin.EffectiveFrom, m.Pin.ExpiresAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func appendSchedulingAudit(ctx context.Context, tx pgx.Tx, shopID int64, eventID, staffPub, target, action, operator string, payload map[string]any, at time.Time) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["operator"] = operator
	if eventID != "" {
		payload["event_id"] = eventID
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var actorID int64
	if staffPub != "" {
		_ = tx.QueryRow(ctx, `SELECT id FROM staff WHERE shop_id=$1 AND public_id::text=$2`, shopID, staffPub).Scan(&actorID)
	}
	if actorID == 0 {
		// audit_logs.actor_id must be a real user; resolve the shop owner.
		_ = tx.QueryRow(ctx, `SELECT user_id FROM user_shops WHERE shop_id=$1 ORDER BY granted_at LIMIT 1`, shopID).Scan(&actorID)
	}
	if actorID == 0 {
		return schedErr("AUDIT_ACTOR_MISSING", "no actor for audit")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (shop_id,actor_id,action,target_type,target_id,payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`,
		shopID, actorID, action, "scheduling."+target, eventID, string(b), at)
	return err
}

// ---- Pin / unpin (per queue) ------------------------------------------------

type PinInput struct {
	QueuePub  string
	StaffPub  string
	PinType   scheduling.PinType
	ExpiresAt *time.Time
	Operator  string
}

func (s *Store) SetPin(ctx context.Context, tx pgx.Tx, shopID int64, in PinInput) error {
	if in.PinType == scheduling.PinNone {
		return schedErr("INVALID_ARGUMENT", "pin type must not be none")
	}
	now := time.Now().UTC()
	members, err := loadMembers(ctx, tx, shopID, in.QueuePub)
	if err != nil {
		return err
	}
	idx := -1
	for i, m := range members {
		if string(m.ExecutorID) == in.StaffPub {
			idx = i
			break
		}
	}
	if idx < 0 {
		return schedErr("EXECUTOR_NOT_FOUND", "executor not in queue")
	}
	oldPos := members[idx].Position
	members[idx].Pin = scheduling.Pin{
		Type: in.PinType, EffectiveFrom: now, ExpiresAt: in.ExpiresAt,
	}
	members[idx].Source = scheduling.PositionPin
	members = scheduling.Renumber(members, now)
	if err := putMembers(ctx, tx, shopID, in.QueuePub, members); err != nil {
		return err
	}
	return appendSchedulingAudit(ctx, tx, shopID, "", in.StaffPub, in.QueuePub,
		"pin", in.Operator, map[string]any{
			"queue_id": in.QueuePub, "old_position": oldPos,
			"new_position": members[idx].Position, "pin_type": string(in.PinType),
		}, now)
}

func (s *Store) Unpin(ctx context.Context, tx pgx.Tx, shopID int64, queuePub, staffPub, operator string) error {
	now := time.Now().UTC()
	members, err := loadMembers(ctx, tx, shopID, queuePub)
	if err != nil {
		return err
	}
	idx := -1
	for i, m := range members {
		if string(m.ExecutorID) == staffPub {
			idx = i
			break
		}
	}
	if idx < 0 {
		return schedErr("EXECUTOR_NOT_FOUND", "executor not in queue")
	}
	oldPos := members[idx].Position
	members[idx].Pin = scheduling.Pin{Type: scheduling.PinNone}
	members[idx].Source = scheduling.PositionAuto
	members = scheduling.Renumber(members, now)
	if err := putMembers(ctx, tx, shopID, queuePub, members); err != nil {
		return err
	}
	return appendSchedulingAudit(ctx, tx, shopID, "", staffPub, queuePub,
		"unpin", operator, map[string]any{
			"queue_id": queuePub, "old_position": oldPos,
			"new_position": members[idx].Position,
		}, now)
}

func (s *Store) ReleaseExpired(ctx context.Context, tx pgx.Tx, shopID int64, operator string) (int, error) {
	now := time.Now().UTC()
	rows, err := tx.Query(ctx, `
		SELECT q.public_id::text, st.public_id::text, qs.pin_expires_at, st.executor_state
		FROM queue_staff qs
		JOIN queues q ON q.id=qs.queue_id AND q.shop_id=qs.shop_id
		JOIN staff st ON st.id=qs.staff_id AND st.shop_id=qs.shop_id
		WHERE qs.shop_id=$1 AND qs.pin_type<>'none'`, shopID)
	if err != nil {
		return 0, err
	}
	type rel struct {
		queue, staff, state string
		exp                 *time.Time
	}
	items := []rel{}
	for rows.Next() {
		var qp, sp, state string
		var exp *time.Time
		if err := rows.Scan(&qp, &sp, &exp, &state); err != nil {
			rows.Close()
			return 0, err
		}
		expired := exp != nil && !now.Before(*exp)
		unavailable := state == string(scheduling.StateDisabled) ||
			state == string(scheduling.StateOffDuty) || state == string(scheduling.StateLeave)
		if expired || unavailable {
			items = append(items, rel{qp, sp, state, exp})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, it := range items {
		if err := s.Unpin(ctx, tx, shopID, it.queue, it.staff, operator); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

// ---- Restore / cancel / complete / change method ----------------------------

func (s *Store) restoreQueues(ctx context.Context, tx pgx.Tx, shopID int64, eventID, operator string, target map[scheduling.QueueID][]scheduling.QueueMember, action string) error {
	now := time.Now().UTC()
	qids := make([]scheduling.QueueID, 0, len(target))
	for qid := range target {
		qids = append(qids, qid)
	}
	sort.Slice(qids, func(i, j int) bool { return qids[i] < qids[j] })
	for _, qid := range qids {
		if err := putMembers(ctx, tx, shopID, string(qid), target[qid]); err != nil {
			return err
		}
	}
	return appendSchedulingAudit(ctx, tx, shopID, eventID, "", eventID,
		action, operator, map[string]any{"event_id": eventID, "action": action}, now)
}

func (s *Store) loadSnapshot(ctx context.Context, tx pgx.Tx, shopID int64, eventID string) (map[scheduling.QueueID][]scheduling.QueueMember, error) {
	var before []byte
	err := tx.QueryRow(ctx,
		`SELECT before FROM scheduling_snapshots WHERE shop_id=$1 AND event_id=$2`, shopID, eventID).Scan(&before)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, schedErr("SNAPSHOT_MISSING", "no snapshot for event %s", eventID)
		}
		return nil, err
	}
	var out map[scheduling.QueueID][]scheduling.QueueMember
	if err := json.Unmarshal(before, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Restore(ctx context.Context, tx pgx.Tx, shopID int64, eventID, operator string) error {
	target, err := s.loadSnapshot(ctx, tx, shopID, eventID)
	if err != nil {
		return err
	}
	return s.restoreQueues(ctx, tx, shopID, eventID, operator, target, "restore")
}

func (s *Store) CancelTask(ctx context.Context, tx pgx.Tx, shopID int64, eventID, operator string) error {
	target, err := s.loadSnapshot(ctx, tx, shopID, eventID)
	if err != nil {
		return err
	}
	var staffPub string
	if err := tx.QueryRow(ctx,
		`SELECT staff_id FROM (SELECT public_id::text AS staff_id FROM staff WHERE shop_id=$1) s
		 WHERE staff_id IN (SELECT st.public_id::text FROM staff st
		   JOIN scheduling_events se ON se.staff_id=st.id AND se.shop_id=st.shop_id
		   WHERE se.shop_id=$1 AND se.event_id=$2)`, shopID, eventID).Scan(&staffPub); err != nil {
		return err
	}
	ex, err := loadExecutor(ctx, tx, shopID, staffPub)
	if err != nil {
		return err
	}
	if err := scheduling.ValidateTransition(ex.State, scheduling.StateAvailable); err != nil {
		return schedErr("INVALID_STATE", "transition not allowed")
	}
	if err := s.restoreQueues(ctx, tx, shopID, eventID, operator, target, "cancel"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE staff SET executor_state='available', updated_at=now()
		WHERE shop_id=$1 AND public_id::text=$2`, shopID, staffPub)
	return err
}

func (s *Store) CompleteTask(ctx context.Context, tx pgx.Tx, shopID int64, eventID, operator string) error {
	var staffPub string
	if err := tx.QueryRow(ctx, `
		SELECT st.public_id::text FROM scheduling_events se
		JOIN staff st ON st.id=se.staff_id AND st.shop_id=se.shop_id
		WHERE se.shop_id=$1 AND se.event_id=$2`, shopID, eventID).Scan(&staffPub); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return schedErr("EVENT_NOT_FOUND", "event %s not found", eventID)
		}
		return err
	}
	ex, err := loadExecutor(ctx, tx, shopID, staffPub)
	if err != nil {
		return err
	}
	if err := scheduling.ValidateTransition(ex.State, scheduling.StateAvailable); err != nil {
		return schedErr("INVALID_STATE", "transition not allowed")
	}
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE staff SET executor_state='available', updated_at=now()
		WHERE shop_id=$1 AND public_id::text=$2`, shopID, staffPub)
	if err != nil {
		return err
	}
	return appendSchedulingAudit(ctx, tx, shopID, eventID, staffPub,
		eventID, "complete", operator, map[string]any{"event_id": eventID}, now)
}

func (s *Store) ChangeMethod(ctx context.Context, tx pgx.Tx, shopID int64, eventID string, method scheduling.AssignmentMethod, operator string) (bool, error) {
	targetState := scheduling.StateExecuting
	if method == scheduling.MethodDesignated {
		targetState = scheduling.StateDesignatedOccupied
	}
	var staffPub string
	if err := tx.QueryRow(ctx, `
		SELECT st.public_id::text FROM scheduling_events se
		JOIN staff st ON st.id=se.staff_id AND st.shop_id=se.shop_id
		WHERE se.shop_id=$1 AND se.event_id=$2`, shopID, eventID).Scan(&staffPub); err != nil {
		return false, schedErr("EVENT_NOT_FOUND", "event %s not found", eventID)
	}
	ex, err := loadExecutor(ctx, tx, shopID, staffPub)
	if err != nil {
		return false, err
	}
	if err := scheduling.ValidateTransition(ex.State, targetState); err != nil {
		return false, schedErr("INVALID_STATE", "transition not allowed")
	}
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE staff SET executor_state=$3, updated_at=now()
		WHERE shop_id=$1 AND public_id::text=$2`, shopID, staffPub, string(targetState))
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE scheduling_events SET method=$3 WHERE shop_id=$1 AND event_id=$2`,
		shopID, eventID, string(method))
	if err != nil {
		return false, err
	}
	if err := appendSchedulingAudit(ctx, tx, shopID, eventID, staffPub,
		eventID, "assignment_method_changed", operator, map[string]any{
			"event_id": eventID, "method": string(method),
		}, now); err != nil {
		return false, err
	}
	return false, nil
}

// ---- Rule / policy publish --------------------------------------------------

type RuleInput struct {
	Type              scheduling.RuleType           `json:"type"`
	Order             int                           `json:"order"`
	Enabled           bool                          `json:"enabled"`
	Scope             scheduling.ScopeLevel         `json:"scope"`
	ScopeID           string                        `json:"scope_id"`
	OnHit             scheduling.Decision           `json:"on_hit"`
	TimeWindow        *scheduling.TimeWindow        `json:"time_window"`
	AssignmentMethods []scheduling.AssignmentMethod `json:"assignment_methods"`
	FromGroup         string                        `json:"from_group"`
	ToGroup           string                        `json:"to_group"`
	NoRotation        bool                          `json:"no_rotation"`
}

func (s *Store) PublishRules(ctx context.Context, tx pgx.Tx, shopID int64, rules []RuleInput, operator string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM scheduling_rules WHERE shop_id=$1`, shopID); err != nil {
		return err
	}
	for _, r := range rules {
		tw, _ := json.Marshal(r.TimeWindow)
		am, _ := json.Marshal(r.AssignmentMethods)
		if _, err := tx.Exec(ctx, `
			INSERT INTO scheduling_rules
			  (shop_id,type,ord,enabled,scope,scope_id,on_hit,time_window,assignments,from_group,to_group,no_rotation)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11,$12)`,
			shopID, string(r.Type), r.Order, r.Enabled, string(r.Scope), r.ScopeID,
			string(r.OnHit), nullIfEmptyJSON(tw), string(am), r.FromGroup, r.ToGroup, r.NoRotation); err != nil {
			return err
		}
	}
	return appendSchedulingAudit(ctx, tx, shopID, "", "", "rules",
		"config_published", operator, map[string]any{"rules": len(rules)}, time.Now().UTC())
}

type PolicyInput struct {
	PrimaryQueueID string                            `json:"primary_queue_id"`
	Mode           scheduling.PropagationMode        `json:"mode"`
	Allowlist      []string                          `json:"allowlist"`
	Exclude        []string                          `json:"exclude"`
	Group          string                            `json:"group"`
	ActionMap      map[string]scheduling.QueueAction `json:"action_map"`
	InsertAt       map[string]int                    `json:"insert_at"`
}

func (s *Store) PublishPolicies(ctx context.Context, tx pgx.Tx, shopID int64, policies []PolicyInput, operator string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM scheduling_policies WHERE shop_id=$1`, shopID); err != nil {
		return err
	}
	for _, p := range policies {
		allow, _ := json.Marshal(p.Allowlist)
		excl, _ := json.Marshal(p.Exclude)
		am, _ := json.Marshal(p.ActionMap)
		ia, _ := json.Marshal(p.InsertAt)
		if _, err := tx.Exec(ctx, `
			INSERT INTO scheduling_policies
			  (shop_id,primary_queue_id,mode,allowlist,exclude,grp,action_map,insert_at)
			VALUES ($1,(SELECT id FROM queues WHERE shop_id=$1 AND public_id::text=$2),$3,
			        $4::jsonb,$5::jsonb,$6,$7::jsonb,$8::jsonb)`,
			shopID, p.PrimaryQueueID, string(p.Mode), string(allow), string(excl),
			p.Group, string(am), string(ia)); err != nil {
			return err
		}
	}
	return appendSchedulingAudit(ctx, tx, shopID, "", "", "policies",
		"config_published", operator, map[string]any{"policies": len(policies)}, time.Now().UTC())
}

func nullIfEmptyJSON(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return string(b)
}
