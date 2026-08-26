package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

func TestCanceledLoginDoesNotCreateUnclaimedSession(t *testing.T) {
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "canceled-login.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := New(store, time.Hour)
	if _, err := service.Register(context.Background(), "guardian-canceled@test", "guardian-password", "Guardian", domain.RoleGuardian); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Login(canceled, "guardian-canceled@test", "guardian-password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled login returned %v instead of context cancellation", err)
	}
	var canceledSessions int
	if err := store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sessions`).Scan(&canceledSessions); err != nil {
		t.Fatal(err)
	}
	if canceledSessions != 0 {
		t.Fatalf("canceled login left %d active session rows", canceledSessions)
	}

	login, err := service.Login(context.Background(), "guardian-canceled@test", "guardian-password")
	if err != nil {
		t.Fatalf("normal login failed after canceled attempt: %v", err)
	}
	principal, err := service.Authenticate(context.Background(), login.Token)
	if err != nil || principal.Role != domain.RoleGuardian {
		t.Fatalf("normal login session is unusable: principal=%+v err=%v", principal, err)
	}
}
