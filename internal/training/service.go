package training

import (
	"context"
	"database/sql"
	"errors"
	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
	"time"
)

type Service struct {
	store *dbstore.Store
	audit *audit.Service
}
type AssignRequest struct {
	EnrollmentID, GroupID int64
	ExpectedGroupVersion  int
}

func New(store *dbstore.Store, a *audit.Service) *Service { return &Service{store: store, audit: a} }
func (s *Service) Assign(ctx context.Context, p domain.Principal, requestID string, req AssignRequest) error {
	if !domain.RoleAllowed(p.Role, domain.RoleCoach, domain.RoleAdministrator) {
		return domain.NewError(domain.KindForbidden, "group_assignment_forbidden", "coach role is required")
	}
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var groupSession, groupCoach int64
		var capacity, version int
		err := tx.QueryRowContext(ctx, `SELECT tg.session_id,c.account_id,tg.capacity,tg.version FROM training_groups tg JOIN coaches c ON c.id=tg.coach_id WHERE tg.id=?`, req.GroupID).Scan(&groupSession, &groupCoach, &capacity, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.KindNotFound, "group_not_found", "training group was not found")
		}
		if err != nil {
			return err
		}
		var enrollmentSession int64
		var enrollmentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT session_id,status FROM enrollments WHERE id=?`, req.EnrollmentID).Scan(&enrollmentSession, &enrollmentStatus); err != nil {
			return domain.NewError(domain.KindNotFound, "enrollment_not_found", "enrollment was not found")
		}
		if enrollmentSession != groupSession || enrollmentStatus != "confirmed" {
			return domain.NewError(domain.KindConflict, "enrollment_group_mismatch", "enrollment is not active for this group session")
		}
		if groupCoach != p.AccountID && p.Role != domain.RoleAdministrator {
			return domain.NewError(domain.KindForbidden, "coach_group_mismatch", "coach does not own the group")
		}
		var members int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_members WHERE group_id=?`, req.GroupID).Scan(&members); err != nil {
			return err
		}
		if members >= capacity {
			return domain.NewError(domain.KindConflict, "group_full", "training group is full")
		}
		result, err := tx.ExecContext(ctx, `UPDATE training_groups SET version=version+1 WHERE id=? AND version=?`, req.GroupID, req.ExpectedGroupVersion)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return domain.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_members(group_id,enrollment_id,assigned_at) VALUES(?,?,?)`, req.GroupID, req.EnrollmentID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, p.AccountID, requestID, "training_group", req.GroupID, "group.member_assigned", "success", map[string]any{"enrollment_id": req.EnrollmentID})
	})
}
