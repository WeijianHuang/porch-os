package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Masterminds/squirrel"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

type CatalogItem struct {
	PublicID         string         `json:"public_id"`
	SKU              string         `json:"sku"`
	Name             string         `json:"name"`
	Type             string         `json:"type"`
	BasePrice        string         `json:"base_price"`
	DurationMinutes  *int           `json:"duration_minutes"` // ADR-0018: service nominal minutes; product -> null
	Active           bool           `json:"active"`
	Version          int            `json:"version"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	CategoryPublicID *string        `json:"category_public_id"`
	CategoryName     *string        `json:"category_name"`
	Attributes       map[string]any `json:"attributes"`
	ID               int64          `json:"-"`
}

type Category struct {
	PublicID  string  `json:"public_id"`
	ParentID  *string `json:"parent_id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	SortOrder int     `json:"sort_order"`
}

const itemCols = "ci.public_id::text, ci.sku, ci.name, ci.type, ci.base_price::text, ci.duration_minutes, ci.active, ci.version, ci.created_at, ci.updated_at, ci.id, cc.public_id::text, cc.name, ci.attributes::jsonb"
const itemFrom = " FROM catalog_items ci LEFT JOIN catalog_categories cc ON cc.id = ci.category_id"

func scanItem(row pgx.Row) (CatalogItem, error) {
	var v CatalogItem
	var duration sql.NullInt32
	var attributes []byte
	err := row.Scan(&v.PublicID, &v.SKU, &v.Name, &v.Type, &v.BasePrice, &duration, &v.Active, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID,
		&v.CategoryPublicID, &v.CategoryName, &attributes)
	if duration.Valid {
		d := int(duration.Int32)
		v.DurationMinutes = &d
	}
	v.Attributes = map[string]any{}
	if len(attributes) > 0 {
		_ = json.Unmarshal(attributes, &v.Attributes)
	}
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, err
}

// ResolveCatalogCategoryID validates a global catalog category public id.
func (s *Store) ResolveCatalogCategoryID(ctx context.Context, q DBTX, publicID string) (int64, error) {
	if q == nil {
		q = s.Pool
	}
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM catalog_categories WHERE public_id::text=$1`, publicID).Scan(&id)
	if IsNoRows(err) {
		return 0, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "category_public_id", Reason: "category_not_found"})
	}
	return id, err
}

func (s *Store) ListCatalogItems(ctx context.Context, q DBTX, limit int, cursorTime *time.Time, cursorID int64) ([]CatalogItem, Page, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(itemCols).From("catalog_items ci").
		LeftJoin("catalog_categories cc ON cc.id = ci.category_id").
		OrderBy("ci.created_at DESC", "ci.id DESC").Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)
	if cursorTime != nil && cursorID > 0 {
		b = b.Where(squirrel.Expr("(ci.created_at, ci.id) < (?, ?)", *cursorTime, cursorID))
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
	v, err := scanItem(q.QueryRow(ctx, `SELECT `+itemCols+itemFrom+` WHERE ci.id=$1`, id))
	return v, id, err
}

func (s *Store) CreateCatalogItem(
	ctx context.Context, tx pgx.Tx, sku, name, itemType, basePrice string,
	durationMinutes *int, categoryID *int64,
) (CatalogItem, error) {
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO catalog_items (sku,name,type,base_price,duration_minutes,category_id)
		VALUES ($1,$2,$3,$4::numeric,$5,$6)
		RETURNING id`, sku, name, itemType, basePrice, durationMinutes, categoryID).Scan(&id); err != nil {
		return CatalogItem{}, err
	}
	return scanItem(tx.QueryRow(ctx, `SELECT `+itemCols+itemFrom+` WHERE ci.id=$1`, id))
}

func (s *Store) UpdateCatalogItem(
	ctx context.Context, tx pgx.Tx, id int64, name, basePrice string,
	durationMinutes *int, categoryID *int64, active bool, version int,
) (CatalogItem, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE catalog_items SET name=$2, base_price=$3::numeric,
		  duration_minutes=CASE WHEN $4::integer IS NULL THEN duration_minutes ELSE $4::integer END,
		  category_id=CASE WHEN $5::bigint IS NULL THEN category_id ELSE $5::bigint END,
		  active=$6, version=version+1, updated_at=now()
		WHERE id=$1 AND version=$7`, id, name, basePrice, durationMinutes, categoryID, active, version)
	if err != nil {
		return CatalogItem{}, err
	}
	if tag.RowsAffected() == 0 {
		return CatalogItem{}, pgx.ErrNoRows
	}
	return scanItem(tx.QueryRow(ctx, `SELECT `+itemCols+itemFrom+` WHERE ci.id=$1`, id))
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
