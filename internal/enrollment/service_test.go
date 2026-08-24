package enrollment

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

var enrollmentNow = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)

type enrollmentFixture struct {
	store                                                                      *dbstore.Store
	service                                                                    *Service
	guardianAccount, guardian, student, coachAccount, coach, template, session int64
}

func newEnrollmentFixture(t *testing.T) enrollmentFixture {
	t.Helper()
	ctx := context.Background()
	store, err := dbstore.Open(ctx, filepath.Join(t.TempDir(), "enrollment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetClock(func() time.Time { return enrollmentNow })
	f := enrollmentFixture{store: store}
	f.guardianAccount = insert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('guardian@test','h','Guardian','guardian',1,?)`, stamp(enrollmentNow))
	f.guardian = insert(t, store.DB(), `INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'10086',?)`, f.guardianAccount, stamp(enrollmentNow))
	f.student = insert(t, store.DB(), `INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Student',?,'38','M',?)`, f.guardian, stamp(enrollmentNow.AddDate(-12, 0, 0)), stamp(enrollmentNow))
	f.coachAccount = insert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('coach@test','h','Coach','coach',1,?)`, stamp(enrollmentNow))
	f.coach = insert(t, store.DB(), `INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'10010',?)`, f.coachAccount, stamp(enrollmentNow))
	f.template = insert(t, store.DB(), `INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Climbing L1','climbing',1,8,2,2,'')`)
	f.session = insert(t, store.DB(), `INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',2)`, f.template, f.coach, stamp(enrollmentNow.Add(24*time.Hour)), stamp(enrollmentNow.Add(26*time.Hour)))
	insert(t, store.DB(), `INSERT INTO coach_qualifications(coach_id,sport,level,valid_from,valid_until,status) VALUES(?,'climbing',2,?,?,'active')`, f.coach, stamp(enrollmentNow.Add(-24*time.Hour)), stamp(enrollmentNow.AddDate(0, 2, 0)))
	insert(t, store.DB(), `INSERT INTO guardian_consents(student_id,guardian_id,scope,granted_at,expires_at) VALUES(?,?,'sports_participation',?,?)`, f.student, f.guardian, stamp(enrollmentNow.Add(-time.Hour)), stamp(enrollmentNow.AddDate(0, 1, 0)))
	f.service = New(store, audit.New(store))
	f.service.SetClock(func() time.Time { return enrollmentNow })
	return f
}
func insert(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	result, err := db.Exec(query, args...)
	if err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}
func stamp(v time.Time) string { return v.UTC().Format(time.RFC3339Nano) }
func principal(id int64, role domain.Role) domain.Principal {
	return domain.Principal{AccountID: id, Role: role, SessionID: 1, ExpiresAt: enrollmentNow.Add(time.Hour)}
}
func errorCode(err error) string { _, code, _ := domain.ErrorDetails(err); return code }

func TestEnrollConfirmsEligibleMinorAndWritesAudit(t *testing.T) {
	f := newEnrollmentFixture(t)
	result, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req-enroll", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "enroll-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "confirmed" || result.StudentID != f.student || result.SessionID != f.session {
		t.Fatalf("unexpected enrollment: %+v", result)
	}
	var enrolled, version int
	if err := f.store.DB().QueryRow(`SELECT enrolled,version FROM course_sessions WHERE id=?`, f.session).Scan(&enrolled, &version); err != nil {
		t.Fatal(err)
	}
	if enrolled != 1 || version != 2 {
		t.Fatalf("enrolled=%d version=%d", enrolled, version)
	}
	var action, requestID string
	if err := f.store.DB().QueryRow(`SELECT action,request_id FROM audit_events WHERE object_type='enrollment' AND object_id=?`, result.ID).Scan(&action, &requestID); err != nil {
		t.Fatal(err)
	}
	if action != "enrollment.confirmed" || requestID != "req-enroll" {
		t.Fatalf("audit action=%s request=%s", action, requestID)
	}
}

func TestEnrollIsIdempotentForRepeatedRequestKey(t *testing.T) {
	f := newEnrollmentFixture(t)
	p := principal(f.guardianAccount, domain.RoleGuardian)
	first, err := f.service.Enroll(context.Background(), p, "req-1", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.Enroll(context.Background(), p, "req-2", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("different enrollment IDs: %d %d", first.ID, second.ID)
	}
	var enrolled, count int
	_ = f.store.DB().QueryRow(`SELECT enrolled FROM course_sessions WHERE id=?`, f.session).Scan(&enrolled)
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM enrollments`).Scan(&count)
	if enrolled != 1 || count != 1 {
		t.Fatalf("enrolled=%d records=%d", enrolled, count)
	}
}

func TestEnrollRejectsIdempotencyKeyReuseForDifferentPayload(t *testing.T) {
	f := newEnrollmentFixture(t)
	p := principal(f.guardianAccount, domain.RoleGuardian)
	_, err := f.service.Enroll(context.Background(), p, "req-1", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "reused-key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.Enroll(context.Background(), p, "req-2", Request{StudentID: f.student + 99, SessionID: f.session, IdempotencyKey: "reused-key"})
	if errorCode(err) != "idempotency_payload_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
	var count int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM enrollments`).Scan(&count)
	if count != 1 {
		t.Fatalf("records=%d want 1", count)
	}
}

