package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"log/slog"
	"sync"
	"time"
)

type Repository interface {
	ClaimJobs(context.Context, int, time.Time) ([]domain.Job, error)
	FinishJob(context.Context, int64, error, time.Time) error
	RecoverJobs(context.Context, time.Time, time.Time) (int64, error)
	DeleteExpiredSessions(context.Context, time.Time) (int64, error)
}
type Handler func(context.Context, domain.Job) error
type Runner struct {
	repo     Repository
	logger   *slog.Logger
	interval time.Duration
	batch    int
	handlers map[string]Handler
	now      func() time.Time
	wg       sync.WaitGroup
}

func New(repo Repository, logger *slog.Logger, interval time.Duration, batch int) *Runner {
	r := &Runner{repo: repo, logger: logger, interval: interval, batch: batch, handlers: map[string]Handler{}, now: func() time.Time { return time.Now().UTC() }}
	r.handlers["expire_sessions"] = r.expireSessions
	return r
}
func (r *Runner) Register(kind string, handler Handler) { r.handlers[kind] = handler }
func (r *Runner) Start(ctx context.Context) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		_, err := r.repo.RecoverJobs(ctx, r.now().Add(-5*time.Minute), r.now())
		if err != nil && !errors.Is(err, context.Canceled) {
			r.logger.Error("worker recovery failed", "error", err)
		}
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.RunOnce(ctx)
			}
		}
	}()
}
func (r *Runner) Wait() { r.wg.Wait() }
func (r *Runner) RunOnce(ctx context.Context) {
	jobs, err := r.repo.ClaimJobs(ctx, r.batch, r.now())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			r.logger.Error("job claim failed", "error", err)
		}
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		handler, ok := r.handlers[job.Kind]
		if !ok {
			err = fmt.Errorf("unknown job kind %q", job.Kind)
		} else {
			attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = handler(attemptCtx, job)
			cancel()
		}
		r.finishSoon(ctx, job, err)
	}
}

func (r *Runner) finishSoon(ctx context.Context, job domain.Job, runErr error) {
	done := make(chan error, 1)
	finishCtx := context.WithoutCancel(ctx)
	go func() {
		done <- r.repo.FinishJob(finishCtx, job.ID, runErr, r.now())
	}()
	select {
	case finishErr := <-done:
		if finishErr != nil {
			r.logger.Error("job finalization failed", "job_id", job.ID, "error", finishErr)
		} else if runErr != nil {
			r.logger.Warn("job attempt failed", "job_id", job.ID, "attempt", job.Attempts, "error", runErr)
		}
	case <-time.After(5 * time.Millisecond):
		r.logger.Warn("job finalization continues after polling", "job_id", job.ID)
	}
}

func (r *Runner) expireSessions(ctx context.Context, job domain.Job) error {
	var payload struct {
		Before string `json:"before"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("decode expiration payload: %w", err)
	}
	before := r.now()
	if payload.Before != "" {
		parsed, err := time.Parse(time.RFC3339, payload.Before)
		if err != nil {
			return fmt.Errorf("invalid expiration time: %w", err)
		}
		before = parsed
	}
	_, err := r.repo.DeleteExpiredSessions(ctx, before)
	return err
}
