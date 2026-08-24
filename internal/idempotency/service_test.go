package idempotency

import (
	"context"
	"database/sql"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
	"path/filepath"
	"testing"
	"time"
)

func idempotencyStore(t *testing.T) *dbstore.Store {
	t.Helper()
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "idempotency.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.DB().Exec(`INSERT INTO accounts(id,email,password_hash,display_name,role,active,created_at) VALUES(1,'actor@test','h','Actor','administrator',1,?)`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func inTransaction(t *testing.T, store *dbstore.Store, fn func(*sql.Tx) error) error {
	t.Helper()
	return store.InTx(context.Background(), fn)
}

func TestRequestHashIsStableAndPayloadSensitive(t *testing.T) {
	first := RequestHash([]byte(`{"student":1,"session":2}`))
	second := RequestHash([]byte(`{"student":1,"session":2}`))
	different := RequestHash([]byte(`{"student":2,"session":2}`))
	if len(first) != 64 || first != second || first == different {
		t.Fatalf("first=%s second=%s different=%s", first, second, different)
	}
}

func TestBeginAcquiresNewScopedKey(t *testing.T) {
	store := idempotencyStore(t)
	err := inTransaction(t, store, func(tx *sql.Tx) error {
		record, err := Begin(context.Background(), tx, 1, "POST", "enrollment", "key-1", RequestHash([]byte("payload")), time.Now().Add(time.Hour))
		if err != nil {
			return err
		}
		if record != nil {
			t.Fatalf("new key returned replay record: %+v", record)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.DB().QueryRow(`SELECT status FROM idempotency_keys WHERE actor_id=1 AND request_key='key-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("status=%s", status)
	}
}

func TestBeginRejectsDuplicateWhileOriginalIsRunning(t *testing.T) {
	store := idempotencyStore(t)
	hash := RequestHash([]byte("payload"))
	if err := inTransaction(t, store, func(tx *sql.Tx) error {
		_, err := Begin(context.Background(), tx, 1, "POST", "checkout", "key", hash, time.Now().Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := inTransaction(t, store, func(tx *sql.Tx) error {
		_, err := Begin(context.Background(), tx, 1, "POST", "checkout", "key", hash, time.Now().Add(time.Hour))
		return err
	})
	if code(err) != "idempotency_in_progress" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBeginRejectsPayloadMismatchForExistingKey(t *testing.T) {
	store := idempotencyStore(t)
	if err := inTransaction(t, store, func(tx *sql.Tx) error {
		_, err := Begin(context.Background(), tx, 1, "POST", "checkout", "key", RequestHash([]byte("first")), time.Now().Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := inTransaction(t, store, func(tx *sql.Tx) error {
		_, err := Begin(context.Background(), tx, 1, "POST", "checkout", "key", RequestHash([]byte("second")), time.Now().Add(time.Hour))
		return err
	})
	if code(err) != "idempotency_payload_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompleteStoresReplayResponseAndReturnsIsolatedBytes(t *testing.T) {
	store := idempotencyStore(t)
	hash := RequestHash([]byte("payload"))
	_ = inTransaction(t, store, func(tx *sql.Tx) error {
		_, err := Begin(context.Background(), tx, 1, "POST", "checkout", "key", hash, time.Now().Add(time.Hour))
		return err
	})
	body := []byte(`{"loan_id":42}`)
	if err := inTransaction(t, store, func(tx *sql.Tx) error {
		return Complete(context.Background(), tx, 1, "POST", "checkout", "key", 201, body)
	}); err != nil {
		t.Fatal(err)
	}
	body[0] = 'x'
	var replay *Record
	err := inTransaction(t, store, func(tx *sql.Tx) error {
		var err error
		replay, err = Begin(context.Background(), tx, 1, "POST", "checkout", "key", hash, time.Now().Add(time.Hour))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay == nil || !replay.Code.Valid || replay.Code.Int64 != 201 || string(replay.Body) != `{"loan_id":42}` || replay.Status != "completed" {
		t.Fatalf("unexpected replay: %+v", replay)
	}
	replay.Body[0] = 'y'
	var stored string
	_ = store.DB().QueryRow(`SELECT response_body FROM idempotency_keys WHERE request_key='key'`).Scan(&stored)
	if stored != `{"loan_id":42}` {
		t.Fatalf("stored body mutated: %q", stored)
	}
}

func TestCompleteRejectsUnknownOrAlreadyCompletedKey(t *testing.T) {
	store := idempotencyStore(t)
	err := inTransaction(t, store, func(tx *sql.Tx) error {
		return Complete(context.Background(), tx, 1, "POST", "checkout", "missing", 200, []byte(`{}`))
	})
	if err == nil {
		t.Fatal("missing key completed")
	}
	hash := RequestHash([]byte("payload"))
	_ = inTransaction(t, store, func(tx *sql.Tx) error {
		_, err := Begin(context.Background(), tx, 1, "POST", "checkout", "key", hash, time.Now().Add(time.Hour))
		return err
	})
	_ = inTransaction(t, store, func(tx *sql.Tx) error {
		return Complete(context.Background(), tx, 1, "POST", "checkout", "key", 200, []byte(`{}`))
	})
	err = inTransaction(t, store, func(tx *sql.Tx) error {
		return Complete(context.Background(), tx, 1, "POST", "checkout", "key", 201, []byte(`{"changed":true}`))
	})
	if err == nil {
		t.Fatal("completed key was overwritten")
	}
}

func TestKeysAreScopedByActorMethodAndOperation(t *testing.T) {
	store := idempotencyStore(t)
	_, _ = store.DB().Exec(`INSERT INTO accounts(id,email,password_hash,display_name,role,active,created_at) VALUES(2,'other@test','h','Other','administrator',1,?)`, time.Now().UTC().Format(time.RFC3339Nano))
	cases := []struct {
		actor             int64
		method, operation string
	}{{1, "POST", "checkout"}, {1, "PUT", "checkout"}, {1, "POST", "return"}, {2, "POST", "checkout"}}
	for _, tc := range cases {
		err := inTransaction(t, store, func(tx *sql.Tx) error {
			_, err := Begin(context.Background(), tx, tc.actor, tc.method, tc.operation, "shared", RequestHash([]byte("payload")), time.Now().Add(time.Hour))
			return err
		})
		if err != nil {
			t.Fatalf("scope %+v: %v", tc, err)
		}
	}
	var count int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM idempotency_keys WHERE request_key='shared'`).Scan(&count)
	if count != 4 {
		t.Fatalf("keys=%d", count)
	}
}

func code(err error) string { _, value, _ := domain.ErrorDetails(err); return value }
