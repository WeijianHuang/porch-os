package postgres

import (
	"context"
	"time"

	"github.com/Masterminds/squirrel"
	qdomain "github.com/WeijianHuang/porch-os/internal/domain/queue"
	"github.com/jackc/pgx/v5"
)

// Queue is a flat shop-scoped DTO: no embedded entity graph (ADR-0002).
type Queue struct {
	PublicID  string    `json:"public_id"`
	Seq       int       `json:"seq"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ID        int64     `json:"-"`
}

// Staff is a flat shop-scoped DTO.
type Staff struct {
	PublicID    string    `json:"public_id"`
	Code        string    `json:"code"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ID          int64     `json:"-"`
}

const queueCols = "public_id::text, seq, name, status, version, created_at, updated_at, id"
const staffCols = "public_id::text, code, display_name, status, version, created_at, updated_at, id"

func scanQueue(row pgx.Row) (Queue, error) {
	var v Queue
	err := row.Scan(&v.PublicID, &v.Seq, &v.Name, &v.Status, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID)
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, err
}

func scanStaff(row pgx.Row) (Staff, error) {
	var v Staff
	err := row.Scan(&v.PublicID, &v.Code, &v.DisplayName, &v.Status, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID)
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, err
}

// ---- queues ----

func (s *Store) ListQueues(ctx context.Context, q DBTX, shopID int64) ([]Queue, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(queueCols).From("queues").
		Where(squirrel.Eq{"shop_id": shopID}).
		OrderBy("seq ASC", "name ASC").PlaceholderFormat(squirrel.Dollar)
	sql, args, err := b.ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Queue, 0)
	for rows.Next() {
		v, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) QueueByPublicID(ctx context.Context, q DBTX, shopID int64, publicID string) (Queue, error) {
	if q == nil {
		q = s.Pool
	}
	return scanQueue(q.QueryRow(ctx,
		`SELECT `+queueCols+` FROM queues WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
}

func (s *Store) CreateQueue(ctx context.Context, tx pgx.Tx, shopID int64, seq int, name string) (Queue, error) {
	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO queues (shop_id,seq,name) VALUES ($1,$2,$3) RETURNING id`, shopID, seq, name).Scan(&id); err != nil {
		return Queue{}, err
	}
	return scanQueue(tx.QueryRow(ctx, `SELECT `+queueCols+` FROM queues WHERE id=$1`, id))
}

func (s *Store) UpdateQueue(ctx context.Context, tx pgx.Tx, shopID, id int64, seq int, name, status string, version int) (Queue, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE queues SET seq=$3, name=$4, status=$5, version=version+1, updated_at=now()
		WHERE id=$1 AND shop_id=$2 AND version=$6`, id, shopID, seq, name, status, version)
	if err != nil {
		return Queue{}, err
	}
	if tag.RowsAffected() == 0 {
		return Queue{}, pgx.ErrNoRows
	}
	return scanQueue(tx.QueryRow(ctx, `SELECT `+queueCols+` FROM queues WHERE id=$1`, id))
}

func (s *Store) DeleteQueue(ctx context.Context, tx pgx.Tx, shopID, id int64) error {
	_, err := tx.Exec(ctx, `DELETE FROM queues WHERE id=$1 AND shop_id=$2`, id, shopID)
	return err
}

// ---- staff ----

func (s *Store) ListStaff(ctx context.Context, q DBTX, shopID int64) ([]Staff, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(staffCols).From("staff").
		Where(squirrel.Eq{"shop_id": shopID}).
		OrderBy("display_name ASC", "code ASC").PlaceholderFormat(squirrel.Dollar)
	sql, args, err := b.ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Staff, 0)
	for rows.Next() {
		v, err := scanStaff(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) StaffByPublicID(ctx context.Context, q DBTX, shopID int64, publicID string) (Staff, error) {
	if q == nil {
		q = s.Pool
	}
	return scanStaff(q.QueryRow(ctx,
		`SELECT `+staffCols+` FROM staff WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
}

func (s *Store) CreateStaff(ctx context.Context, tx pgx.Tx, shopID int64, code, displayName string) (Staff, error) {
	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO staff (shop_id,code,display_name) VALUES ($1,$2,$3) RETURNING id`, shopID, code, displayName).Scan(&id); err != nil {
		return Staff{}, err
	}
	return scanStaff(tx.QueryRow(ctx, `SELECT `+staffCols+` FROM staff WHERE id=$1`, id))
}

func (s *Store) UpdateStaff(ctx context.Context, tx pgx.Tx, shopID, id int64, displayName, status string, version int) (Staff, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE staff SET display_name=$3, status=$4, version=version+1, updated_at=now()
		WHERE id=$1 AND shop_id=$2 AND version=$5`, id, shopID, displayName, status, version)
	if err != nil {
		return Staff{}, err
	}
	if tag.RowsAffected() == 0 {
		return Staff{}, pgx.ErrNoRows
	}
	return scanStaff(tx.QueryRow(ctx, `SELECT `+staffCols+` FROM staff WHERE id=$1`, id))
}

func (s *Store) DeleteStaff(ctx context.Context, tx pgx.Tx, shopID, id int64) error {
	_, err := tx.Exec(ctx, `DELETE FROM staff WHERE id=$1 AND shop_id=$2`, id, shopID)
	return err
}

// ---- queue_staff assignments ----

// Assignments returns flat join rows for the shop. Filtering by queue/staff is
// optional; both ends are already constrained to the shop by the composite FK.
func (s *Store) Assignments(ctx context.Context, q DBTX, shopID int64, queueID, staffID *int64) ([]qdomain.Assignment, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select("q.public_id::text, q.name, q.seq, st.public_id::text, st.display_name").
		From("queue_staff qs").
		Join("queues q ON q.id = qs.queue_id AND q.shop_id = qs.shop_id").
		Join("staff st ON st.id = qs.staff_id AND st.shop_id = qs.shop_id").
		Where(squirrel.Eq{"qs.shop_id": shopID}).PlaceholderFormat(squirrel.Dollar)
	if queueID != nil {
		b = b.Where(squirrel.Eq{"qs.queue_id": *queueID})
	}
	if staffID != nil {
		b = b.Where(squirrel.Eq{"qs.staff_id": *staffID})
	}
	sql, args, err := b.ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]qdomain.Assignment, 0)
	for rows.Next() {
		var a qdomain.Assignment
		if err := rows.Scan(&a.QueuePublicID, &a.QueueName, &a.QueueSeq, &a.StaffPublicID, &a.StaffDisplayName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Assign inserts a queue-staff link. The composite FK rejects a cross-shop
// pair; the composite PK makes a repeated assignment a no-op.
func (s *Store) Assign(ctx context.Context, tx pgx.Tx, shopID, queueID, staffID int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO queue_staff (shop_id,queue_id,staff_id) VALUES ($1,$2,$3)
		ON CONFLICT (queue_id,staff_id) DO NOTHING`, shopID, queueID, staffID)
	return err
}

func (s *Store) Unassign(ctx context.Context, tx pgx.Tx, shopID, queueID, staffID int64) error {
	_, err := tx.Exec(ctx, `DELETE FROM queue_staff WHERE shop_id=$1 AND queue_id=$2 AND staff_id=$3`, shopID, queueID, staffID)
	return err
}

// Active counts staff currently assigned to a queue; used in list responses.
func (s *Store) QueueStaffCount(ctx context.Context, q DBTX, shopID, queueID int64) (int, error) {
	if q == nil {
		q = s.Pool
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(*)::int FROM queue_staff WHERE shop_id=$1 AND queue_id=$2`, shopID, queueID).Scan(&n)
	return n, err
}
