package training

import (
	"context"
	"database/sql"
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
	snapshot, err := s.store.TrainingAssignmentSnapshot(ctx, req.GroupID, req.EnrollmentID)
	if err != nil {
		return err
	}
	if snapshot.EnrollmentSession != snapshot.GroupSession || snapshot.EnrollmentStatus != "confirmed" {
		return domain.NewError(domain.KindConflict, "enrollment_group_mismatch", "enrollment is not active for this group session")
	}
	if snapshot.GroupCoachAccount != p.AccountID && p.Role != domain.RoleAdministrator {
		return domain.NewError(domain.KindForbidden, "coach_group_mismatch", "coach does not own the group")
	}
	if snapshot.MemberCount >= snapshot.Capacity {
		return domain.NewError(domain.KindConflict, "group_full", "training group is full")
	}
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		if err := s.store.ReserveTrainingGroup(ctx, tx, req.GroupID, req.ExpectedGroupVersion); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_members(group_id,enrollment_id,assigned_at) VALUES(?,?,?)`, req.GroupID, req.EnrollmentID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, p.AccountID, requestID, "training_group", req.GroupID, "group.member_assigned", "success", map[string]any{"enrollment_id": req.EnrollmentID})
	})
}
