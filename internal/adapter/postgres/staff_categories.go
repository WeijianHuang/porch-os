package postgres

import (
	"context"
	"encoding/json"
	"time"

	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// StaffCategoryRef is the flat category projection carried on staff rows
// (ADR-0024). It has no embedded children and never crosses a shop boundary.
type StaffCategoryRef struct {
	PublicID string `json:"public_id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
}

// StaffCategory is the shop-scoped category definition returned by the
// management endpoints.
type StaffCategory struct {
	StaffCategoryRef
	Status    string `json:"status"`
	SortOrder int    `json:"sort_order"`
	Version   int    `json:"version"`
}

func scanStaffCategory(row pgx.Row) (StaffCategory, error) {
	var v StaffCategory
	err := row.Scan(&v.PublicID, &v.Code, &v.Name, &v.SortOrder, &v.Status, &v.Version)
	return v, err
}

// ListStaffCategories returns every category of the shop, including inactive
// ones, in stable display order.
func (s *Store) ListStaffCategories(ctx context.Context, q DBTX, shopID int64) ([]StaffCategory, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, code, name, sort_order, status, version
		FROM staff_categories WHERE shop_id=$1
		ORDER BY sort_order, code`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StaffCategory, 0)
	for rows.Next() {
		v, err := scanStaffCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// StaffCategory resolves one shop-scoped category by public id.
func (s *Store) StaffCategory(ctx context.Context, q DBTX, shopID int64, publicID string) (StaffCategory, error) {
	if q == nil {
		q = s.Pool
	}
	v, err := scanStaffCategory(q.QueryRow(ctx, `
		SELECT public_id::text, code, name, sort_order, status, version
		FROM staff_categories WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
	if IsNoRows(err) {
		return StaffCategory{}, apierrors.StaffCategoryNotFound
	}
	return v, err
}

// StaffCategoryID returns the internal id needed for the staff foreign key.
func (s *Store) StaffCategoryID(ctx context.Context, q DBTX, shopID int64, publicID string) (int64, error) {
	if q == nil {
		q = s.Pool
	}
	var id int64
	err := q.QueryRow(ctx, `
		SELECT id FROM staff_categories WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID).Scan(&id)
	if IsNoRows(err) {
		return 0, apierrors.StaffCategoryNotFound
	}
	return id, err
}

// AssignableStaffCategory additionally rejects inactive categories. Existing
// assignments to a category that later became inactive are left untouched.
func (s *Store) AssignableStaffCategory(ctx context.Context, q DBTX, shopID int64, publicID string) (StaffCategory, error) {
	v, err := s.StaffCategory(ctx, q, shopID, publicID)
	if err != nil {
		return StaffCategory{}, err
	}
	if v.Status != "active" {
		return StaffCategory{}, apierrors.StaffCategoryInactive
	}
	return v, nil
}

// StaffCategoryForStaff returns the current category of one staff member.
func (s *Store) StaffCategoryForStaff(ctx context.Context, q DBTX, shopID, staffID int64) (*StaffCategoryRef, error) {
	if q == nil {
		q = s.Pool
	}
	var pub, code, name *string
	err := q.QueryRow(ctx, `
		SELECT sc.public_id::text, sc.code, sc.name
		FROM staff st LEFT JOIN staff_categories sc
		  ON sc.id = st.category_id AND sc.shop_id = st.shop_id
		WHERE st.shop_id=$1 AND st.id=$2`, shopID, staffID).Scan(&pub, &code, &name)
	if err != nil {
		return nil, err
	}
	if pub == nil || code == nil || name == nil {
		return nil, nil
	}
	return &StaffCategoryRef{PublicID: *pub, Code: *code, Name: *name}, nil
}

// CreateStaffCategory inserts one shop-scoped category and writes its audit
// in the same transaction.
func (s *Store) CreateStaffCategory(
	ctx context.Context, tx pgx.Tx, shopID int64, code, name string, sortOrder int, actorID int64,
) (StaffCategory, error) {
	var id int64
	var publicID string
	err := tx.QueryRow(ctx, `
		INSERT INTO staff_categories (shop_id, code, name, sort_order)
		VALUES ($1,$2,$3,$4) RETURNING id, public_id::text`, shopID, code, name, sortOrder).
		Scan(&id, &publicID)
	if err != nil {
		return StaffCategory{}, err
	}
	if err := appendCategoryAudit(ctx, tx, shopID, actorID,
		"staff_category_created", publicID, map[string]any{"code": code, "name": name}, time.Now().UTC()); err != nil {
		return StaffCategory{}, err
	}
	return scanStaffCategory(tx.QueryRow(ctx, `
		SELECT public_id::text, code, name, sort_order, status, version
		FROM staff_categories WHERE shop_id=$1 AND id=$2`, shopID, id))
}

// UpdateStaffCategory changes name / order / status with an optimistic lock.
func (s *Store) UpdateStaffCategory(
	ctx context.Context, tx pgx.Tx, shopID int64, publicID string,
	name *string, sortOrder *int, status *string, version int, actorID int64,
) (StaffCategory, error) {
	existing, err := s.StaffCategory(ctx, tx, shopID, publicID)
	if err != nil {
		return StaffCategory{}, err
	}
	nextName, nextOrder, nextStatus := existing.Name, existing.SortOrder, existing.Status
	if name != nil {
		nextName = *name
	}
	if sortOrder != nil {
		nextOrder = *sortOrder
	}
	if status != nil {
		if *status != "active" && *status != "inactive" {
			return StaffCategory{}, apierrors.InvalidArgument
		}
		nextStatus = *status
	}
	tag, err := tx.Exec(ctx, `
		UPDATE staff_categories
		SET name=$3, sort_order=$4, status=$5, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND public_id::text=$2 AND version=$6`,
		shopID, publicID, nextName, nextOrder, nextStatus, version)
	if err != nil {
		return StaffCategory{}, err
	}
	if tag.RowsAffected() == 0 {
		return StaffCategory{}, errVersion
	}
	if err := appendCategoryAudit(ctx, tx, shopID, actorID, "staff_category_updated", publicID,
		map[string]any{"name": nextName, "sort_order": nextOrder, "status": nextStatus}, time.Now().UTC()); err != nil {
		return StaffCategory{}, err
	}
	return scanStaffCategory(tx.QueryRow(ctx, `
		SELECT public_id::text, code, name, sort_order, status, version
		FROM staff_categories WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
}

// RecordStaffCategoryChange writes the audit row for a category reassignment.
// It is called inside the same transaction as the staff row update.
func (s *Store) RecordStaffCategoryChange(
	ctx context.Context, tx pgx.Tx, shopID, actorID int64,
	staffPub, fromPublicID, toPublicID string,
) error {
	return appendCategoryAudit(ctx, tx, shopID, actorID, "staff_category_assigned", staffPub,
		map[string]any{"from": fromPublicID, "to": toPublicID}, time.Now().UTC())
}

func appendCategoryAudit(
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
		VALUES ($1,$2,$3,'staff_category',$4,$5::jsonb,$6)`,
		shopID, actorID, action, publicID, string(b), at)
	return err
}
