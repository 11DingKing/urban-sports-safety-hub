package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type blockingFinalizationRepository struct {
	mu            sync.Mutex
	claimed       bool
	finishStarted chan struct{}
	releaseFinish chan struct{}
	finishDone    chan struct{}
}

func (r *blockingFinalizationRepository) ClaimJobs(context.Context, int, time.Time) ([]domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return nil, nil
	}
	r.claimed = true
	return []domain.Job{{ID: 71, Kind: "inspection", Status: "running", Attempts: 1, MaxAttempts: 3}}, nil
}

func (r *blockingFinalizationRepository) FinishJob(context.Context, int64, error, time.Time) error {
	close(r.finishStarted)
	<-r.releaseFinish
	close(r.finishDone)
	return nil
}

func (*blockingFinalizationRepository) RecoverJobs(context.Context, time.Time, time.Time) (int64, error) {
	return 0, nil
}

func (*blockingFinalizationRepository) DeleteExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestSlowFinalizationDoesNotOutliveRunnerWait(t *testing.T) {
	repository := &blockingFinalizationRepository{
		finishStarted: make(chan struct{}),
		releaseFinish: make(chan struct{}),
		finishDone:    make(chan struct{}),
	}
	runner := New(repository, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond, 1)
	handled := make(chan struct{})
	runner.Register("inspection", func(context.Context, domain.Job) error {
		close(handled)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("inspection handler did not run")
	}
	select {
	case <-repository.finishStarted:
	case <-time.After(time.Second):
		t.Fatal("job finalization did not start")
	}

	cancel()
	waitReturned := make(chan struct{})
	go func() {
		runner.Wait()
		close(waitReturned)
	}()
	select {
	case <-waitReturned:
		t.Fatal("Wait returned while job finalization was still owned by the runner")
	case <-time.After(20 * time.Millisecond):
	}

	close(repository.releaseFinish)
	select {
	case <-repository.finishDone:
	case <-time.After(time.Second):
		t.Fatal("blocked finalization did not finish after release")
	}
	select {
	case <-waitReturned:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after finalization completed")
	}
}
