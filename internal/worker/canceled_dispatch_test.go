package worker

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

func TestCanceledWorkerLeavesDueJobPendingForRecovery(t *testing.T) {
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	if _, err := store.EnqueueJob(context.Background(), "equipment_check", "helmet-42", `{}`, 3, now); err != nil {
		t.Fatal(err)
	}
	runner := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, 1)
	handled := 0
	runner.Register("equipment_check", func(context.Context, domain.Job) error {
		handled++
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.RunOnce(ctx)
	if handled != 0 {
		t.Fatalf("canceled worker dispatched %d jobs", handled)
	}
	jobs, err := store.ClaimJobs(context.Background(), 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Key != "helmet-42" || jobs[0].Attempts != 1 {
		t.Fatalf("pending job was not recoverable: %+v", jobs)
	}
}
