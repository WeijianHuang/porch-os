// Command job is a one-shot Go process (ADR-0008): migrations, optional dev
// seeding, and on-demand audit cleanup. It is never started by api/worker.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/WeijianHuang/porch-os/internal/platform/config"
	"github.com/WeijianHuang/porch-os/internal/security"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fatal(errors.New("usage: job <migrate|seed-user|audit-cleanup>"))
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	switch os.Args[1] {
	case "migrate":
		err = migrate(ctx, cfg)
	case "seed-user":
		err = seedUser(ctx, cfg)
	case "audit-cleanup":
		err = auditCleanup(ctx, cfg)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	slog.Error("job failed", "error", err)
	os.Exit(1)
}

func migrate(ctx context.Context, cfg config.Config) error {
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	goose.SetDialect("postgres")
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return err
	}
	v, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return err
	}
	slog.Info("migration complete", "version", v)
	return nil
}

// seedUser is a development helper. Credentials are never stored in the repo.
func seedUser(ctx context.Context, cfg config.Config) error {
	login, password := os.Getenv("SEED_LOGIN"), os.Getenv("SEED_PASSWORD")
	if login == "" || len(password) < 8 {
		return errors.New("SEED_LOGIN and SEED_PASSWORD (>=8 bytes) are required")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `
		INSERT INTO users (login,password_hash) VALUES ($1,$2)
		ON CONFLICT (login) DO NOTHING`, login, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		_, err = conn.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT id, (SELECT id FROM roles WHERE code='admin') FROM users WHERE login=$1`, login)
		if err != nil {
			return err
		}
	}
	slog.Info("seed user ready", "login", login)
	return nil
}

// auditCleanup uses the UTC+8 natural-day cutoff and one session-scoped
// advisory lock on the same connection (ADR-0008, design/08 §2/§3).
func auditCleanup(ctx context.Context, cfg config.Config) error {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	now := time.Now().In(loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	cutoff := midnight.AddDate(0, 0, -cfg.AuditRetentionDays)
	runKey := "job:audit_cleanup:" + now.Format("2006-01-02")
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, runKey).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		slog.Info("audit cleanup skipped: another worker holds the lock", "run_key", runKey)
		return nil
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, runKey); err != nil {
			slog.Error("unlock failed", "error", err)
		}
	}()
	_, err = conn.Exec(ctx, `
		INSERT INTO audit_log_cleanup_runs (run_key,retention_days,cutoff_at)
		VALUES ($1,$2,$3)`, runKey, cfg.AuditRetentionDays, cutoff)
	if err != nil {
		return err
	}
	var total int64
	batches := 0
	for {
		tag, err := conn.Exec(ctx, `
			WITH victims AS (
			  SELECT id FROM audit_logs WHERE created_at < $1
			  ORDER BY created_at, id LIMIT 5000 FOR UPDATE SKIP LOCKED
			)
			DELETE FROM audit_logs a USING victims v WHERE a.id = v.id`, cutoff)
		if err != nil {
			_, _ = conn.Exec(ctx, `UPDATE audit_log_cleanup_runs SET status='failed', error_message=$2, finished_at=now() WHERE run_key=$1`, runKey, "delete failed")
			return err
		}
		n := tag.RowsAffected()
		total += n
		batches++
		if n < 5000 {
			break
		}
	}
	_, err = conn.Exec(ctx, `
		UPDATE audit_log_cleanup_runs SET status='succeeded', finished_at=now(),
		  deleted_rows=$2, batches=$3 WHERE run_key=$1`, runKey, total, batches)
	if err != nil {
		return err
	}
	slog.Info("audit cleanup complete", "run_key", runKey, "deleted_rows", total, "batches", batches,
		"cutoff", cutoff.UTC().Format(time.RFC3339))
	return nil
}
