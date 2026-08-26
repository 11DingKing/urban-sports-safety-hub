package enrollment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

func TestDuplicateEnrollmentFailurePreservesCapacityForAnotherStudent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	store, err := dbstore.Open(ctx, filepath.Join(t.TempDir(), "duplicate-enrollment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetClock(func() time.Time { return now })

	mustInsert := func(query string, args ...any) int64 {
		t.Helper()
		result, err := store.DB().ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatalf("prepare enrollment scenario: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read inserted id: %v", err)
		}
		return id
	}
	stamp := func(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

	guardianAccount := mustInsert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('capacity-guardian@test','h','Capacity Guardian','guardian',1,?)`, stamp(now))
	guardian := mustInsert(`INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'10000',?)`, guardianAccount, stamp(now))
	firstStudent := mustInsert(`INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'First Student',?,'38','M',?)`, guardian, stamp(now.AddDate(-12, 0, 0)), stamp(now))
	secondStudent := mustInsert(`INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Second Student',?,'39','M',?)`, guardian, stamp(now.AddDate(-13, 0, 0)), stamp(now))
	coachAccount := mustInsert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('capacity-coach@test','h','Capacity Coach','coach',1,?)`, stamp(now))
	coach := mustInsert(`INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'10001',?)`, coachAccount, stamp(now))
	template := mustInsert(`INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Capacity Climbing','climbing',1,8,2,2,'')`)
	session := mustInsert(`INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',2)`, template, coach, stamp(now.Add(24*time.Hour)), stamp(now.Add(26*time.Hour)))
	mustInsert(`INSERT INTO coach_qualifications(coach_id,sport,level,valid_from,valid_until,status) VALUES(?,'climbing',2,?,?,'active')`, coach, stamp(now.Add(-time.Hour)), stamp(now.AddDate(0, 2, 0)))
	for _, studentID := range []int64{firstStudent, secondStudent} {
		mustInsert(`INSERT INTO guardian_consents(student_id,guardian_id,scope,granted_at,expires_at) VALUES(?,?,'sports_participation',?,?)`, studentID, guardian, stamp(now.Add(-time.Hour)), stamp(now.AddDate(0, 1, 0)))
	}

	service := New(store, audit.New(store))
	service.SetClock(func() time.Time { return now })
	actor := domain.Principal{AccountID: guardianAccount, Role: domain.RoleGuardian, SessionID: 1, ExpiresAt: now.Add(time.Hour)}
	if _, err := service.Enroll(ctx, actor, "initial-enrollment", Request{StudentID: firstStudent, SessionID: session, IdempotencyKey: "initial-key"}); err != nil {
		t.Fatalf("initial enrollment failed: %v", err)
	}

	var beforeEnrolled, beforeVersion, beforeAudits int
	if err := store.DB().QueryRowContext(ctx, `SELECT enrolled,version FROM course_sessions WHERE id=?`, session).Scan(&beforeEnrolled, &beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE object_type='enrollment'`).Scan(&beforeAudits); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enroll(ctx, actor, "duplicate-enrollment", Request{StudentID: firstStudent, SessionID: session, IdempotencyKey: "different-key"}); err == nil {
		t.Error("duplicate enrollment unexpectedly succeeded")
	}

	var afterEnrolled, afterVersion, afterAudits int
	if err := store.DB().QueryRowContext(ctx, `SELECT enrolled,version FROM course_sessions WHERE id=?`, session).Scan(&afterEnrolled, &afterVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE object_type='enrollment'`).Scan(&afterAudits); err != nil {
		t.Fatal(err)
	}
	if afterEnrolled != beforeEnrolled || afterVersion != beforeVersion || afterAudits != beforeAudits {
		t.Errorf("duplicate failure changed state: capacity %d/%d -> %d/%d, audits %d -> %d", beforeEnrolled, beforeVersion, afterEnrolled, afterVersion, beforeAudits, afterAudits)
	}

	second, secondErr := service.Enroll(ctx, actor, "second-enrollment", Request{StudentID: secondStudent, SessionID: session, IdempotencyKey: "second-key"})
	if secondErr != nil {
		t.Errorf("remaining seat was unavailable to another eligible student: %v", secondErr)
	} else if second.StudentID != secondStudent || second.Status != "confirmed" {
		t.Errorf("unexpected second enrollment: %+v", second)
	}

	var finalEnrolled, finalVersion, finalEnrollments, finalAudits int
	row := store.DB().QueryRowContext(ctx, `SELECT enrolled,version,(SELECT COUNT(*) FROM enrollments),(SELECT COUNT(*) FROM audit_events WHERE object_type='enrollment') FROM course_sessions WHERE id=?`, session)
	if err := row.Scan(&finalEnrolled, &finalVersion, &finalEnrollments, &finalAudits); err != nil {
		t.Fatal(err)
	}
	if finalEnrolled != 2 || finalVersion != 3 || finalEnrollments != 2 || finalAudits != 2 {
		t.Errorf("final state enrolled=%d version=%d enrollments=%d audits=%d; want 2,3,2,2", finalEnrolled, finalVersion, finalEnrollments, finalAudits)
	}
}
