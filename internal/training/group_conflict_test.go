package training

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

func TestConflictingGroupAssignmentPreservesVersionForNextStudent(t *testing.T) {
	ctx := context.Background()
	store, err := dbstore.Open(ctx, filepath.Join(t.TempDir(), "group-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	insert := func(query string, args ...any) int64 {
		result, insertErr := store.DB().ExecContext(ctx, query, args...)
		if insertErr != nil {
			t.Fatalf("seed scenario: %v", insertErr)
		}
		id, insertErr := result.LastInsertId()
		if insertErr != nil {
			t.Fatalf("read inserted id: %v", insertErr)
		}
		return id
	}
	stamp := now.Format(time.RFC3339Nano)
	guardianAccount := insert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('group-guardian@test','h','Guardian','guardian',1,?)`, stamp)
	guardian := insert(`INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'1',?)`, guardianAccount, stamp)
	studentOne := insert(`INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'First',?,'38','M',?)`, guardian, now.AddDate(-12, 0, 0).Format(time.RFC3339Nano), stamp)
	studentTwo := insert(`INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Second',?,'39','M',?)`, guardian, now.AddDate(-13, 0, 0).Format(time.RFC3339Nano), stamp)
	coachAccount := insert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('group-coach@test','h','Coach','coach',1,?)`, stamp)
	coach := insert(`INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'2',?)`, coachAccount, stamp)
	template := insert(`INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Disc conflict','flying_disc',1,8,10,5,'')`)
	session := insert(`INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',10)`, template, coach, now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(2*time.Hour).Format(time.RFC3339Nano))
	enrollmentOne := insert(`INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','group-conflict-one',?)`, session, studentOne, stamp)
	enrollmentTwo := insert(`INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','group-conflict-two',?)`, session, studentTwo, stamp)
	groupOne := insert(`INSERT INTO training_groups(session_id,coach_id,name,capacity) VALUES(?,?, 'Established',2)`, session, coach)
	groupTwo := insert(`INSERT INTO training_groups(session_id,coach_id,name,capacity) VALUES(?,?, 'Open',2)`, session, coach)
	insert(`INSERT INTO group_members(group_id,enrollment_id,assigned_at) VALUES(?,?,?)`, groupOne, enrollmentOne, stamp)

	service := New(store, audit.New(store))
	principal := domain.Principal{AccountID: coachAccount, Role: domain.RoleCoach}
	conflictErr := service.Assign(ctx, principal, "conflicting-assignment", AssignRequest{EnrollmentID: enrollmentOne, GroupID: groupTwo, ExpectedGroupVersion: 1})
	if conflictErr == nil {
		t.Fatal("already grouped enrollment unexpectedly joined a second group")
	}

	var versionAfterConflict, membersAfterConflict, conflictAudits int
	if err := store.DB().QueryRowContext(ctx, `SELECT version FROM training_groups WHERE id=?`, groupTwo).Scan(&versionAfterConflict); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM group_members WHERE group_id=?`, groupTwo).Scan(&membersAfterConflict); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE request_id='conflicting-assignment'`).Scan(&conflictAudits); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}

	nextErr := service.Assign(ctx, principal, "next-valid-assignment", AssignRequest{EnrollmentID: enrollmentTwo, GroupID: groupTwo, ExpectedGroupVersion: 1})
	var finalVersion, finalMembers, successAudits int
	_ = store.DB().QueryRowContext(ctx, `SELECT version FROM training_groups WHERE id=?`, groupTwo).Scan(&finalVersion)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM group_members WHERE group_id=?`, groupTwo).Scan(&finalMembers)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE request_id='next-valid-assignment'`).Scan(&successAudits)
	if versionAfterConflict != 1 || membersAfterConflict != 0 || conflictAudits != 0 {
		t.Fatalf("failed assignment leaked target state: version=%d members=%d audits=%d", versionAfterConflict, membersAfterConflict, conflictAudits)
	}
	if nextErr != nil || finalVersion != 2 || finalMembers != 1 || successAudits != 1 {
		t.Fatalf("next valid assignment was blocked: err=%v version=%d members=%d audits=%d", nextErr, finalVersion, finalMembers, successAudits)
	}
}
