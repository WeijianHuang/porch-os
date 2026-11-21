package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/WeijianHuang/porch-os/internal/domain/scheduling"
	"github.com/jackc/pgx/v5"
)

// This file implements the read side of the scheduling engine and the helpers
// that load an immutable decision context from PostgreSQL. All rows are
// shop-scoped through shop_id (ADR-0003). Generic terms only (C-10).

// ---- Read DTOs ---------------------------------------------------------------

type SchedulingRule struct {
	PublicID          string                        `json:"public_id"`
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
	Version           int                           `json:"version"`
}

type SchedulingPolicy struct {
	PublicID       string                            `json:"public_id"`
	PrimaryQueueID string                            `json:"primary_queue_id"`
	Mode           scheduling.PropagationMode        `json:"mode"`
	Allowlist      []string                          `json:"allowlist"`
	Exclude        []string                          `json:"exclude"`
	Group          string                            `json:"group"`
	ActionMap      map[string]scheduling.QueueAction `json:"action_map"`
	InsertAt       map[string]int                    `json:"insert_at"`
	Version        int                               `json:"version"`
}

type SchedulingTaskType struct {
	PublicID   string `json:"public_id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Group      string `json:"group"`
	NoRotation bool   `json:"no_rotation"`
	Version    int    `json:"version"`
}

type SchedulingEvent struct {
	PublicID    string    `json:"public_id"`
	EventID     string    `json:"event_id"`
	StaffID     string    `json:"staff_id"`
	SourceQueue string    `json:"source_queue_id"`
	Method      string    `json:"method"`
	Decision    string    `json:"decision"`
	HitRule     string    `json:"hit_rule"`
	Explain     string    `json:"explain"`
	Operator    string    `json:"operator"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// BoardMember is one queue membership with executor details, flattened from
// joins (DA-01: no entity graph).
type BoardMember struct {
	QueueID     string `json:"queue_id"`
	ExecutorID  string `json:"executor_id"`
	DisplayName string `json:"display_name"`
	Code        string `json:"code"`
	Position    int    `json:"position"`
	Source      string `json:"source"`
	PinType     string `json:"pin_type"`
	DutyStatus  string `json:"duty_status"`
	State       string `json:"state"`
	Primary     bool   `json:"is_primary"`
	// ADR-0017: current card and the reserved order-domain remaining minutes.
	IDCard                  *CardRef `json:"id_card"`
	ServiceRemainingMinutes *int     `json:"service_remaining_minutes"`
}

type BoardQueue struct {
	PublicID string `json:"public_id"`
	Seq      int    `json:"seq"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Frozen   bool   `json:"frozen"`
	Enabled  bool   `json:"enabled"`
}

type BoardExecutor struct {
	PublicID                string   `json:"public_id"`
	Code                    string   `json:"code"`
	DisplayName             string   `json:"display_name"`
	DutyStatus              string   `json:"duty_status"`
	State                   string   `json:"state"`
	PrimaryQueue            string   `json:"primary_queue_id"`
	StopRotation            bool     `json:"stop_rotation"`
	IDCard                  *CardRef `json:"id_card"`
	ServiceRemainingMinutes *int     `json:"service_remaining_minutes"`
}

type Board struct {
	Queues    []BoardQueue    `json:"queues"`
	Members   []BoardMember   `json:"members"`
	Executors []BoardExecutor `json:"executors"`
}

// ---- Reads -------------------------------------------------------------------

func (s *Store) SchedulingBoard(ctx context.Context, q DBTX, shopID int64) (Board, error) {
	if q == nil {
		q = s.Pool
	}
	b := Board{Queues: []BoardQueue{}, Members: []BoardMember{}, Executors: []BoardExecutor{}}

	// 1. Queues (main queue is the one with the lowest seq, per design/12).
	qrows, err := q.Query(ctx, `
		SELECT public_id::text, seq, name, status FROM queues
		WHERE shop_id=$1 ORDER BY seq, name`, shopID)
	if err != nil {
		return b, err
	}
	var firstQueue string
	for qrows.Next() {
		var pub, name, status string
		var seq int
		if err := qrows.Scan(&pub, &seq, &name, &status); err != nil {
			qrows.Close()
			return b, err
		}
		if firstQueue == "" {
			firstQueue = pub
		}
		b.Queues = append(b.Queues, BoardQueue{
			PublicID: pub, Seq: seq, Name: name, Type: "main",
			Enabled: status == "active",
		})
	}
	qrows.Close()
	if err := qrows.Err(); err != nil {
		return b, err
	}

	// 2. Flat membership rows: one query, no entity graph (DA-01).
	mrows, err := q.Query(ctx, `
		SELECT qs.queue_id, qs.staff_id, qs.position, COALESCE(qs.source,'auto'),
		       COALESCE(qs.pin_type,'none'),
		       st.public_id::text, st.code, st.display_name, st.duty_status,
		       st.executor_state, COALESCE(q.public_id::text,''),
		       COALESCE(st.primary_queue_id = qs.queue_id, false)
		FROM queue_staff qs
		JOIN staff st ON st.id = qs.staff_id AND st.shop_id = qs.shop_id
		JOIN queues q ON q.id = qs.queue_id AND q.shop_id = qs.shop_id
		WHERE qs.shop_id = $1
		ORDER BY q.seq, qs.position`, shopID)
	if err != nil {
		return b, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var v BoardMember
		var qid, sid int64
		if err := mrows.Scan(&qid, &sid, &v.Position, &v.Source, &v.PinType,
			&v.ExecutorID, &v.Code, &v.DisplayName, &v.DutyStatus,
			&v.State, &v.QueueID, &v.Primary); err != nil {
			return b, err
		}
		b.Members = append(b.Members, v)
	}
	if err := mrows.Err(); err != nil {
		return b, err
	}

	// 3. Executors with their primary queue public id.
	erows, err := q.Query(ctx, `
		SELECT st.public_id::text, st.code, st.display_name, st.duty_status,
		       st.executor_state, st.stop_rotation, COALESCE(pq.public_id::text,'')
		FROM staff st
		LEFT JOIN queues pq ON pq.id = st.primary_queue_id AND pq.shop_id = st.shop_id
		WHERE st.shop_id=$1 ORDER BY st.code`, shopID)
	if err != nil {
		return b, err
	}
	defer erows.Close()
	for erows.Next() {
		var v BoardExecutor
		if err := erows.Scan(&v.PublicID, &v.Code, &v.DisplayName, &v.DutyStatus,
			&v.State, &v.StopRotation, &v.PrimaryQueue); err != nil {
			return b, err
		}
		b.Executors = append(b.Executors, v)
	}
	if err := erows.Err(); err != nil {
		return b, err
	}

	// ADR-0017: one extra query folds the current card into every member; no
	// per-row lookup. Remaining minutes stay nil until the order domain lands.
	cards, err := CurrentCardByStaff(ctx, q, shopID)
	if err != nil {
		return b, err
	}
	for i := range b.Members {
		if ref, ok := cards[b.Members[i].ExecutorID]; ok {
			b.Members[i].IDCard = &ref
		}
		for j := range b.Executors {
			if b.Executors[j].PublicID == b.Members[i].ExecutorID {
				if c, ok := cards[b.Executors[j].PublicID]; ok {
					b.Executors[j].IDCard = &c
				}
			}
		}
	}
	return b, nil
}

func (s *Store) SchedulingRules(ctx context.Context, q DBTX, shopID int64) ([]SchedulingRule, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, type, ord, enabled, scope, scope_id, on_hit,
		       time_window, assignments, COALESCE(from_group,''), COALESCE(to_group,''),
		       no_rotation, version
		FROM scheduling_rules WHERE shop_id=$1 ORDER BY ord, id`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchedulingRule{}
	for rows.Next() {
		var v SchedulingRule
		var tw, am []byte
		if err := rows.Scan(&v.PublicID, &v.Type, &v.Order, &v.Enabled, &v.Scope, &v.ScopeID,
			&v.OnHit, &tw, &am, &v.FromGroup, &v.ToGroup, &v.NoRotation, &v.Version); err != nil {
			return nil, err
		}
		if len(tw) > 0 {
			var w scheduling.TimeWindow
			if json.Unmarshal(tw, &w) == nil {
				v.TimeWindow = &w
			}
		}
		_ = json.Unmarshal(am, &v.AssignmentMethods)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SchedulingPolicies(ctx context.Context, q DBTX, shopID int64) ([]SchedulingPolicy, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, primary_queue_id, mode, allowlist, exclude,
		       COALESCE(grp,''), action_map, insert_at, version
		FROM scheduling_policies WHERE shop_id=$1 ORDER BY id`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchedulingPolicy{}
	for rows.Next() {
		var v SchedulingPolicy
		var pq int64
		var allow, excl, am, ia []byte
		if err := rows.Scan(&v.PublicID, &pq, &v.Mode, &allow, &excl, &v.Group, &am, &ia, &v.Version); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(allow, &v.Allowlist)
		_ = json.Unmarshal(excl, &v.Exclude)
		_ = json.Unmarshal(am, &v.ActionMap)
		_ = json.Unmarshal(ia, &v.InsertAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SchedulingTaskTypes(ctx context.Context, q DBTX, shopID int64) ([]SchedulingTaskType, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, code, name, COALESCE(grp,''), no_rotation, version
		FROM scheduling_task_types WHERE shop_id=$1 ORDER BY code`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchedulingTaskType{}
	for rows.Next() {
		var v SchedulingTaskType
		if err := rows.Scan(&v.PublicID, &v.Code, &v.Name, &v.Group, &v.NoRotation, &v.Version); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SchedulingEvents(ctx context.Context, q DBTX, shopID int64) ([]SchedulingEvent, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, event_id, staff_id, source_queue_id, method, decision,
		       COALESCE(hit_rule,''), COALESCE(explain,''), operator, occurred_at
		FROM scheduling_events WHERE shop_id=$1 ORDER BY occurred_at DESC, id DESC LIMIT 200`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchedulingEvent{}
	for rows.Next() {
		var v SchedulingEvent
		var staffID, queueID int64
		if err := rows.Scan(&v.PublicID, &v.EventID, &staffID, &queueID, &v.Method,
			&v.Decision, &v.HitRule, &v.Explain, &v.Operator, &v.OccurredAt); err != nil {
			return nil, err
		}
		v.OccurredAt = utc(v.OccurredAt)
		if err := q.QueryRow(ctx, `SELECT public_id::text FROM staff WHERE id=$1`, staffID).Scan(&v.StaffID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err := q.QueryRow(ctx, `SELECT public_id::text FROM queues WHERE id=$1`, queueID).Scan(&v.SourceQueue); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AuditEntry is a flat projection of audit_logs for the scheduling board.
type AuditEntry struct {
	PublicID  string         `json:"public_id"`
	Action    string         `json:"action"`
	Target    string         `json:"target_type"`
	TargetID  string         `json:"target_id"`
	Payload   map[string]any `json:"payload"`
	Operator  string         `json:"operator"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Store) SchedulingAudit(ctx context.Context, q DBTX, shopID int64, limit int) ([]AuditEntry, error) {
	if q == nil {
		q = s.Pool
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, action, target_type, COALESCE(target_id,''), payload, created_at
		FROM audit_logs WHERE shop_id=$1 AND target_type LIKE 'scheduling.%'
		ORDER BY created_at DESC, id DESC LIMIT $2`, shopID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var v AuditEntry
		var payload []byte
		var targetID string
		if err := rows.Scan(&v.PublicID, &v.Action, &v.Target, &targetID, &payload, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.TargetID = targetID
		v.CreatedAt = utc(v.CreatedAt)
		_ = json.Unmarshal(payload, &v.Payload)
		if v.Payload == nil {
			v.Payload = map[string]any{}
		}
		if op, ok := v.Payload["operator"].(string); ok {
			v.Operator = op
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
