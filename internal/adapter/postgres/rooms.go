package postgres

import (
	"context"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// Room is a flat shop-scoped DTO: no embedded entity graph (ADR-0002 / ADR-0011).
// Floor is a scalar column; there is no floors table or floor_id.
type Room struct {
	PublicID  string `json:"public_id"`
	Floor     int    `json:"floor"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Version   int    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ID        int64     `json:"-"`
}

const roomCols = "public_id::text, floor, code, name, status, version, created_at, updated_at, id"

func scanRoom(row pgx.Row) (Room, error) {
	var v Room
	err := row.Scan(&v.PublicID, &v.Floor, &v.Code, &v.Name, &v.Status, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID)
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, err
}

func (s *Store) ListRooms(ctx context.Context, q DBTX, shopID int64) ([]Room, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(roomCols).From("rooms").
		Where(squirrel.Eq{"shop_id": shopID}).
		OrderBy("floor DESC", "code ASC").PlaceholderFormat(squirrel.Dollar)
	sql, args, err := b.ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Room, 0)
	for rows.Next() {
		v, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RoomByPublicID(ctx context.Context, q DBTX, shopID int64, publicID string) (Room, error) {
	if q == nil {
		q = s.Pool
	}
	return scanRoom(q.QueryRow(ctx,
		`SELECT `+roomCols+` FROM rooms WHERE shop_id=$1 AND public_id::text=$2`, shopID, publicID))
}

func (s *Store) CreateRoom(ctx context.Context, tx pgx.Tx, shopID int64, floor int, code, name string) (Room, error) {
	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO rooms (shop_id,floor,code,name) VALUES ($1,$2,$3,$4) RETURNING id`,
		shopID, floor, code, name).Scan(&id); err != nil {
		return Room{}, err
	}
	return scanRoom(tx.QueryRow(ctx, `SELECT `+roomCols+` FROM rooms WHERE id=$1`, id))
}

func (s *Store) UpdateRoom(
	ctx context.Context,
	tx pgx.Tx,
	shopID, id int64,
	floor int, code, name, status string,
	version int,
) (Room, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE rooms SET floor=$3, code=$4, name=$5, status=$6, version=version+1, updated_at=now()
		WHERE id=$1 AND shop_id=$2 AND version=$7`, id, shopID, floor, code, name, status, version)
	if err != nil {
		return Room{}, err
	}
	if tag.RowsAffected() == 0 {
		return Room{}, pgx.ErrNoRows
	}
	return scanRoom(tx.QueryRow(ctx, `SELECT `+roomCols+` FROM rooms WHERE id=$1`, id))
}

func (s *Store) DeleteRoom(ctx context.Context, tx pgx.Tx, shopID, id int64) error {
	_, err := tx.Exec(ctx, `DELETE FROM rooms WHERE id=$1 AND shop_id=$2`, id, shopID)
	return err
}