func TestEnrollRejectsUnauthorizedRoles(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleCoach, domain.RoleEquipmentManager} {
		t.Run(string(role), func(t *testing.T) {
			f := newEnrollmentFixture(t)
			_, err := f.service.Enroll(context.Background(), principal(f.coachAccount, role), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "key"})
			if errorCode(err) != "enrollment_forbidden" {
				t.Fatalf("unexpected error: %v", err)
			}
			var count int
			_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM enrollments`).Scan(&count)
			if count != 0 {
				t.Fatal("unauthorized enrollment persisted")
			}
		})
	}
}

func TestEnrollRejectsMissingRequestFields(t *testing.T) {
	cases := []Request{{SessionID: 1, IdempotencyKey: "k"}, {StudentID: 1, IdempotencyKey: "k"}, {StudentID: 1, SessionID: 1}}
	for _, req := range cases {
		f := newEnrollmentFixture(t)
		_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", req)
		if errorCode(err) != "invalid_enrollment" {
			t.Fatalf("request %+v: %v", req, err)
		}
	}
}

func TestEnrollRejectsExpiredGuardianConsent(t *testing.T) {
	f := newEnrollmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE guardian_consents SET expires_at=? WHERE student_id=?`, stamp(enrollmentNow.Add(25*time.Hour)), f.student)
	_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "expired-consent"})
	if errorCode(err) != "consent_required" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoEnrollment(t, f.store.DB())
}

func TestEnrollRejectsRevokedGuardianConsent(t *testing.T) {
	f := newEnrollmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE guardian_consents SET revoked_at=? WHERE student_id=?`, stamp(enrollmentNow), f.student)
	_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "revoked-consent"})
	if errorCode(err) != "consent_required" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoEnrollment(t, f.store.DB())
}

func TestEnrollRejectsMissingCertificationPrerequisite(t *testing.T) {
	f := newEnrollmentFixture(t)
	insert(t, f.store.DB(), `INSERT INTO course_prerequisites(template_id,required_sport,required_level) VALUES(?,'climbing',1)`, f.template)
	_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "missing-prerequisite"})
	if errorCode(err) != "prerequisite_missing" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoEnrollment(t, f.store.DB())
}

func TestEnrollAcceptsUnexpiredCertificationPrerequisite(t *testing.T) {
	f := newEnrollmentFixture(t)
	insert(t, f.store.DB(), `INSERT INTO course_prerequisites(template_id,required_sport,required_level) VALUES(?,'climbing',1)`, f.template)
	insert(t, f.store.DB(), `INSERT INTO certifications(student_id,sport,level,granted_by,granted_at,expires_at) VALUES(?,'climbing',1,?,?,?)`, f.student, f.coach, stamp(enrollmentNow.Add(-time.Hour)), stamp(enrollmentNow.Add(48*time.Hour)))
	if _, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "certified"}); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollRejectsCoachQualificationThatExpiresDuringSession(t *testing.T) {
	f := newEnrollmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE coach_qualifications SET valid_until=? WHERE coach_id=?`, stamp(enrollmentNow.Add(25*time.Hour)), f.coach)
	_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "coach-expired"})
	if errorCode(err) != "coach_unqualified" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoEnrollment(t, f.store.DB())
}

