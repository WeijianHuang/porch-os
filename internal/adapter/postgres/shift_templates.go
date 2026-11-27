package postgres

import (
	"context"
	"encoding/json"
	"time"

	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// ShiftTemplateRef is the flat projected reference carried on staff rows
// (ADR-0025). It is display metadata only: no permissions, no state machine.
type ShiftTemplateRef struct {
	PublicID  string `json:"public_id"`
	Name      string `json:"name"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

// ShiftTemplate is the shop-scoped template definition returned by the
// management endpoints.
type ShiftTemplate struct {
	ShiftTemplateRef
	SortOrder          int  `json:"sort_order"`
	CheckInLeadMinutes int  `json:"check_in_lead_minutes"`
	NextDayFlag        bool `json:"next_day_flag"`
	Version            int  `json:"version"`
}

const shiftTemplateSelect = `
	SELECT public_id::text, name, to_char(start_time,'HH24:MI'), to_char(end_time,'HH24:MI'),
	       check_in_lead_minutes, next_day_flag, sort_order, version
	FROM shift_templates`

func scanShiftTemplate(row pgx.Row) (ShiftTemplate, error) {
	var v ShiftTemplate
	err := row.Scan(&v.PublicID, &v.Name, &v.StartTime, &v.EndTime,
		&v.CheckInLeadMinutes, &v.NextDayFlag, &v.SortOrder, &v.Version)
	return v, err
}

// ListShiftTemplates returns every template of the shop in display order.
func (s *Store) ListShiftTemplates(ctx context.Context, q DBTX, shopID int64) ([]ShiftTemplate, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, shiftTemplateSelect+
		` WHERE shop_id=$1 ORDER BY sort_order, name`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ShiftTemplate, 0)
	for rows.Next() {
		v, err := scanShiftTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ShiftTemplate resolves one shop-scoped template by public id.
func (s *Store) ShiftTemplate(ctx context.Context, q DBTX, shopID int64, publicID string) (ShiftTemplate, error) {
	if q == nil {
		q = s.Pool
	}
	v, err := scanShiftTemplate(q.QueryRow(ctx, shiftTemplateSelect+
		` WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
	if IsNoRows(err) {
		return ShiftTemplate{}, apierrors.ShiftTemplateNotFound
	}
	return v, err
}

// StaffShiftTemplate returns the current template reference of one staff
// member, or nil when unassigned.
func (s *Store) StaffShiftTemplate(ctx context.Context, q DBTX, shopID, staffID int64) (*ShiftTemplateRef, error) {
	if q == nil {
		q = s.Pool
	}
	var pub, name, start, end *string
	err := q.QueryRow(ctx, `
		SELECT st.public_id::text, st.name, to_char(st.start_time,'HH24:MI'), to_char(st.end_time,'HH24:MI')
		FROM staff s JOIN shift_templates st
		  ON st.id = s.shift_template_id AND st.shop_id = s.shop_id
		WHERE s.shop_id=$1 AND s.id=$2`, shopID, staffID).
		Scan(&pub, &name, &start, &end)
	if err != nil {
		return nil, err
	}
	if pub == nil || name == nil || start == nil || end == nil {
		return nil, nil
	}
	return &ShiftTemplateRef{PublicID: *pub, Name: *name, StartTime: *start, EndTime: *end}, nil
}

// CreateShiftTemplate inserts one template and writes its audit in the same
// transaction.
func (s *Store) CreateShiftTemplate(
	ctx context.Context, tx pgx.Tx, shopID int64,
	name, start, end string, lead, sortOrder int, nextDay bool, actorID int64,
) (ShiftTemplate, error) {
	var id int64
	var publicID string
	err := tx.QueryRow(ctx, `
		INSERT INTO shift_templates (shop_id,name,start_time,end_time,check_in_lead_minutes,next_day_flag,sort_order)
		VALUES ($1,$2,$3::time,$4::time,$5,$6,$7) RETURNING id, public_id::text`,
		shopID, name, start, end, lead, nextDay, sortOrder).Scan(&id, &publicID)
	if err != nil {
		return ShiftTemplate{}, err
	}
	if err := appendShiftTemplateAudit(ctx, tx, shopID, actorID, "shift_template_created",
		publicID, map[string]any{"name": name, "start_time": start, "end_time": end}, time.Now().UTC()); err != nil {
		return ShiftTemplate{}, err
	}
	return scanShiftTemplate(tx.QueryRow(ctx, shiftTemplateSelect+
		` WHERE shop_id=$1 AND id=$2`, shopID, id))
}

// UpdateShiftTemplate merges the supplied fields under an optimistic lock.
func (s *Store) UpdateShiftTemplate(
	ctx context.Context, tx pgx.Tx, shopID int64, publicID string,
	name *string, start, end *string, lead *int, nextDay *bool,
	sortOrder *int, version int, actorID int64,
) (ShiftTemplate, error) {
	existing, err := s.ShiftTemplate(ctx, tx, shopID, publicID)
	if err != nil {
		return ShiftTemplate{}, err
	}
	nextName, nextStart, nextEnd := existing.Name, existing.StartTime, existing.EndTime
	nextLead, nextSort, nextFlag := existing.CheckInLeadMinutes, existing.SortOrder, existing.NextDayFlag
	if name != nil {
		nextName = *name
	}
	if start != nil {
		nextStart = *start
	}
	if end != nil {
		nextEnd = *end
	}
	if lead != nil {
		nextLead = *lead
	}
	if sortOrder != nil {
		nextSort = *sortOrder
	}
	if nextDay != nil {
		nextFlag = *nextDay
	}
	tag, err := tx.Exec(ctx, `
		UPDATE shift_templates
		SET name=$3, start_time=$4::time, end_time=$5::time,
		    check_in_lead_minutes=$6, next_day_flag=$7, sort_order=$8,
		    version=version+1, updated_at=now()
		WHERE shop_id=$1 AND public_id::text=$2 AND version=$9`,
		shopID, publicID, nextName, nextStart, nextEnd, nextLead, nextFlag, nextSort, version)
	if err != nil {
		return ShiftTemplate{}, err
	}
	if tag.RowsAffected() == 0 {
		return ShiftTemplate{}, errVersion
	}
	if err := appendShiftTemplateAudit(ctx, tx, shopID, actorID, "shift_template_updated", publicID,
		map[string]any{"name": nextName, "start_time": nextStart, "end_time": nextEnd}, time.Now().UTC()); err != nil {
		return ShiftTemplate{}, err
	}
	return scanShiftTemplate(tx.QueryRow(ctx, shiftTemplateSelect+
		` WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
}

// AssignShiftTemplateStaff replaces the whole member list in one transaction.
// Because staff.shift_template_id is a scalar, a person assigned elsewhere is
// moved here automatically; "one staff, one shift" is never broken.
func (s *Store) AssignShiftTemplateStaff(
	ctx context.Context, tx pgx.Tx, shopID int64, publicID string,
	staffPublicIDs []string, version int, actorID int64,
) (ShiftTemplate, error) {
	var id int64
	var currentVersion int
	if err := tx.QueryRow(ctx, `
		SELECT id, version FROM shift_templates
		WHERE shop_id=$1 AND public_id::text=$2 FOR UPDATE`, shopID, publicID).
		Scan(&id, &currentVersion); err != nil {
		if IsNoRows(err) {
			return ShiftTemplate{}, apierrors.ShiftTemplateNotFound
		}
		return ShiftTemplate{}, err
	}
	if currentVersion != version {
		return ShiftTemplate{}, errVersion
	}
	from := make([]string, 0)
	rows, err := tx.Query(ctx, `
		SELECT public_id::text FROM staff
		WHERE shop_id=$1 AND shift_template_id=$2 ORDER BY public_id::text`, shopID, id)
	if err != nil {
		return ShiftTemplate{}, err
	}
	for rows.Next() {
		var pub string
		if err := rows.Scan(&pub); err != nil {
			rows.Close()
			return ShiftTemplate{}, err
		}
		from = append(from, pub)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ShiftTemplate{}, err
	}

	to := make([]string, 0, len(staffPublicIDs))
	seen := make(map[string]struct{}, len(staffPublicIDs))
	for _, pub := range staffPublicIDs {
		if pub == "" {
			continue
		}
		if _, ok := seen[pub]; ok {
			continue
		}
		seen[pub] = struct{}{}
		to = append(to, pub)
	}
	if len(to) > 0 {
		var found int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM staff WHERE shop_id=$1 AND public_id::text = ANY($2::text[])`,
			shopID, to).Scan(&found); err != nil {
			return ShiftTemplate{}, err
		}
		if found != len(to) {
			return ShiftTemplate{}, apierrors.NotFound
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff SET shift_template_id=NULL, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND shift_template_id=$2`, shopID, id); err != nil {
		return ShiftTemplate{}, err
	}
	if len(to) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE staff SET shift_template_id=$3, version=version+1, updated_at=now()
			WHERE shop_id=$1 AND public_id::text = ANY($2::text[])`,
			shopID, to, id); err != nil {
			return ShiftTemplate{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE shift_templates SET version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id); err != nil {
		return ShiftTemplate{}, err
	}
	if err := appendShiftTemplateAudit(ctx, tx, shopID, actorID, "shift_template_staff_assigned", publicID,
		map[string]any{"from": from, "to": to}, time.Now().UTC()); err != nil {
		return ShiftTemplate{}, err
	}
	return scanShiftTemplate(tx.QueryRow(ctx, shiftTemplateSelect+
		` WHERE shop_id=$1 AND id=$2`, shopID, id))
}

// DeleteShiftTemplate unbinds every member in the same transaction before
// removing the definition. Staff rows survive with a null template reference.
func (s *Store) DeleteShiftTemplate(
	ctx context.Context, tx pgx.Tx, shopID int64, publicID string, actorID int64,
) error {
	v, err := s.ShiftTemplate(ctx, tx, shopID, publicID)
	if err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM shift_templates WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID).
		Scan(&id); err != nil {
		return err
	}
	members := make([]string, 0)
	rows, err := tx.Query(ctx, `
		SELECT public_id::text FROM staff
		WHERE shop_id=$1 AND shift_template_id=$2 ORDER BY public_id::text`, shopID, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pub string
		if err := rows.Scan(&pub); err != nil {
			rows.Close()
			return err
		}
		members = append(members, pub)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff SET shift_template_id=NULL, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND shift_template_id=$2`, shopID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM shift_templates WHERE shop_id=$1 AND id=$2`, shopID, id); err != nil {
		return err
	}
	return appendShiftTemplateAudit(ctx, tx, shopID, actorID, "shift_template_deleted", v.PublicID,
		map[string]any{"unbound_staff": members}, time.Now().UTC())
}

func appendShiftTemplateAudit(
	ctx context.Context, tx pgx.Tx, shopID, actorID int64,
	action, publicID string, payload map[string]any, at time.Time,
) error {
	if actorID == 0 {
		return apierrors.New("AUDIT_ACTOR_MISSING", "审计缺少操作人")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (shop_id, actor_id, action, target_type, target_id, payload, created_at)
		VALUES ($1,$2,$3,'shift_template',$4,$5::jsonb,$6)`,
		shopID, actorID, action, publicID, string(b), at)
	return err
}
