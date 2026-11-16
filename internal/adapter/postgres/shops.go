package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

type Shop struct {
	PublicID  string    `json:"public_id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	ID        int64     `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Page struct {
	HasMore bool
	Next    string
}

const shopCols = "sh.public_id::text, sh.code, sh.name, sh.status, sh.version, sh.created_at, sh.updated_at, sh.id"

func scanShop(row pgx.Row) (Shop, error) {
	var s Shop
	err := row.Scan(&s.PublicID, &s.Code, &s.Name, &s.Status, &s.Version, &s.CreatedAt, &s.UpdatedAt, &s.ID)
	s.CreatedAt, s.UpdatedAt = utc(s.CreatedAt), utc(s.UpdatedAt)
	return s, err
}

// ListVisibleShops uses keyset pagination and a whitelisted sort. Dynamic SQL
// goes through squirrel with Dollar placeholders (ADR-0002 / DA-01).
func (s *Store) ListVisibleShops(ctx context.Context, q DBTX, userID int64, limit int, cursorTime *time.Time, cursorID int64) ([]Shop, Page, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(shopCols).
		From("user_shops us").
		Join("shops sh ON sh.id = us.shop_id").
		Where(squirrel.Eq{"us.user_id": userID, "us.active": true}).
		OrderBy("sh.created_at DESC", "sh.id DESC").
		Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)
	if cursorTime != nil && cursorID > 0 {
		b = b.Where(squirrel.Expr("(sh.created_at, sh.id) < (?, ?)", *cursorTime, cursorID))
	}
	sql, args, err := b.ToSql()
	if err != nil {
		return nil, Page{}, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	out := make([]Shop, 0, limit+1)
	for rows.Next() {
		shop, err := scanShop(rows)
		if err != nil {
			return nil, Page{}, err
		}
		out = append(out, shop)
	}
	if err := rows.Err(); err != nil {
		return nil, Page{}, err
	}
	page := Page{}
	if len(out) > limit {
		page.HasMore = true
		out = out[:limit]
	}
	return out, page, nil
}

func (s *Store) ShopByPublicID(ctx context.Context, q DBTX, publicID string) (Shop, int64, error) {
	if q == nil {
		q = s.Pool
	}
	var id int64
	if err := q.QueryRow(ctx, `SELECT id FROM shops WHERE public_id::text=$1`, publicID).Scan(&id); err != nil {
		return Shop{}, 0, err
	}
	shop, err := scanShop(q.QueryRow(ctx, `SELECT `+shopCols+` FROM shops sh WHERE sh.id=$1`, id))
	return shop, id, err
}

// CreateShop grants the creator visibility in the same transaction. There is
// no owner column: "owning" means authorized via user_shops (ADR-0006).
func (s *Store) CreateShop(ctx context.Context, tx pgx.Tx, code, name string, userID int64) (Shop, error) {
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO shops (code,name) VALUES ($1,$2) RETURNING id`, code, name).Scan(&id); err != nil {
		return Shop{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_shops (user_id,shop_id) VALUES ($1,$2)`, userID, id); err != nil {
		return Shop{}, err
	}
	return scanShop(tx.QueryRow(ctx, `SELECT `+shopCols+` FROM shops sh WHERE sh.id=$1`, id))
}

func (s *Store) UpdateShop(ctx context.Context, tx pgx.Tx, id int64, name, status string, version int) (Shop, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE shops SET name=$2, status=$3, version=version+1, updated_at=now()
		WHERE id=$1 AND version=$4`, id, name, status, version)
	if err != nil {
		return Shop{}, err
	}
	if tag.RowsAffected() == 0 {
		return Shop{}, pgx.ErrNoRows
	}
	return scanShop(tx.QueryRow(ctx, `SELECT `+shopCols+` FROM shops sh WHERE sh.id=$1`, id))
}

func (s *Store) GetSetting(ctx context.Context, q DBTX, shopID int64) (map[string]any, error) {
	if q == nil {
		q = s.Pool
	}
	var value map[string]any
	err := q.QueryRow(ctx, `SELECT value FROM shop_settings WHERE shop_id=$1 AND key='general'`, shopID).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{}, nil
	}
	return value, err
}

func (s *Store) UpsertSetting(ctx context.Context, tx pgx.Tx, shopID int64, value map[string]any) (map[string]any, error) {
	_, err := tx.Exec(ctx, `
		INSERT INTO shop_settings (shop_id,key,value) VALUES ($1,'general',$2::jsonb)
		ON CONFLICT (shop_id,key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, shopID, value)
	return value, err
}

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func (s Shop) getCursor() (time.Time, int64) { return s.CreatedAt, s.ID }
