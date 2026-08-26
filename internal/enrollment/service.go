package enrollment

import (
	"context"
	"database/sql"
	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
	"strings"
	"time"
)

type Service struct {
	store *dbstore.Store
	audit *audit.Service
	now   func() time.Time
}
type Request struct {
	StudentID      int64  `json:"student_id"`
	SessionID      int64  `json:"session_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func New(store *dbstore.Store, auditService *audit.Service) *Service {
	return &Service{store: store, audit: auditService, now: func() time.Time { return time.Now().UTC() }}
}
func (s *Service) SetClock(now func() time.Time) { s.now = now }
func (s *Service) Enroll(ctx context.Context, principal domain.Principal, requestID string, req Request) (domain.Enrollment, error) {
	if !domain.RoleAllowed(principal.Role, domain.RoleGuardian, domain.RoleAdministrator) {
		return domain.Enrollment{}, domain.NewError(domain.KindForbidden, "enrollment_forbidden", "only guardians and administrators may enroll students")
	}
	if req.StudentID < 1 || req.SessionID < 1 || strings.TrimSpace(req.IdempotencyKey) == "" {
		return domain.Enrollment{}, domain.NewError(domain.KindInvalid, "invalid_enrollment", "student, session and idempotency key are required")
	}
	if existing, err := s.store.EnrollmentByKey(ctx, req.IdempotencyKey); err == nil {
		if existing.StudentID != req.StudentID || existing.SessionID != req.SessionID {
			return domain.Enrollment{}, domain.NewError(domain.KindConflict, "idempotency_payload_mismatch", "idempotency key was used for a different enrollment")
		}
		return existing, nil
	}
	var result domain.Enrollment
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := s.store.EnrollmentSnapshot(ctx, tx, req.StudentID, req.SessionID, s.now())
		if err != nil {
			return err
		}
		if snapshot.Session.Status != "scheduled" || !snapshot.Session.StartsAt.After(s.now()) {
			return domain.NewError(domain.KindConflict, "session_closed", "course session is not open for enrollment")
		}
		age := snapshot.Session.StartsAt.Year() - snapshot.Student.BirthDate.Year()
		if snapshot.Student.BirthDate.AddDate(age, 0, 0).After(snapshot.Session.StartsAt) {
			age--
		}
		if age < snapshot.Template.MinimumAge {
			return domain.NewError(domain.KindForbidden, "minimum_age_not_met", "student does not meet the minimum age")
		}
		if domain.IsMinor(snapshot.Student.BirthDate, snapshot.Session.StartsAt) && !snapshot.ConsentValid {
			return domain.NewError(domain.KindForbidden, "consent_required", "active guardian consent is required")
		}
		if !snapshot.PrerequisitesMet {
			return domain.NewError(domain.KindForbidden, "prerequisite_missing", "course prerequisite certification is missing")
		}
		if !snapshot.CoachQualified {
			return domain.NewError(domain.KindForbidden, "coach_unqualified", "coach qualification does not cover the course")
		}
		result, err = s.store.ReserveEnrollment(ctx, tx, req.StudentID, snapshot, req.IdempotencyKey)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, principal.AccountID, requestID, "enrollment", result.ID, "enrollment.confirmed", "success", map[string]any{"session_id": req.SessionID, "student_id": req.StudentID})
	})
	return result, err
}
func (s *Service) CancelCourse(ctx context.Context, principal domain.Principal, requestID string, sessionID int64, reason string) (int64, error) {
	if !domain.RoleAllowed(principal.Role, domain.RoleAdministrator) {
		return 0, domain.NewError(domain.KindForbidden, "cancel_forbidden", "only administrators may cancel a course")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, domain.NewError(domain.KindInvalid, "cancel_reason_required", "a cancellation reason is required")
	}
	session, err := s.store.SessionByID(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	if err := domain.ValidateTransition("course", session.Status, "canceled"); err != nil {
		return 0, err
	}
	var count int64
	err = s.store.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		count, err = s.store.CancelSessionRows(ctx, tx, sessionID, reason, session.Version, s.now().AddDate(0, 2, 0))
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, principal.AccountID, requestID, "course_session", sessionID, "course.canceled", "success", map[string]any{"reason": reason, "makeups": count})
	})
	return count, err
}
