package postgres

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/WeijianHuang/porch-os/internal/domain/servicestaff"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// ADR-0023: staff qualifications (which service items a staff member may
// perform) and per-line executor assignment. All rows are shop-scoped; a
// composite FK to staff(shop_id,id) makes cross-shop grants impossible.
// Catalog items stay A-class/global and are referenced by their internal id.

// ServiceItemIDs returns the qualified service item public_ids of one staff
// member, sorted by catalog name. Empty slice, never nil, for JSON stability.
func (s *Store) ServiceItemIDs(ctx context.Context, q DBTX, shopID, staffID int64) ([]string, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT c.public_id::text
		FROM staff_service_items si
		JOIN catalog_items c ON c.id=si.catalog_item_id
		WHERE si.shop_id=$1 AND si.staff_id=$2
		ORDER BY c.name, c.public_id`, shopID, staffID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ServiceItemsByStaff folds all qualifications of a shop in one query, so a
// staff list never issues one request per card (DA-01).
func (s *Store) ServiceItemsByStaff(ctx context.Context, q DBTX, shopID int64) (map[string][]string, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT st.public_id::text, c.public_id::text
		FROM staff_service_items si
		JOIN staff st ON st.id=si.staff_id AND st.shop_id=si.shop_id
		JOIN catalog_items c ON c.id=si.catalog_item_id
		WHERE si.shop_id=$1
		ORDER BY st.public_id, c.name, c.public_id`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var staffPub, itemPub string
		if err := rows.Scan(&staffPub, &itemPub); err != nil {
			return nil, err
		}
		out[staffPub] = append(out[staffPub], itemPub)
	}
	return out, rows.Err()
}

// SetStaffServiceItems replaces the whole qualification set in one
// transaction. Version is the staff row's optimistic lock; the public ids are
// normalized (trim/de-duplicate/sort) by the domain before they reach SQL.
func (s *Store) SetStaffServiceItems(
	ctx context.Context, tx pgx.Tx, shopID, staffID int64, staffPub string,
	version int, ids []string, actorID int64,
) (StaffSummary, error) {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM staff WHERE shop_id=$1 AND id=$2`, shopID, staffID).Scan(&status); err != nil {
		return StaffSummary{}, err
	}
	if status != "active" {
		return StaffSummary{}, apierrors.StaffInactive
	}
	// Resolve every id before writing so a bad item aborts the whole replace.
	itemIDs := make([]int64, 0, len(ids))
	for _, pub := range ids {
		var id int64
		var itemType string
		if err := tx.QueryRow(ctx, `SELECT id, type FROM catalog_items WHERE public_id::text=$1`, pub).
			Scan(&id, &itemType); err != nil {
			if IsNoRows(err) {
				return StaffSummary{}, apierrors.WithDetails(apierrors.InvalidArgument,
					apierrors.Detail{Field: "catalog_item_ids", Reason: "item_not_found"})
			}
			return StaffSummary{}, err
		}
		if itemType != servicestaff.ItemTypeService {
			return StaffSummary{}, apierrors.WithDetails(apierrors.InvalidArgument,
				apierrors.Detail{Field: "catalog_item_ids", Reason: "item_is_not_a_service"})
		}
		itemIDs = append(itemIDs, id)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE staff SET version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3`, shopID, staffID, version)
	if err != nil {
		return StaffSummary{}, err
	}
	if tag.RowsAffected() == 0 {
		return StaffSummary{}, errVersion
	}
	if _, err := tx.Exec(ctx, `DELETE FROM staff_service_items WHERE shop_id=$1 AND staff_id=$2`, shopID, staffID); err != nil {
		return StaffSummary{}, err
	}
	for _, itemID := range itemIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO staff_service_items (shop_id, staff_id, catalog_item_id)
			VALUES ($1,$2,$3)`, shopID, staffID, itemID); err != nil {
			return StaffSummary{}, err
		}
	}
	payload := map[string]any{"catalog_item_ids": ids}
	if err := appendStaffAudit(ctx, tx, shopID, actorID,
		"staff_service_items_changed", staffPub, payload, time.Now().UTC()); err != nil {
		return StaffSummary{}, err
	}
	return s.StaffSummaryByPublicID(ctx, tx, shopID, staffPub)
}

// ResolveQualifiedStaff validates one executor assignment and returns the
// internal staff id plus the name to snapshot. "not found" is 404, inactive is
// 409 and a missing qualification is 422 (ADR-0023 §3).
func (s *Store) ResolveQualifiedStaff(
	ctx context.Context, q DBTX, shopID int64, staffPub string, catalogItemID int64,
) (int64, string, error) {
	if q == nil {
		q = s.Pool
	}
	var id int64
	var name, status string
	if err := q.QueryRow(ctx, `
		SELECT id, display_name, status FROM staff
		WHERE shop_id=$1 AND public_id::text=$2`, shopID, staffPub).
		Scan(&id, &name, &status); err != nil {
		return 0, "", err
	}
	if status != "active" {
		return 0, "", apierrors.StaffInactive
	}
	var qualified bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM staff_service_items
		               WHERE shop_id=$1 AND staff_id=$2 AND catalog_item_id=$3)`,
		shopID, id, catalogItemID).Scan(&qualified); err != nil {
		return 0, "", err
	}
	if !qualified {
		return 0, "", apierrors.StaffNotQualified
	}
	return id, name, nil
}

