package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/WeijianHuang/porch-os/internal/domain/queue"
	"github.com/WeijianHuang/porch-os/internal/domain/scheduling"
)

// StaffSummary is the flat projection `GET /staff` needs (ADR-0015 §3.1): the
// staff row plus the scheduling state, the rotation switch and every queue
// membership with its position. One list call is enough to render the card and
// its menu — no per-card detail request (DA-01).
type StaffSummary struct {
	Staff
	State          string                `json:"state"`
	StopRotation   bool                  `json:"stop_rotation"`
	PrimaryQueueID string                `json:"primary_queue_id"`
	Queues         []queue.StaffQueueRef `json:"queues"`
	// ADR-0017: current card and the reserved order-domain remaining minutes.
	IDCard                  *CardRef `json:"id_card"`
	ServiceRemainingMinutes *int     `json:"service_remaining_minutes"`
	// ADR-0023: qualified service catalog items (public_ids).
	ServiceItemIDs []string `json:"service_item_ids"`
}

const staffSummarySelect = `
	SELECT st.public_id::text, st.code, st.alias, st.gender, st.phone, st.display_name, st.status, st.duty_status,
	       st.version, st.created_at, st.updated_at, st.id,
	       st.executor_state, st.stop_rotation, COALESCE(pq.public_id::text,''),
	       sc.public_id::text, sc.code, sc.name,
	       stpl.public_id::text, stpl.name, to_char(stpl.start_time,'HH24:MI'), to_char(stpl.end_time,'HH24:MI')
	FROM staff st
	LEFT JOIN queues pq ON pq.id = st.primary_queue_id AND pq.shop_id = st.shop_id
	LEFT JOIN staff_categories sc ON sc.id = st.category_id AND sc.shop_id = st.shop_id
	LEFT JOIN shift_templates stpl ON stpl.id = st.shift_template_id AND stpl.shop_id = st.shop_id`

func scanStaffSummary(row pgx.Row) (StaffSummary, error) {
	var v StaffSummary
	var catPub, catCode, catName *string
	var shiftPub, shiftName, shiftStart, shiftEnd *string
	err := row.Scan(&v.PublicID, &v.Code, &v.Alias, &v.Gender, &v.Phone, &v.DisplayName, &v.Status, &v.DutyStatus,
		&v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID,
		&v.State, &v.StopRotation, &v.PrimaryQueueID,
		&catPub, &catCode, &catName,
		&shiftPub, &shiftName, &shiftStart, &shiftEnd)
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	v.Queues = []queue.StaffQueueRef{}
	v.ServiceItemIDs = []string{}
	if catPub != nil && catCode != nil && catName != nil {
		v.Category = &StaffCategoryRef{PublicID: *catPub, Code: *catCode, Name: *catName}
	}
	if shiftPub != nil && shiftName != nil && shiftStart != nil && shiftEnd != nil {
		v.ShiftTemplate = &ShiftTemplateRef{
			PublicID: *shiftPub, Name: *shiftName,
			StartTime: *shiftStart, EndTime: *shiftEnd,
		}
	}
	return v, err
}

