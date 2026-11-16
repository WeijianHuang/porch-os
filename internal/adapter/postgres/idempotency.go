package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// RequestFingerprint binds an idempotency key to one method/path/body tuple.
func RequestFingerprint(method, path string, body []byte) string {
	sum := sha256.Sum256(append([]byte(method+"\n"+path+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

type CachedResponse struct {
	Status int
	Body   []byte
}

// IdempotentWrite runs fn inside one transaction together with the idempotency
// record: business effects and the key commit atomically (design/03 §6).
func (s *Store) IdempotentWrite(
	ctx context.Context,
	scope, key, fingerprint string,
	fn func(tx pgx.Tx) (int, any, error),
) (int, any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	fp, _ := hex.DecodeString(fingerprint)
	var state string
	var storedHash []byte
	var status int
	var body []byte
	err = tx.QueryRow(ctx, `
		SELECT state, request_hash, COALESCE(response_status,0), COALESCE(response_body::text,'')
		FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, key).
		Scan(&state, &storedHash, &status, &body)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `
			INSERT INTO idempotency_keys (scope,key,request_hash,state)
			VALUES ($1,$2,$3,'in_progress')`, scope, key, fp)
		if err != nil {
			return 0, nil, err
		}
	case err != nil:
		return 0, nil, err
	default:
		if hex.EncodeToString(storedHash) != fingerprint {
			return 0, nil, apierrors.IdemConflict
		}
		switch state {
		case "succeeded":
			return status, json.RawMessage(body), nil
		case "in_progress":
			return 0, nil, apierrors.IdemInProgress
		default: // expired failed row: allow one more attempt
			if _, err := tx.Exec(ctx, `
				UPDATE idempotency_keys SET state='in_progress', request_hash=$3,
				  response_status=NULL, response_body=NULL, completed_at=NULL
				WHERE scope=$1 AND key=$2`, scope, key, fp); err != nil {
				return 0, nil, err
			}
		}
	}

	st, payload, err := fn(tx)
	if err != nil {
		return 0, nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE idempotency_keys SET state='succeeded', response_status=$3,
		  response_body=$4::jsonb, completed_at=now(), expires_at=now()+interval '24 hours'
		WHERE scope=$1 AND key=$2`, scope, key, st, string(encoded)); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return st, payload, nil
}

// ExpireIdempotencyKeys is called by the worker; 24h retention from design/03.
func (s *Store) ExpireIdempotencyKeys(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}
