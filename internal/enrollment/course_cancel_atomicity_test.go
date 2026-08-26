package enrollment

import (
	"context"
	"testing"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

func TestCancellationFailureLeavesCourseAndMakeupStateUnchanged(t *testing.T) {
	f := newEnrollmentFixture(t)
	enrolled, err := f.service.Enroll(context.Background(), principal(f.guardianAccount, domain.RoleGuardian), "setup-enrollment", Request{
		StudentID: f.student, SessionID: f.session, IdempotencyKey: "atomic-cancel-source",
	})
	if err != nil {
		t.Fatalf("prepare confirmed enrollment: %v", err)
	}
	if _, err := f.store.DB().Exec(`CREATE TRIGGER reject_cancellation_audit BEFORE INSERT ON audit_events WHEN NEW.action='course.canceled' BEGIN SELECT RAISE(ABORT, 'audit storage unavailable'); END`); err != nil {
		t.Fatalf("install deterministic audit failure: %v", err)
	}

	_, err = f.service.CancelCourse(context.Background(), principal(f.guardianAccount, domain.RoleAdministrator), "failed-cancel", f.session, "wall inspection failed")
	if err == nil {
		t.Fatal("cancellation unexpectedly succeeded while its audit write was rejected")
	}
	var sessionStatus, cancelReason, enrollmentStatus string
	var makeupCount int
	if err := f.store.DB().QueryRow(`SELECT status,cancel_reason FROM course_sessions WHERE id=?`, f.session).Scan(&sessionStatus, &cancelReason); err != nil {
		t.Fatalf("read course after failed cancellation: %v", err)
	}
	if err := f.store.DB().QueryRow(`SELECT status FROM enrollments WHERE id=?`, enrolled.ID).Scan(&enrollmentStatus); err != nil {
		t.Fatalf("read enrollment after failed cancellation: %v", err)
	}
	if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM makeup_entitlements WHERE enrollment_id=?`, enrolled.ID).Scan(&makeupCount); err != nil {
		t.Fatalf("count makeup entitlements: %v", err)
	}
	if sessionStatus != "scheduled" || cancelReason != "" || enrollmentStatus != "confirmed" || makeupCount != 0 {
		t.Fatalf("failed cancellation leaked state: session=%s reason=%q enrollment=%s makeups=%d", sessionStatus, cancelReason, enrollmentStatus, makeupCount)
	}

	if _, err := f.store.DB().Exec(`DROP TRIGGER reject_cancellation_audit`); err != nil {
		t.Fatalf("remove audit failure: %v", err)
	}
	created, err := f.service.CancelCourse(context.Background(), principal(f.guardianAccount, domain.RoleAdministrator), "successful-cancel", f.session, "wall inspection failed")
	if err != nil || created != 1 {
		t.Fatalf("valid cancellation did not complete atomically: makeups=%d error=%v", created, err)
	}
}
