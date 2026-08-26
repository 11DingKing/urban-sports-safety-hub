package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) AppendAudit(ctx context.Context, exec Executor, event domain.AuditEvent) (int64, error) {
	if strings.TrimSpace(event.RequestID) == "" || event.ObjectID < 1 || strings.TrimSpace(event.Action) == "" {
		return 0, domain.NewError(domain.KindInvalid, "invalid_audit", "audit request, object and action are required")
	}
	event.CreatedAt = s.now()
	result, err := exec.ExecContext(ctx, `INSERT INTO audit_events(actor_id,request_id,object_type,object_id,action,result,detail,created_at) VALUES(?,?,?,?,?,?,?,?)`, event.ActorID, event.RequestID, event.ObjectType, event.ObjectID, event.Action, event.Result, event.Detail, timeText(event.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("append audit event: %w", err)
	}
	return result.LastInsertId()
}

func (s *Store) ListAudit(ctx context.Context, objectType string, objectID int64, limit, offset int) ([]domain.AuditEvent, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor_id,request_id,object_type,object_id,action,result,detail,created_at FROM audit_events WHERE object_type=? AND object_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, objectType, objectID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.AuditEvent, 0)
	for rows.Next() {
		var e domain.AuditEvent
		var actor sql.NullInt64
		var created string
		if err := rows.Scan(&e.ID, &actor, &e.RequestID, &e.ObjectType, &e.ObjectID, &e.Action, &e.Result, &e.Detail, &created); err != nil {
			return nil, err
		}
		if actor.Valid {
			v := actor.Int64
			e.ActorID = &v
		}
		e.CreatedAt, _ = parseTime(created)
		events = append(events, e)
	}
	return events, rows.Err()
}
