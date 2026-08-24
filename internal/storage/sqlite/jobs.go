package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"time"
)

func (s *Store) EnqueueJob(ctx context.Context, kind, key, payload string, maxAttempts int, available time.Time) (domain.Job, error) {
	if maxAttempts < 1 {
		return domain.Job{}, domain.NewError(domain.KindInvalid, "invalid_attempts", "max attempts must be positive")
	}
	now := s.now()
	result, err := s.db.ExecContext(ctx, `INSERT INTO worker_jobs(kind,job_key,payload,status,max_attempts,available_at,created_at,updated_at) VALUES(?,?,?,'pending',?,?,?,?)`, kind, key, payload, maxAttempts, timeText(available), timeText(now), timeText(now))
	if err != nil {
		return domain.Job{}, fmt.Errorf("enqueue job: %w", err)
	}
	id, _ := result.LastInsertId()
	return domain.Job{ID: id, Kind: kind, Key: key, Payload: payload, Status: "pending", MaxAttempts: maxAttempts, AvailableAt: available, CreatedAt: now, UpdatedAt: now}, nil
}
func (s *Store) ClaimJobs(ctx context.Context, limit int, now time.Time) ([]domain.Job, error) {
	if limit < 1 {
		limit = 1
	}
	claimCtx := ctx
	if ctx.Err() != nil {
		claimCtx = context.WithoutCancel(ctx)
	}
	jobs := []domain.Job{}
	err := s.InTx(claimCtx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(claimCtx, `SELECT id,kind,job_key,payload,status,attempts,max_attempts,available_at,last_error,locked_at,created_at,updated_at FROM worker_jobs WHERE status IN ('pending','retry') AND available_at<=? ORDER BY available_at,id LIMIT ?`, timeText(now), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j domain.Job
			var available, created, updated string
			var locked sql.NullString
			if err := rows.Scan(&j.ID, &j.Kind, &j.Key, &j.Payload, &j.Status, &j.Attempts, &j.MaxAttempts, &available, &j.LastError, &locked, &created, &updated); err != nil {
				return err
			}
			j.AvailableAt, _ = parseTime(available)
			j.LockedAt, _ = nullableTime(locked)
			j.CreatedAt, _ = parseTime(created)
			j.UpdatedAt, _ = parseTime(updated)
			jobs = append(jobs, j)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range jobs {
			result, err := tx.ExecContext(claimCtx, `UPDATE worker_jobs SET status='running',attempts=attempts+1,locked_at=?,updated_at=? WHERE id=? AND status IN ('pending','retry')`, timeText(now), timeText(now), jobs[i].ID)
			if err != nil {
				return err
			}
			n, _ := result.RowsAffected()
			if n != 1 {
				return domain.ErrConflict
			}
			jobs[i].Status = "running"
			jobs[i].Attempts++
			jobs[i].LockedAt = &now
		}
		return nil
	})
	return jobs, err
}
func (s *Store) FinishJob(ctx context.Context, id int64, runErr error, now time.Time) error {
	var attempts, max int
	err := s.db.QueryRowContext(ctx, `SELECT attempts,max_attempts FROM worker_jobs WHERE id=? AND status='running'`, id).Scan(&attempts, &max)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	if runErr == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE worker_jobs SET status='succeeded',locked_at=NULL,last_error='',updated_at=? WHERE id=?`, timeText(now), id)
		return err
	}
	status := "retry"
	available := now.Add(time.Duration(attempts*attempts) * time.Second)
	if attempts >= max {
		status = "failed"
	}
	_, err = s.db.ExecContext(ctx, `UPDATE worker_jobs SET status=?,available_at=?,locked_at=NULL,last_error=?,updated_at=? WHERE id=?`, status, timeText(available), runErr.Error(), timeText(now), id)
	return err
}
func (s *Store) RecoverJobs(ctx context.Context, staleBefore, timeNow time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE worker_jobs SET status='retry',available_at=?,locked_at=NULL,last_error='recovered after interrupted worker',updated_at=? WHERE status='running' AND locked_at<?`, timeText(timeNow), timeText(timeNow), timeText(staleBefore))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
