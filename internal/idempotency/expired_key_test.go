package idempotency

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestExpiredCompletedKeyCanStartFreshRequest(t *testing.T) {
	store := idempotencyStore(t)
	oldHash := RequestHash([]byte("old enrollment"))
	err := store.InTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := Begin(context.Background(), tx, 1, "POST", "enrollment", "summer-key", oldHash, time.Now().Add(-2*time.Hour)); err != nil {
			return err
		}
		return Complete(context.Background(), tx, 1, "POST", "enrollment", "summer-key", 201, []byte(`{"enrollment_id":11}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	var replay *Record
	err = store.InTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		replay, err = Begin(context.Background(), tx, 1, "POST", "enrollment", "summer-key", RequestHash([]byte("new enrollment")), time.Now().Add(time.Hour))
		return err
	})
	if err != nil {
		t.Fatalf("expired key rejected fresh request: %v", err)
	}
	if replay != nil {
		t.Fatalf("expired response was replayed: %+v", replay)
	}
}
