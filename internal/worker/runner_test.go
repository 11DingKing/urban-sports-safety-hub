package worker

import (
	"context"
	"errors"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeRepository struct {
	mu                                         sync.Mutex
	jobs                                       []domain.Job
	claimErr, recoverErr, finishErr, deleteErr error
	finished                                   map[int64]error
	recovered                                  int
	deletedBefore                              time.Time
	claimCalls                                 int
}

func newFakeRepository() *fakeRepository { return &fakeRepository{finished: map[int64]error{}} }
func (f *fakeRepository) ClaimJobs(ctx context.Context, limit int, now time.Time) ([]domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if len(f.jobs) == 0 {
		return nil, nil
	}
	count := limit
	if count > len(f.jobs) {
		count = len(f.jobs)
	}
	jobs := append([]domain.Job(nil), f.jobs[:count]...)
	f.jobs = f.jobs[count:]
	for i := range jobs {
		jobs[i].Attempts++
		jobs[i].Status = "running"
	}
	return jobs, nil
}
func (f *fakeRepository) FinishJob(_ context.Context, id int64, err error, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished[id] = err
	return f.finishErr
}
func (f *fakeRepository) RecoverJobs(_ context.Context, _ time.Time, _ time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovered++
	return int64(f.recovered), f.recoverErr
}
func (f *fakeRepository) DeleteExpiredSessions(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedBefore = before
	return 3, f.deleteErr
}
func testRunner(repo Repository) *Runner {
	return New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond, 10)
}

func TestRunOnceDispatchesRegisteredHandlerAndFinishesSuccess(t *testing.T) {
	repo := newFakeRepository()
	repo.jobs = []domain.Job{{ID: 1, Kind: "custom", Payload: "payload", Status: "pending", MaxAttempts: 3}}
	runner := testRunner(repo)
	var got domain.Job
	runner.Register("custom", func(_ context.Context, job domain.Job) error { got = job; return nil })
	runner.RunOnce(context.Background())
	if got.ID != 1 || got.Payload != "payload" || got.Attempts != 1 || got.Status != "running" {
		t.Fatalf("handler got %+v", got)
	}
	if err, ok := repo.finished[1]; !ok || err != nil {
		t.Fatalf("finish=%v present=%v", err, ok)
	}
}

func TestRunOnceRecordsHandlerFailureForRetryPolicy(t *testing.T) {
	repo := newFakeRepository()
	repo.jobs = []domain.Job{{ID: 2, Kind: "custom", MaxAttempts: 3}}
	runner := testRunner(repo)
	sentinel := errors.New("temporary downstream failure")
	runner.Register("custom", func(context.Context, domain.Job) error { return sentinel })
	runner.RunOnce(context.Background())
	if !errors.Is(repo.finished[2], sentinel) {
		t.Fatalf("finished error=%v", repo.finished[2])
	}
}

