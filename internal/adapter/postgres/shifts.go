package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type Shift struct {
	PublicID string     `json:"public_id"`
	ShiftNo  string     `json:"shift_no"`
	Status   string     `json:"status"`
	OpenedAt time.Time  `json:"opened_at"`
	ClosedAt *time.Time `json:"closed_at"`
	ID       int64      `json:"-"`
}

func scanShift(row pgx.Row) (Shift, error) {
	var v Shift
	var closed *time.Time
	err := row.Scan(&v.PublicID, &v.ShiftNo, &v.Status, &v.OpenedAt, &closed, &v.ID)
	if closed != nil {
		c := utc(*closed)
		v.ClosedAt = &c
	}
	v.OpenedAt = utc(v.OpenedAt)
	return v, err
}

// CurrentShift returns the shop's open shift (there is at most one, enforced
// by a partial unique index).
func (s *Store) CurrentShift(ctx context.Context, q DBTX, shopID int64) (Shift, error) {
	if q == nil {
		q = s.Pool
	}
	return scanShift(q.QueryRow(ctx, `
		SELECT public_id::text, shift_no, status, opened_at, closed_at, id
		FROM shifts WHERE shop_id=$1 AND status='open'`, shopID))
}

// OpenShift creates a new open shift. The partial unique index rejects a
// second open shift in the same shop (mapped to CONFLICT).
func (s *Store) OpenShift(ctx context.Context, tx pgx.Tx, shopID int64) (Shift, error) {
	var no string
	if err := tx.QueryRow(ctx, `
		SELECT 'SH' || to_char(business_date(now(), $1),'YYYYMMDD') || '-'
		       || lpad(nextval('order_no_seq')::text, 10, '0')`, shopID).Scan(&no); err != nil {
		return Shift{}, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO shifts (shop_id, shift_no, status) VALUES ($1,$2,'open') RETURNING id`,
		shopID, no).Scan(&id); err != nil {
		return Shift{}, err
	}
	return scanShift(tx.QueryRow(ctx, `
		SELECT public_id::text, shift_no, status, opened_at, closed_at, id FROM shifts WHERE id=$1`, id))
}

// CloseShift closes an open shift (open → closing → closed in one step; there
// is no async settlement in this release).
func (s *Store) CloseShift(ctx context.Context, tx pgx.Tx, shopID int64, publicID string) (Shift, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE shifts SET status='closed', closed_at=now()
		WHERE shop_id=$1 AND public_id=$2::uuid AND status IN ('open','closing')`, shopID, publicID)
	if err != nil {
		return Shift{}, err
	}
	if tag.RowsAffected() == 0 {
		return Shift{}, errInvalidState
	}
	return scanShift(tx.QueryRow(ctx, `
		SELECT public_id::text, shift_no, status, opened_at, closed_at, id
		FROM shifts WHERE shop_id=$1 AND public_id=$2::uuid`, shopID, publicID))
}
