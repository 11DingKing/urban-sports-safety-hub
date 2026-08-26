package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type EnrollmentSnapshot struct {
	Student          domain.Student
	Session          domain.CourseSession
	Template         domain.CourseTemplate
	ConsentValid     bool
	PrerequisitesMet bool
	CoachQualified   bool
}

func (s *Store) EnrollmentSnapshot(ctx context.Context, tx *sql.Tx, studentID, sessionID int64, at time.Time) (EnrollmentSnapshot, error) {
	var out EnrollmentSnapshot
	var birth, created, start, end string
	err := tx.QueryRowContext(ctx, `SELECT st.id,st.guardian_id,st.name,st.birth_date,st.shoe_size,st.helmet_size,st.version,st.created_at,cs.id,cs.template_id,cs.coach_id,cs.starts_at,cs.ends_at,cs.status,cs.capacity,cs.enrolled,cs.version,cs.cancel_reason,ct.id,ct.name,ct.sport,ct.level,ct.minimum_age,ct.capacity,ct.coach_ratio,ct.required_certification FROM students st CROSS JOIN course_sessions cs JOIN course_templates ct ON ct.id=cs.template_id WHERE st.id=? AND cs.id=?`, studentID, sessionID).Scan(&out.Student.ID, &out.Student.GuardianID, &out.Student.Name, &birth, &out.Student.ShoeSize, &out.Student.HelmetSize, &out.Student.Version, &created, &out.Session.ID, &out.Session.TemplateID, &out.Session.CoachID, &start, &end, &out.Session.Status, &out.Session.Capacity, &out.Session.Enrolled, &out.Session.Version, &out.Session.CancelReason, &out.Template.ID, &out.Template.Name, &out.Template.Sport, &out.Template.Level, &out.Template.MinimumAge, &out.Template.Capacity, &out.Template.CoachRatio, &out.Template.RequiredCertification)
	if errors.Is(err, sql.ErrNoRows) {
		return out, domain.NewError(domain.KindNotFound, "enrollment_target_not_found", "student or course session was not found")
	}
	if err != nil {
		return out, err
	}
	out.Student.BirthDate, _ = parseTime(birth)
	out.Student.CreatedAt, _ = parseTime(created)
	out.Session.StartsAt, _ = parseTime(start)
	out.Session.EndsAt, _ = parseTime(end)
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM guardian_consents WHERE student_id=? AND scope='sports_participation' AND granted_at<=? AND expires_at>? AND revoked_at IS NULL`, studentID, timeText(at), timeText(out.Session.EndsAt)).Scan(&count)
	if err != nil {
		return out, err
	}
	out.ConsentValid = count > 0
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM course_prerequisites p WHERE p.template_id=? AND NOT EXISTS(SELECT 1 FROM certifications c WHERE c.student_id=? AND c.sport=p.required_sport AND c.level>=p.required_level AND (c.expires_at IS NULL OR c.expires_at>?))`, out.Template.ID, studentID, timeText(out.Session.StartsAt)).Scan(&count)
	if err != nil {
		return out, err
	}
	out.PrerequisitesMet = count == 0
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM coach_qualifications q WHERE q.coach_id=? AND q.sport=? AND q.level>=? AND q.status='active' AND q.valid_from<=? AND q.valid_until>=?`, out.Session.CoachID, out.Template.Sport, out.Template.Level, timeText(out.Session.StartsAt), timeText(out.Session.EndsAt)).Scan(&count)
	if err != nil {
		return out, err
	}
	out.CoachQualified = count > 0
	return out, nil
}

func (s *Store) ReserveEnrollment(ctx context.Context, tx *sql.Tx, studentID int64, snapshot EnrollmentSnapshot, key string) (domain.Enrollment, error) {
	result, err := tx.ExecContext(ctx, `UPDATE course_sessions SET enrolled=enrolled+1,version=version+1 WHERE id=? AND version=? AND status='scheduled' AND enrolled<capacity`, snapshot.Session.ID, snapshot.Session.Version)
	if err != nil {
		return domain.Enrollment{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return domain.Enrollment{}, domain.ErrConflict
	}
	now := s.now()
	result, err = tx.ExecContext(ctx, `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed',?,?)`, snapshot.Session.ID, studentID, key, timeText(now))
	if err != nil {
		return domain.Enrollment{}, fmt.Errorf("insert enrollment: %w", err)
	}
	id, err := result.LastInsertId()
	return domain.Enrollment{ID: id, SessionID: snapshot.Session.ID, StudentID: studentID, Status: "confirmed", IdempotencyKey: key, Version: 1, CreatedAt: now}, err
}

func (s *Store) EnrollmentByKey(ctx context.Context, key string) (domain.Enrollment, error) {
	var e domain.Enrollment
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,session_id,student_id,status,idempotency_key,version,created_at FROM enrollments WHERE idempotency_key=?`, key).Scan(&e.ID, &e.SessionID, &e.StudentID, &e.Status, &e.IdempotencyKey, &e.Version, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return e, domain.NewError(domain.KindNotFound, "enrollment_not_found", "enrollment was not found")
	}
	if err != nil {
		return e, err
	}
	e.CreatedAt, _ = parseTime(created)
	return e, nil
}

func (s *Store) CancelSessionRows(ctx context.Context, tx *sql.Tx, sessionID int64, reason string, expectedVersion int, expiry time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `UPDATE course_sessions SET status='canceled',cancel_reason=?,version=version+1 WHERE id=? AND version=? AND status='scheduled'`, reason, sessionID, expectedVersion)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return 0, domain.ErrConflict
	}
	result, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO makeup_entitlements(enrollment_id,canceled_session_id,status,expires_at,created_at) SELECT id,session_id,'available',?,? FROM enrollments WHERE session_id=? AND status='confirmed'`, timeText(expiry), timeText(s.now()), sessionID)
	if err != nil {
		return 0, err
	}
	created, _ := result.RowsAffected()
	_, err = tx.ExecContext(ctx, `UPDATE enrollments SET status='makeup_due',version=version+1 WHERE session_id=? AND status='confirmed'`, sessionID)
	return created, err
}

func (s *Store) CancelSession(ctx context.Context, sessionID int64, reason string, expectedVersion int, expiry time.Time) (int64, error) {
	var count int64
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		count, err = s.CancelSessionRows(ctx, tx, sessionID, reason, expectedVersion, expiry)
		return err
	})
	return count, err
}

func (s *Store) SessionByID(ctx context.Context, id int64) (domain.CourseSession, error) {
	var v domain.CourseSession
	var start, end string
	err := s.db.QueryRowContext(ctx, `SELECT id,template_id,coach_id,starts_at,ends_at,status,capacity,enrolled,version,cancel_reason FROM course_sessions WHERE id=?`, id).Scan(&v.ID, &v.TemplateID, &v.CoachID, &start, &end, &v.Status, &v.Capacity, &v.Enrolled, &v.Version, &v.CancelReason)
	if errors.Is(err, sql.ErrNoRows) {
		return v, domain.NewError(domain.KindNotFound, "session_not_found", "course session was not found")
	}
	v.StartsAt, _ = parseTime(start)
	v.EndsAt, _ = parseTime(end)
	return v, err
}