// ValidateOrderLineStaff is the submit-time gate of ADR-0023: every service
// line must still have an active, qualified executor. Qualification is checked
// again here so a revocation between adding the line and submitting cannot be
// silently ignored. Product lines are never checked.
func (s *Store) ValidateOrderLineStaff(ctx context.Context, q DBTX, shopID, orderID int64) error {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT l.line_no, c.type, l.staff_id, st.status,
		       EXISTS (SELECT 1 FROM staff_service_items si
		               WHERE si.shop_id=l.shop_id AND si.staff_id=l.staff_id
		                 AND si.catalog_item_id=l.catalog_item_id)
		FROM order_lines l
		JOIN catalog_items c ON c.id=l.catalog_item_id
		LEFT JOIN staff st ON st.id=l.staff_id AND st.shop_id=l.shop_id
		WHERE l.shop_id=$1 AND l.order_id=$2
		ORDER BY l.line_no`, shopID, orderID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var lineNo int
		var itemType string
		var staffID sql.NullInt64
		var staffStatus sql.NullString
		var qualified bool
		if err := rows.Scan(&lineNo, &itemType, &staffID, &staffStatus, &qualified); err != nil {
			return err
		}
		if !servicestaff.RequiresStaff(itemType) {
			continue
		}
		detail := apierrors.Detail{Field: "line_no", Reason: strconv.Itoa(lineNo)}
		if !staffID.Valid {
			return apierrors.WithDetails(apierrors.OrderLineStaffMissing, detail)
		}
		if !staffStatus.Valid || staffStatus.String != "active" {
			return apierrors.WithDetails(apierrors.StaffInactive, detail)
		}
		if !qualified {
			return apierrors.WithDetails(apierrors.StaffNotQualified, detail)
		}
	}
	return rows.Err()
}

// SetOrderLineStaff assigns or clears the executor of one line. Clearing sends
// an explicit null; the stored name snapshot is kept for history (ADR-0023 §4).
func (s *Store) SetOrderLineStaff(
	ctx context.Context, tx pgx.Tx, shopID int64, orderPub string, lineNo int,
	staffPub *string, actorID int64,
) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if v.Status != "draft" && v.Status != "open" {
		return v, errInvalidState
	}
	var itemType string
	var catalogItemID int64
	if err := tx.QueryRow(ctx, `
		SELECT c.id, c.type
		FROM order_lines l JOIN catalog_items c ON c.id=l.catalog_item_id
		WHERE l.shop_id=$1 AND l.order_id=$2 AND l.line_no=$3`, shopID, v.ID, lineNo).
		Scan(&catalogItemID, &itemType); err != nil {
		return v, err
	}
	var staffID *int64
	var staffName *string
	if staffPub != nil && *staffPub != "" {
		if !servicestaff.CanAssign(itemType, *staffPub) {
			return v, apierrors.WithDetails(apierrors.InvalidArgument,
				apierrors.Detail{Field: "staff_public_id", Reason: "product_line_cannot_have_staff"})
		}
		id, name, err := s.ResolveQualifiedStaff(ctx, tx, shopID, *staffPub, catalogItemID)
		if err != nil {
			return v, err
		}
		staffID, staffName = &id, &name
	} else if itemType == servicestaff.ItemTypeProduct {
		// Clearing a product line is a no-op but still must not be accepted as
		// a way to attach a person; null simply keeps it person-free.
		staffID, staffName = nil, nil
	} else if staffPub != nil {
		// A service line may explicitly clear its executor while still drafting.
		staffID, staffName = nil, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE order_lines SET staff_id=$4,
		  staff_name_snapshot=COALESCE($5, staff_name_snapshot)
		WHERE shop_id=$1 AND order_id=$2 AND line_no=$3`,
		shopID, v.ID, lineNo, staffID, staffName)
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() == 0 {
		return v, apierrors.NotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET version=version+1, updated_at=now() WHERE shop_id=$1 AND id=$2`,
		shopID, v.ID); err != nil {
		return v, err
	}
	to := ""
	if staffPub != nil {
		to = *staffPub
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "order_line_staff_changed", v.PublicID,
		map[string]any{"line_no": lineNo, "to": to}); err != nil {
		return v, err
	}
	return s.OrderDetail(ctx, tx, shopID, orderPub)
}