func TestRunOnceMarksUnknownJobKindAsFailure(t *testing.T) {
	repo := newFakeRepository()
	repo.jobs = []domain.Job{{ID: 3, Kind: "not_registered", MaxAttempts: 2}}
	runner := testRunner(repo)
	runner.RunOnce(context.Background())
	err := repo.finished[3]
	if err == nil || err.Error() != `unknown job kind "not_registered"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOnceRespectsBatchLimit(t *testing.T) {
	repo := newFakeRepository()
	for i := int64(1); i <= 5; i++ {
		repo.jobs = append(repo.jobs, domain.Job{ID: i, Kind: "custom"})
	}
	runner := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond, 2)
	var handled []int64
	runner.Register("custom", func(_ context.Context, job domain.Job) error { handled = append(handled, job.ID); return nil })
	runner.RunOnce(context.Background())
	if len(handled) != 2 || handled[0] != 1 || handled[1] != 2 {
		t.Fatalf("handled=%v", handled)
	}
	if len(repo.jobs) != 3 {
		t.Fatalf("remaining=%d", len(repo.jobs))
	}
}

func TestRunOnceStopsDispatchAfterContextCancellation(t *testing.T) {
	repo := newFakeRepository()
	repo.jobs = []domain.Job{{ID: 1, Kind: "custom"}, {ID: 2, Kind: "custom"}}
	runner := testRunner(repo)
	ctx, cancel := context.WithCancel(context.Background())
	handled := 0
	runner.Register("custom", func(context.Context, domain.Job) error { handled++; cancel(); return nil })
	runner.RunOnce(ctx)
	if handled != 1 {
		t.Fatalf("handled=%d want 1", handled)
	}
	if _, ok := repo.finished[2]; ok {
		t.Fatal("second claimed job should not be finalized without execution")
	}
}

func TestRunOnceReturnsAfterClaimFailureWithoutFinishing(t *testing.T) {
	repo := newFakeRepository()
	repo.claimErr = errors.New("database offline")
	runner := testRunner(repo)
	runner.RunOnce(context.Background())
	if len(repo.finished) != 0 {
		t.Fatalf("unexpected finishes: %v", repo.finished)
	}
}

func TestExpirationHandlerUsesPayloadBoundary(t *testing.T) {
	repo := newFakeRepository()
	runner := testRunner(repo)
	job := domain.Job{Payload: `{"before":"2026-08-24T07:30:00Z"}`}
	if err := runner.expireSessions(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 24, 7, 30, 0, 0, time.UTC)
	if !repo.deletedBefore.Equal(want) {
		t.Fatalf("before=%s want %s", repo.deletedBefore, want)
	}
}

func TestExpirationHandlerUsesRunnerClockWhenPayloadEmpty(t *testing.T) {
	repo := newFakeRepository()
	runner := testRunner(repo)
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }
	if err := runner.expireSessions(context.Background(), domain.Job{Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	if !repo.deletedBefore.Equal(now) {
		t.Fatalf("before=%s want %s", repo.deletedBefore, now)
	}
}

func TestExpirationHandlerRejectsMalformedPayloadAndTimestamp(t *testing.T) {
	repo := newFakeRepository()
	runner := testRunner(repo)
	for _, payload := range []string{`{`, `{"before":"tomorrow"}`} {
		if err := runner.expireSessions(context.Background(), domain.Job{Payload: payload}); err == nil {
			t.Fatalf("payload %q accepted", payload)
		}
	}
}

func TestExpirationHandlerPreservesRepositoryFailure(t *testing.T) {
	repo := newFakeRepository()
	sentinel := errors.New("delete blocked")
	repo.deleteErr = sentinel
	runner := testRunner(repo)
	if err := runner.expireSessions(context.Background(), domain.Job{Payload: `{}`}); !errors.Is(err, sentinel) {
		t.Fatalf("got %v want sentinel", err)
	}
}

func TestStartRecoversInterruptedJobsBeforePolling(t *testing.T) {
	repo := newFakeRepository()
	runner := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond, 1)
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	deadline := time.Now().Add(time.Second)
	for {
		repo.mu.Lock()
		recovered := repo.recovered
		repo.mu.Unlock()
		if recovered > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery was not called")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	runner.Wait()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.recovered != 1 {
		t.Fatalf("recover calls=%d", repo.recovered)
	}
}

func TestStartStopsPromptlyWhenContextCanceled(t *testing.T) {
	repo := newFakeRepository()
	runner := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 1)
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() { runner.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestRegisteredHandlerCanObserveAttemptContext(t *testing.T) {
	repo := newFakeRepository()
	repo.jobs = []domain.Job{{ID: 10, Kind: "context"}}
	runner := testRunner(repo)
	runner.Register("context", func(ctx context.Context, _ domain.Job) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("attempt context has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > 31*time.Second {
			t.Fatalf("deadline remaining=%s", remaining)
		}
		return nil
	})
	runner.RunOnce(context.Background())
}
