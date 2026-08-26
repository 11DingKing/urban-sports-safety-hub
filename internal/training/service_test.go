package training

import (
	"context"
	"database/sql"
	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
	"path/filepath"
	"testing"
	"time"
)

type trainingFixture struct {
	store                                         *dbstore.Store
	service                                       *Service
	coachAccount, otherAccount, enrollment, group int64
}

func newTrainingFixture(t *testing.T) trainingFixture {
	t.Helper()
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "training.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	f := trainingFixture{store: store}
	guardianAccount := tinsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('guardian@test','h','Guardian','guardian',1,?)`, tts(now))
	guardian := tinsert(t, store.DB(), `INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'1',?)`, guardianAccount, tts(now))
	student := tinsert(t, store.DB(), `INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Student',?,'38','M',?)`, guardian, tts(now.AddDate(-12, 0, 0)), tts(now))
	f.coachAccount = tinsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('coach@test','h','Coach','coach',1,?)`, tts(now))
	coach := tinsert(t, store.DB(), `INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'2',?)`, f.coachAccount, tts(now))
	f.otherAccount = tinsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('other@test','h','Other','coach',1,?)`, tts(now))
	template := tinsert(t, store.DB(), `INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Disc','flying_disc',1,8,10,5,'')`)
	session := tinsert(t, store.DB(), `INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',10)`, template, coach, tts(now.Add(time.Hour)), tts(now.Add(2*time.Hour)))
	f.enrollment = tinsert(t, store.DB(), `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','group-source',?)`, session, student, tts(now))
	f.group = tinsert(t, store.DB(), `INSERT INTO training_groups(session_id,coach_id,name,capacity) VALUES(?,?,'A',1)`, session, coach)
	f.service = New(store, audit.New(store))
	return f
}
func tinsert(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	result, err := db.Exec(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
func tts(v time.Time) string { return v.UTC().Format(time.RFC3339Nano) }
func tcode(err error) string { _, code, _ := domain.ErrorDetails(err); return code }
func TestAssignAddsConfirmedEnrollmentAndAudit(t *testing.T) {
	f := newTrainingFixture(t)
	err := f.service.Assign(context.Background(), domain.Principal{AccountID: f.coachAccount, Role: domain.RoleCoach}, "group-request", AssignRequest{EnrollmentID: f.enrollment, GroupID: f.group, ExpectedGroupVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	var members, version, audits int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_id=?`, f.group).Scan(&members)
	_ = f.store.DB().QueryRow(`SELECT version FROM training_groups WHERE id=?`, f.group).Scan(&version)
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE request_id='group-request'`).Scan(&audits)
	if members != 1 || version != 2 || audits != 1 {
		t.Fatalf("members=%d version=%d audits=%d", members, version, audits)
	}
}
func TestAssignRejectsWrongCoachOwnership(t *testing.T) {
	f := newTrainingFixture(t)
	err := f.service.Assign(context.Background(), domain.Principal{AccountID: f.otherAccount, Role: domain.RoleCoach}, "request", AssignRequest{EnrollmentID: f.enrollment, GroupID: f.group, ExpectedGroupVersion: 1})
	if tcode(err) != "coach_group_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestAssignRejectsCanceledEnrollment(t *testing.T) {
	f := newTrainingFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE enrollments SET status='canceled' WHERE id=?`, f.enrollment)
	err := f.service.Assign(context.Background(), domain.Principal{AccountID: f.coachAccount, Role: domain.RoleCoach}, "request", AssignRequest{EnrollmentID: f.enrollment, GroupID: f.group, ExpectedGroupVersion: 1})
	if tcode(err) != "enrollment_group_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestAssignRejectsFullGroup(t *testing.T) {
	f := newTrainingFixture(t)
	tinsert(t, f.store.DB(), `INSERT INTO group_members(group_id,enrollment_id,assigned_at) VALUES(?,?,?)`, f.group, f.enrollment, tts(time.Now()))
	second := f.enrollment + 99
	_ = second
	err := f.service.Assign(context.Background(), domain.Principal{AccountID: f.coachAccount, Role: domain.RoleCoach}, "request", AssignRequest{EnrollmentID: f.enrollment, GroupID: f.group, ExpectedGroupVersion: 1})
	if tcode(err) != "group_full" {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestAssignRejectsStaleVersion(t *testing.T) {
	f := newTrainingFixture(t)
	err := f.service.Assign(context.Background(), domain.Principal{AccountID: f.coachAccount, Role: domain.RoleCoach}, "request", AssignRequest{EnrollmentID: f.enrollment, GroupID: f.group, ExpectedGroupVersion: 9})
	if err == nil {
		t.Fatal("expected conflict")
	}
	var count int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM group_members`).Scan(&count)
	if count != 0 {
		t.Fatalf("partial membership=%d", count)
	}
}
func TestAssignRejectsGuardianRole(t *testing.T) {
	f := newTrainingFixture(t)
	err := f.service.Assign(context.Background(), domain.Principal{AccountID: 1, Role: domain.RoleGuardian}, "request", AssignRequest{EnrollmentID: f.enrollment, GroupID: f.group, ExpectedGroupVersion: 1})
	if tcode(err) != "group_assignment_forbidden" {
		t.Fatalf("unexpected error: %v", err)
	}
}
