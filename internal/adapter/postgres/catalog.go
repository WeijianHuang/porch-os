package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

type CatalogItem struct {
	PublicID        string    `json:"public_id"`
	SKU             string    `json:"sku"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	BasePrice       string    `json:"base_price"`
	DurationMinutes *int      `json:"duration_minutes"` // ADR-0018: service nominal minutes; product -> null
	Active          bool      `json:"active"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ID              int64     `json:"-"`
}

type Category struct {
	PublicID  string  `json:"public_id"`
	ParentID  *string `json:"parent_id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sort_order"`
}

const itemCols = "public_id::text, sku, name, type, base_price::text, duration_minutes, active, version, created_at, updated_at, id"

func scanItem(row pgx.Row) (CatalogItem, error) {
	var v CatalogItem
	var duration sql.NullInt32
	err := row.Scan(&v.PublicID, &v.SKU, &v.Name, &v.Type, &v.BasePrice, &duration, &v.Active, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID)
	if duration.Valid {
		d := int(duration.Int32)
		v.DurationMinutes = &d
	}
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, err
}

func (s *Store) ListCatalogItems(ctx context.Context, q DBTX, limit int, cursorTime *time.Time, cursorID int64) ([]CatalogItem, Page, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(itemCols).From("catalog_items").
		OrderBy("created_at DESC", "id DESC").Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)
	if cursorTime != nil && cursorID > 0 {
		b = b.Where(squirrel.Expr("(created_at, id) < (?, ?)", *cursorTime, cursorID))
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
	out := make([]CatalogItem, 0, limit+1)
	for rows.Next() {
		v, err := scanItem(rows)
		if err != nil {
			return nil, Page{}, err
		}
		out = append(out, v)
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

func (s *Store) CatalogItem(ctx context.Context, q DBTX, publicID string) (CatalogItem, int64, error) {
	if q == nil {
		q = s.Pool
	}
	var id int64
	if err := q.QueryRow(ctx, `SELECT id FROM catalog_items WHERE public_id::text=$1`, publicID).Scan(&id); err != nil {
		return CatalogItem{}, 0, err
	}
	v, err := scanItem(q.QueryRow(ctx, `SELECT `+itemCols+` FROM catalog_items WHERE id=$1`, id))
	return v, id, err
}

func (s *Store) CreateCatalogItem(ctx context.Context, tx pgx.Tx, sku, name, itemType, basePrice string, durationMinutes *int) (CatalogItem, error) {
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO catalog_items (sku,name,type,base_price,duration_minutes)
		VALUES ($1,$2,$3,$4::numeric,$5)
		RETURNING id`, sku, name, itemType, basePrice, durationMinutes).Scan(&id); err != nil {
		return CatalogItem{}, err
	}
	return scanItem(tx.QueryRow(ctx, `SELECT `+itemCols+` FROM catalog_items WHERE id=$1`, id))
}

func (s *Store) UpdateCatalogItem(ctx context.Context, tx pgx.Tx, id int64, name, basePrice string, durationMinutes *int, active bool, version int) (CatalogItem, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE catalog_items SET name=$2, base_price=$3::numeric,
		  duration_minutes=CASE WHEN $4::integer IS NULL THEN duration_minutes ELSE $4::integer END,
		  active=$5, version=version+1, updated_at=now()
		WHERE id=$1 AND version=$6`, id, name, basePrice, durationMinutes, active, version)
	if err != nil {
		return CatalogItem{}, err
	}
	if tag.RowsAffected() == 0 {
		return CatalogItem{}, pgx.ErrNoRows
	}
	return scanItem(tx.QueryRow(ctx, `SELECT `+itemCols+` FROM catalog_items WHERE id=$1`, id))
}

func (s *Store) ListCategories(ctx context.Context, q DBTX) ([]Category, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text,
		       (SELECT p.public_id::text FROM catalog_categories p WHERE p.id = c.parent_id),
		       c.code, c.name, c.sort_order
		FROM catalog_categories c ORDER BY c.sort_order, c.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Category, 0)
	for rows.Next() {
		var c Category
		var parent *string
		if err := rows.Scan(&c.PublicID, &parent, &c.Code, &c.Name, &c.SortOrder); err != nil {
			return nil, err
		}
		c.ParentID = parent
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) PutOverride(ctx context.Context, tx pgx.Tx, shopID, itemID int64, price string, available bool) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO shop_catalog_overrides (shop_id,catalog_item_id,price,available)
		VALUES ($1,$2,$3::numeric,$4)
		ON CONFLICT (shop_id,catalog_item_id) DO UPDATE
		SET price=EXCLUDED.price, available=EXCLUDED.available, updated_at=now()`,
		shopID, itemID, price, available)
	return err
}

func (v CatalogItem) GetCursor() (time.Time, int64) { return v.CreatedAt, v.ID }
