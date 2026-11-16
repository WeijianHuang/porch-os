// Package postgres implements SQL-first repositories. No ORM, no entity graph;
// every method returns a flat, scenario-specific DTO (ADR-0002 / DA-01).
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/WeijianHuang/porch-os/internal/platform/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx, letting a service choose
// between an autocommit read and an explicit Unit of Work.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ Pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

// NewPool configures explicit limits rather than driver defaults. Shared state
// and coordination live in PostgreSQL only (ADR-0008); no Redis lock in R1.
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	pc.MaxConns = cfg.MaxConns
	pc.MinConns = 1
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		for _, stmt := range []string{
			"SET statement_timeout = '5s'",
			"SET idle_in_transaction_session_timeout = '10s'",
			"SET lock_timeout = '3s'",
		} {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	return pool, nil
}

// utc normalizes timestamptz values to the transport contract (ISO 8601 Z).
func utc(t time.Time) time.Time { return t.UTC() }