// ListStaffSummaries returns every staff member of the shop with state and
// memberships. Two queries, folded in memory (no N+1).
func (s *Store) ListStaffSummaries(ctx context.Context, q DBTX, shopID int64) ([]StaffSummary, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, staffSummarySelect+
		` WHERE st.shop_id=$1 ORDER BY st.display_name, st.code`, shopID)
	if err != nil {
		return nil, err
	}
	out := make([]StaffSummary, 0)
	for rows.Next() {
		v, err := scanStaffSummary(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	memberships, err := s.Memberships(ctx, q, shopID)
	if err != nil {
		return nil, err
	}
	byStaff := queue.MembershipsByStaff(memberships)
	for i := range out {
		if list, ok := byStaff[out[i].PublicID]; ok {
			out[i].Queues = list
		}
	}
	cards, err := CurrentCardByStaff(ctx, q, shopID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if ref, ok := cards[out[i].PublicID]; ok {
			out[i].IDCard = &ref
		}
	}
	qualified, err := s.ServiceItemsByStaff(ctx, q, shopID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if list, ok := qualified[out[i].PublicID]; ok {
			out[i].ServiceItemIDs = list
		}
	}
	return out, nil
}

// StaffSummaryByPublicID returns one staff member with the same projection.
func (s *Store) StaffSummaryByPublicID(ctx context.Context, q DBTX, shopID int64, publicID string) (StaffSummary, error) {
	if q == nil {
		q = s.Pool
	}
	v, err := scanStaffSummary(q.QueryRow(ctx, staffSummarySelect+
		` WHERE st.shop_id=$1 AND st.public_id::text=$2`, shopID, publicID))
	if err != nil {
		return StaffSummary{}, err
	}
	memberships, err := s.Memberships(ctx, q, shopID)
	if err != nil {
		return StaffSummary{}, err
	}
	if list, ok := queue.MembershipsByStaff(memberships)[v.PublicID]; ok {
		v.Queues = list
	}
	qualified, err := s.ServiceItemIDs(ctx, q, shopID, v.ID)
	if err != nil {
		return StaffSummary{}, err
	}
	v.ServiceItemIDs = qualified
	return v, nil
}

// Memberships returns every queue membership of the shop with its position and
// pin type, as flat rows for the pure fold in the queue domain.
func (s *Store) Memberships(ctx context.Context, q DBTX, shopID int64) ([]queue.Membership, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT st.public_id::text, q.public_id::text, q.name, q.seq, qs.position,
		       COALESCE(qs.pin_type,'none')
		FROM queue_staff qs
		JOIN queues q ON q.id=qs.queue_id AND q.shop_id=qs.shop_id
		JOIN staff st ON st.id=qs.staff_id AND st.shop_id=qs.shop_id
		WHERE qs.shop_id=$1 ORDER BY q.seq, qs.position`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]queue.Membership, 0)
	for rows.Next() {
		var m queue.Membership
		if err := rows.Scan(&m.StaffPublicID, &m.QueuePublicID, &m.QueueName,
			&m.QueueSeq, &m.Position, &m.PinType); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- Manual state / rotation / position control (ADR-0015) ------------------

type StaffStateInput struct {
	StaffPub string
	Target   scheduling.ExecutorState
	Reason   string
	Version  int // 0 = skip the optimistic check
	ActorID  int64
}

// SetStaffState applies an operator-driven state change. The row is locked so
// two operators cannot interleave updates; transitions come from the manual
// policy in the domain, never from the engine's automatic table.
func (s *Store) SetStaffState(ctx context.Context, tx pgx.Tx, shopID int64, in StaffStateInput) (StaffSummary, error) {
	var id int64
	var status, duty, state string
	var version int
	err := tx.QueryRow(ctx, `
		SELECT id, status, duty_status, executor_state, version FROM staff
		WHERE shop_id=$1 AND public_id::text=$2 FOR UPDATE`,
		shopID, in.StaffPub).Scan(&id, &status, &duty, &state, &version)
	if err != nil {
		return StaffSummary{}, err
	}
	from := scheduling.ExecutorState(state)
	if err := scheduling.ValidateManualTransition(from, in.Target); err != nil {
		switch {
		case errors.Is(err, scheduling.ErrManualTarget):
			return StaffSummary{}, schedErr("INVALID_ARGUMENT", "该状态不允许手工设置")
		case errors.Is(err, scheduling.ErrStateUnchanged):
			return StaffSummary{}, schedErr("STATE_UNCHANGED", "服务人员已处于该状态")
		case errors.Is(err, scheduling.ErrExecutorBusy):
			return StaffSummary{}, schedErr("EXECUTOR_BUSY", "该服务人员有在途服务，请先完成或取消事件")
		default:
			return StaffSummary{}, schedErr("INVALID_STATE", "当前任职状态不允许该变更")
		}
	}
	nextDuty := scheduling.DutyForState(in.Target)
	if nextDuty == "on" && status != "active" {
		return StaffSummary{}, schedErr("INVALID_STATE", "停用人员不能置为在岗状态")
	}
	if in.Version > 0 && in.Version != version {
		return StaffSummary{}, schedErr("VERSION_CONFLICT", "并发冲突，请重读后重试")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff SET executor_state=$3, duty_status=$4, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id, string(in.Target), nextDuty); err != nil {
		return StaffSummary{}, err
	}
	if err := appendStaffAudit(ctx, tx, shopID, in.ActorID, "staff_state_changed", in.StaffPub,
		map[string]any{
			"from": string(from), "to": string(in.Target),
			"duty_status": nextDuty, "reason": in.Reason,
		}, time.Now().UTC()); err != nil {
		return StaffSummary{}, err
	}
	return s.StaffSummaryByPublicID(ctx, tx, shopID, in.StaffPub)
}

// SetStaffRotation flips the "stop rotation" switch (不动牌 / 恢复轮转).
func (s *Store) SetStaffRotation(ctx context.Context, tx pgx.Tx, shopID int64, staffPub string, stop bool, version int, actorID int64) (StaffSummary, error) {
	var id int64
	var current bool
	var curVersion int
	if err := tx.QueryRow(ctx, `
		SELECT id, stop_rotation, version FROM staff
		WHERE shop_id=$1 AND public_id::text=$2 FOR UPDATE`,
		shopID, staffPub).Scan(&id, &current, &curVersion); err != nil {
		return StaffSummary{}, err
	}
	if current == stop {
		return s.StaffSummaryByPublicID(ctx, tx, shopID, staffPub)
	}
	if version > 0 && version != curVersion {
		return StaffSummary{}, schedErr("VERSION_CONFLICT", "并发冲突，请重读后重试")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff SET stop_rotation=$3, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id, stop); err != nil {
		return StaffSummary{}, err
	}
	if err := appendStaffAudit(ctx, tx, shopID, actorID, "staff_rotation_changed", staffPub,
		map[string]any{"from": current, "to": stop}, time.Now().UTC()); err != nil {
		return StaffSummary{}, err
	}
	return s.StaffSummaryByPublicID(ctx, tx, shopID, staffPub)
}

// MoveQueueStaff moves one member inside a queue and renumbers 1..N in the same
// transaction. It is the single-member shortcut of ReorderQueueStaff, so the
// caller does not need the full member list or the queue version.
func (s *Store) MoveQueueStaff(
	ctx context.Context, tx pgx.Tx, shopID int64,
	queuePub, staffPub, to string, index int, actorID int64,
) (int, error) {
	var queueID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM queues WHERE shop_id=$1 AND public_id::text=$2`,
		shopID, queuePub).Scan(&queueID); err != nil {
		return 0, err
	}
	// One lock per queue: dispatch, pin and reorder already serialise on the
	// same key, so an interleaved move cannot produce duplicate positions.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, $2::int)`, shopID, queueID); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		SELECT st.public_id::text FROM queue_staff qs
		JOIN staff st ON st.id=qs.staff_id AND st.shop_id=qs.shop_id
		WHERE qs.shop_id=$1 AND qs.queue_id=$2 ORDER BY qs.position`, shopID, queueID)
	if err != nil {
		return 0, err
	}
	current := make([]string, 0)
	for rows.Next() {
		var pub string
		if err := rows.Scan(&pub); err != nil {
			rows.Close()
			return 0, err
		}
		current = append(current, pub)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	from := -1
	rest := make([]string, 0, len(current))
	for _, pub := range current {
		if pub == staffPub {
			from = len(rest)
			continue
		}
		rest = append(rest, pub)
	}
	if from < 0 {
		return 0, schedErr("NOT_FOUND", "该服务人员不在此队列")
	}
	var at int
	switch to {
	case "head":
		at = 0
	case "tail":
		at = len(rest)
	case "index":
		at = index - 1
		if at < 0 {
			at = 0
		}
		if at > len(rest) {
			at = len(rest)
		}
	default:
		return 0, schedErr("INVALID_ARGUMENT", "to 必须是 head、tail 或 index")
	}
	ordered := make([]string, 0, len(current))
	ordered = append(ordered, rest[:at]...)
	ordered = append(ordered, staffPub)
	ordered = append(ordered, rest[at:]...)
	if err := applyQueueOrder(ctx, tx, shopID, queueID, ordered); err != nil {
		return 0, err
	}
	toPosition := at + 1
	if err := appendStaffAudit(ctx, tx, shopID, actorID, "staff_moved", staffPub,
		map[string]any{
			"queue_id": queuePub, "to": to, "index": index,
			"from_position": from + 1, "to_position": toPosition,
		}, time.Now().UTC()); err != nil {
		return 0, err
	}
	return toPosition, nil
}

// applyQueueOrder writes positions 1..N. Negative placeholders first, because
// (queue_id, position) is unique and a direct rewrite would collide.
func applyQueueOrder(ctx context.Context, tx pgx.Tx, shopID, queueID int64, ordered []string) error {
	for i, pub := range ordered {
		if _, err := tx.Exec(ctx, `
			UPDATE queue_staff qs SET position=$4
			FROM staff st
			WHERE qs.shop_id=$1 AND qs.queue_id=$2 AND qs.staff_id=st.id
			  AND st.shop_id=$1 AND st.public_id::text=$3`,
			shopID, queueID, pub, -(i + 1)); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		UPDATE queue_staff SET position=-position
		WHERE shop_id=$1 AND queue_id=$2 AND position < 0`, shopID, queueID)
	return err
}

// appendStaffAudit writes one operator action. actor_id is the logged-in user,
// unlike dispatch audits which resolve an actor from the staff row.
func appendStaffAudit(ctx context.Context, tx pgx.Tx, shopID, actorID int64, action, staffPub string, payload map[string]any, at time.Time) error {
	if actorID == 0 {
		return schedErr("AUDIT_ACTOR_MISSING", "no actor for audit")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (shop_id,actor_id,action,target_type,target_id,payload,created_at)
		VALUES ($1,$2,$3,'staff',$4,$5::jsonb,$6)`,
		shopID, actorID, action, staffPub, string(b), at)
	return err
}