func TestEnrollRejectsMinimumAgeAtCourseStart(t *testing.T) {
	f := newEnrollmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE course_templates SET minimum_age=16 WHERE id=?`, f.template)
	_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "too-young"})
	if errorCode(err) != "minimum_age_not_met" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoEnrollment(t, f.store.DB())
}

func TestEnrollRejectsClosedOrStartedSession(t *testing.T) {
	cases := []struct {
		name, status string
		start        time.Time
	}{{"canceled", "canceled", enrollmentNow.Add(time.Hour)}, {"completed", "completed", enrollmentNow.Add(-2 * time.Hour)}, {"started", "scheduled", enrollmentNow.Add(-time.Minute)}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEnrollmentFixture(t)
			_, _ = f.store.DB().Exec(`UPDATE course_sessions SET status=?,starts_at=?,ends_at=? WHERE id=?`, tc.status, stamp(tc.start), stamp(tc.start.Add(time.Hour)), f.session)
			_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "closed"})
			if errorCode(err) != "session_closed" {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConcurrentEnrollmentDoesNotExceedCapacity(t *testing.T) {
	f := newEnrollmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE course_sessions SET capacity=1 WHERE id=?`, f.session)
	second := insert(t, f.store.DB(), `INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Second',?,'38','M',?)`, f.guardian, stamp(enrollmentNow.AddDate(-12, 0, 0)), stamp(enrollmentNow))
	insert(t, f.store.DB(), `INSERT INTO guardian_consents(student_id,guardian_id,scope,granted_at,expires_at) VALUES(?,?,'sports_participation',?,?)`, second, f.guardian, stamp(enrollmentNow.Add(-time.Hour)), stamp(enrollmentNow.AddDate(0, 1, 0)))
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, studentID := range []int64{f.student, second} {
		i, studentID := i, studentID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: studentID, SessionID: f.session, IdempotencyKey: string(rune('a' + i))})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, failed := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			failed++
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("success=%d failed=%d", success, failed)
	}
	var enrolled, count int
	_ = f.store.DB().QueryRow(`SELECT enrolled FROM course_sessions WHERE id=?`, f.session).Scan(&enrolled)
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM enrollments`).Scan(&count)
	if enrolled != 1 || count != 1 {
		t.Fatalf("enrolled=%d records=%d", enrolled, count)
	}
}

func TestCancelCourseCreatesMakeupsAndAuditInOneTransaction(t *testing.T) {
	f := newEnrollmentFixture(t)
	p := principal(f.guardianAccount, domain.RoleGuardian)
	enrolled, err := f.service.Enroll(context.Background(), p, "enroll", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "cancel-source"})
	if err != nil {
		t.Fatal(err)
	}
	count, err := f.service.CancelCourse(context.Background(), principal(f.guardianAccount, domain.RoleAdministrator), "cancel-request", f.session, "unsafe weather")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("makeups=%d", count)
	}
	var status, reason, enrollmentStatus string
	_ = f.store.DB().QueryRow(`SELECT status,cancel_reason FROM course_sessions WHERE id=?`, f.session).Scan(&status, &reason)
	_ = f.store.DB().QueryRow(`SELECT status FROM enrollments WHERE id=?`, enrolled.ID).Scan(&enrollmentStatus)
	if status != "canceled" || reason != "unsafe weather" || enrollmentStatus != "makeup_due" {
		t.Fatalf("course=%s reason=%s enrollment=%s", status, reason, enrollmentStatus)
	}
	var audits int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE request_id='cancel-request' AND action='course.canceled'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
}

func TestCancelCourseRequiresAdministratorAndReason(t *testing.T) {
	f := newEnrollmentFixture(t)
	if _, err := f.service.CancelCourse(context.Background(), principal(f.coachAccount, domain.RoleCoach), "req", f.session, "reason"); errorCode(err) != "cancel_forbidden" {
		t.Fatalf("unexpected coach error: %v", err)
	}
	if _, err := f.service.CancelCourse(context.Background(), principal(f.guardianAccount, domain.RoleAdministrator), "req", f.session, " "); errorCode(err) != "cancel_reason_required" {
		t.Fatalf("unexpected reason error: %v", err)
	}
}

func TestCancelCourseRejectsTerminalSession(t *testing.T) {
	f := newEnrollmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE course_sessions SET status='completed' WHERE id=?`, f.session)
	_, err := f.service.CancelCourse(context.Background(), principal(f.guardianAccount, domain.RoleAdministrator), "req", f.session, "late")
	if errorCode(err) != "illegal_transition" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnrollmentHonorsCanceledContext(t *testing.T) {
	f := newEnrollmentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.service.Enroll(ctx, principal(f.guardianAccount, domain.RoleGuardian), "req", Request{StudentID: f.student, SessionID: f.session, IdempotencyKey: "canceled-context"})
	if err == nil {
		t.Fatal("expected context-related database error")
	}
	assertNoEnrollment(t, f.store.DB())
}

func assertNoEnrollment(t *testing.T, db *sql.DB) {
	t.Helper()
	var count, enrolled int
	_ = db.QueryRow(`SELECT COUNT(*) FROM enrollments`).Scan(&count)
	_ = db.QueryRow(`SELECT enrolled FROM course_sessions LIMIT 1`).Scan(&enrolled)
	if count != 0 || enrolled != 0 {
		t.Fatalf("partial state: enrollments=%d enrolled=%d", count, enrolled)
	}
}

var _ = errors.Is
