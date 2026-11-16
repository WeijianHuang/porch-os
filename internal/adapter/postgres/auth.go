package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/WeijianHuang/porch-os/internal/domain/shop"
	"github.com/jackc/pgx/v5"
)

type AuthUser struct {
	ID           int64
	Login        string
	PasswordHash string
	Status       string
}

func (s *Store) UserByLogin(ctx context.Context, q DBTX, login string) (AuthUser, error) {
	if q == nil {
		q = s.Pool
	}
	var u AuthUser
	err := q.QueryRow(ctx,
		`SELECT id, login, password_hash, status FROM users WHERE login = $1`, login,
	).Scan(&u.ID, &u.Login, &u.PasswordHash, &u.Status)
	return u, err
}

func (s *Store) UserByID(ctx context.Context, q DBTX, id int64) (string, error) {
	if q == nil {
		q = s.Pool
	}
	var login string
	err := q.QueryRow(ctx, `SELECT login FROM users WHERE id=$1`, id).Scan(&login)
	return login, err
}

func (s *Store) UserExists(ctx context.Context, q DBTX, id int64) (bool, error) {
	if q == nil {
		q = s.Pool
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active')`, id).Scan(&ok)
	return ok, err
}

// VisibleShops reads user_shops — the only authorization source for shops.
func (s *Store) VisibleShops(ctx context.Context, q DBTX, userID int64) ([]shop.Visible, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT sh.public_id::text, sh.name, sh.status, us.granted_at
		FROM user_shops us
		JOIN shops sh ON sh.id = us.shop_id
		WHERE us.user_id = $1 AND us.active
		ORDER BY us.granted_at, sh.public_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]shop.Visible, 0)
	for rows.Next() {
		var v shop.Visible
		var granted time.Time
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Status, &granted); err != nil {
			return nil, err
		}
		v.GrantedAtUnix = granted.Unix()
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RolesAndPermissions(ctx context.Context, q DBTX, userID int64) ([]string, []string, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, `
		SELECT 'role:' || r.code AS code
		FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1
		UNION
		SELECT p.code
		FROM user_roles ur
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE ur.user_id = $1
		ORDER BY code`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	roles, perms := make([]string, 0), make([]string, 0)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, nil, err
		}
		if len(code) > 5 && code[:5] == "role:" {
			roles = append(roles, code[5:])
		} else {
			perms = append(perms, code)
		}
	}
	return roles, perms, rows.Err()
}

func (s *Store) SaveRefreshToken(ctx context.Context, q DBTX, userID int64, hash string, expires time.Time) error {
	if q == nil {
		q = s.Pool
	}
	_, err := q.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1,$2,$3)`,
		userID, hash, expires)
	return err
}

// ConsumeRefreshToken atomically revokes an unexpired token and returns its owner.
func (s *Store) ConsumeRefreshToken(ctx context.Context, q DBTX, hash string) (int64, error) {
	if q == nil {
		q = s.Pool
	}
	var userID int64
	err := q.QueryRow(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING user_id`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pgx.ErrNoRows
	}
	return userID, err
}

func (s *Store) RevokeRefreshToken(ctx context.Context, q DBTX, hash string) error {
	if q == nil {
		q = s.Pool
	}
	_, err := q.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash=$1 AND revoked_at IS NULL`, hash)
	return err
}

func (s *Store) WriteAudit(ctx context.Context, q DBTX, shopID *int64, actorID int64, action, targetType, targetID string) error {
	if q == nil {
		q = s.Pool
	}
	_, err := q.Exec(ctx, `
		INSERT INTO audit_logs (shop_id, actor_id, action, target_type, target_id)
		VALUES ($1,$2,$3,$4,$5)`, shopID, actorID, action, targetType, targetID)
	return err
}
