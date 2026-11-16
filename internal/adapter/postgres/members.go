package postgres

import (
	"context"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

type Member struct {
	PublicID    string    `json:"public_id"`
	Phone       string    `json:"phone"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	ID          int64     `json:"-"`
}

const memberCols = "public_id::text, phone, display_name, status, version, created_at, id"

func scanMember(row pgx.Row) (Member, error) {
	var m Member
	err := row.Scan(&m.PublicID, &m.Phone, &m.DisplayName, &m.Status, &m.Version, &m.CreatedAt, &m.ID)
	m.CreatedAt = utc(m.CreatedAt)
	return m, err
}

func (s *Store) ListMembers(ctx context.Context, q DBTX, limit int, cursorTime *time.Time, cursorID int64, phone string) ([]Member, Page, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(memberCols).From("members").
		OrderBy("created_at DESC", "id DESC").Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)
	if phone != "" {
		b = b.Where(squirrel.Like{"phone": "%" + phone + "%"})
	}
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
	out := make([]Member, 0, limit+1)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, Page{}, err
		}
		out = append(out, m)
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

func (s *Store) Member(ctx context.Context, q DBTX, publicID string) (Member, int64, error) {
	if q == nil {
		q = s.Pool
	}
	var id int64
	if err := q.QueryRow(ctx, `SELECT id FROM members WHERE public_id::text=$1`, publicID).Scan(&id); err != nil {
		return Member{}, 0, err
	}
	m, err := scanMember(q.QueryRow(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1`, id))
	return m, id, err
}

func (s *Store) CreateMember(ctx context.Context, tx pgx.Tx, phone, displayName string) (Member, error) {
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO members (phone,display_name) VALUES ($1,$2) RETURNING id`, phone, displayName).Scan(&id); err != nil {
		return Member{}, err
	}
	return scanMember(tx.QueryRow(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1`, id))
}

func (s *Store) UpdateMember(ctx context.Context, tx pgx.Tx, id int64, displayName, status string, version int) (Member, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE members SET display_name=$2, status=$3, version=version+1, updated_at=now()
		WHERE id=$1 AND version=$4`, id, displayName, status, version)
	if err != nil {
		return Member{}, err
	}
	if tag.RowsAffected() == 0 {
		return Member{}, pgx.ErrNoRows
	}
	return scanMember(tx.QueryRow(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1`, id))
}

type LedgerEntry struct {
	PublicID     string    `json:"public_id"`
	Delta        string    `json:"delta"`
	BalanceAfter string    `json:"balance_after"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
	ID           int64     `json:"-"`
}

func (s *Store) Balance(ctx context.Context, q DBTX, shopID, memberID int64) (string, int, error) {
	if q == nil {
		q = s.Pool
	}
	var balance string
	var version int
	err := q.QueryRow(ctx, `SELECT balance::text, version FROM member_balances WHERE shop_id=$1 AND member_id=$2`, shopID, memberID).Scan(&balance, &version)
	if IsNoRows(err) {
		return "0.00", 0, nil
	}
	return balance, version, err
}

func (s *Store) Ledger(ctx context.Context, q DBTX, shopID, memberID int64, limit int) ([]LedgerEntry, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT public_id::text, delta::text, balance_after::text, reason, created_at
		FROM balance_ledger WHERE shop_id=$1 AND member_id=$2
		ORDER BY created_at DESC, id DESC LIMIT $3`, shopID, memberID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LedgerEntry, 0)
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.PublicID, &e.Delta, &e.BalanceAfter, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.CreatedAt = utc(e.CreatedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Topup writes the balance and its append-only ledger row in one transaction.
func (s *Store) Topup(ctx context.Context, tx pgx.Tx, shopID, memberID int64, amount string) (string, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO member_balances (shop_id,member_id,balance) VALUES ($1,$2,0)
		ON CONFLICT (shop_id,member_id) DO NOTHING`, shopID, memberID); err != nil {
		return "", err
	}
	var after string
	if err := tx.QueryRow(ctx, `
		UPDATE member_balances SET balance=balance+$2::numeric,
		  version=version+1, updated_at=now()
		WHERE shop_id=$1 AND member_id=$3 RETURNING balance::text`, shopID, amount, memberID).Scan(&after); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO balance_ledger (shop_id,member_id,delta,balance_after,reason)
		VALUES ($1,$2,$3::numeric,$4::numeric,'topup')`, shopID, memberID, amount, after)
	return after, err
}

func (m Member) getCursor() (time.Time, int64) { return m.CreatedAt, m.ID }
