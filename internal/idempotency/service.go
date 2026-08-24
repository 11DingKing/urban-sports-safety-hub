package idempotency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type Record struct {
	ActorID     int64
	Method      string
	Operation   string
	RequestKey  string
	RequestHash string
	Status      string
	Code        sql.NullInt64
	Body        []byte
	ExpiresAt   time.Time
}

func RequestHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func Begin(ctx context.Context, tx *sql.Tx, actorID int64, method, operation, key, hash string, expiresAt time.Time) (*Record, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(actor_id,method,operation,request_key,request_hash,status,expires_at,created_at) VALUES(?,?,?,?,?,'running',?,?)`, actorID, method, operation, key, hash, expiresAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		return nil, nil
	}
	var record Record
	var expires string
	err = tx.QueryRowContext(ctx, `SELECT actor_id,method,operation,request_key,request_hash,status,response_code,response_body,expires_at FROM idempotency_keys WHERE actor_id=? AND method=? AND operation=? AND request_key=?`, actorID, method, operation, key).Scan(&record.ActorID, &record.Method, &record.Operation, &record.RequestKey, &record.RequestHash, &record.Status, &record.Code, &record.Body, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(domain.KindConflict, "idempotency_conflict", "idempotency key could not be acquired")
	}
	if err != nil {
		return nil, err
	}
	record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return nil, err
	}
	if record.RequestHash != hash {
		return nil, domain.NewError(domain.KindConflict, "idempotency_payload_mismatch", "idempotency key was used with a different request")
	}
	if record.Status == "running" {
		return nil, domain.NewError(domain.KindConflict, "idempotency_in_progress", "an equivalent request is still in progress")
	}
	copyRecord := record
	copyRecord.Body = append([]byte(nil), record.Body...)
	return &copyRecord, nil
}

func Complete(ctx context.Context, tx *sql.Tx, actorID int64, method, operation, key string, status int, body []byte) error {
	result, err := tx.ExecContext(ctx, `UPDATE idempotency_keys SET status='completed',response_code=?,response_body=? WHERE actor_id=? AND method=? AND operation=? AND request_key=? AND status='running'`, status, append([]byte(nil), body...), actorID, method, operation, key)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}
