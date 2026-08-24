package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type TrainingAssignmentSnapshot struct {
	GroupSession, GroupCoachAccount int64
	Capacity, MemberCount           int
	EnrollmentSession               int64
	EnrollmentStatus                string
}

func (s *Store) TrainingAssignmentSnapshot(ctx context.Context, groupID, enrollmentID int64) (TrainingAssignmentSnapshot, error) {
	var snapshot TrainingAssignmentSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT tg.session_id,c.account_id,tg.capacity,(SELECT COUNT(*) FROM group_members gm WHERE gm.group_id=tg.id) FROM training_groups tg JOIN coaches c ON c.id=tg.coach_id WHERE tg.id=?`, groupID).Scan(&snapshot.GroupSession, &snapshot.GroupCoachAccount, &snapshot.Capacity, &snapshot.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, domain.NewError(domain.KindNotFound, "group_not_found", "training group was not found")
	}
	if err != nil {
		return snapshot, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT session_id,status FROM enrollments WHERE id=?`, enrollmentID).Scan(&snapshot.EnrollmentSession, &snapshot.EnrollmentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, domain.NewError(domain.KindNotFound, "enrollment_not_found", "enrollment was not found")
	}
	return snapshot, err
}

func (s *Store) ReserveTrainingGroup(ctx context.Context, tx *sql.Tx, groupID int64, expectedVersion int) error {
	result, err := tx.ExecContext(ctx, `UPDATE training_groups SET version=version+1 WHERE id=? AND version=?`, groupID, expectedVersion)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return domain.ErrConflict
	}
	return nil
}
