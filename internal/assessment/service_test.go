package assessment

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

var assessmentNow = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)

type assessmentFixture struct {
	store                                                                *dbstore.Store
	service                                                              *Service
	coachAccount, coach, otherCoachAccount, student, session, assessment int64
}

func newAssessmentFixture(t *testing.T) assessmentFixture {
	t.Helper()
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "assessment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetClock(func() time.Time { return assessmentNow })
	f := assessmentFixture{store: store}
	guardianAccount := ainsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('guardian@test','h','Guardian','guardian',1,?)`, ats(assessmentNow))
	guardian := ainsert(t, store.DB(), `INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'1',?)`, guardianAccount, ats(assessmentNow))
	f.student = ainsert(t, store.DB(), `INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Student',?,'38','M',?)`, guardian, ats(assessmentNow.AddDate(-12, 0, 0)), ats(assessmentNow))
	f.coachAccount = ainsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('coach@test','h','Coach','coach',1,?)`, ats(assessmentNow))
	f.coach = ainsert(t, store.DB(), `INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'2',?)`, f.coachAccount, ats(assessmentNow))
	f.otherCoachAccount = ainsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('other@test','h','Other','coach',1,?)`, ats(assessmentNow))
	otherCoach := ainsert(t, store.DB(), `INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'3',?)`, f.otherCoachAccount, ats(assessmentNow))
	_ = otherCoach
	ainsert(t, store.DB(), `INSERT INTO coach_qualifications(coach_id,sport,level,valid_from,valid_until,status) VALUES(?,'climbing',3,?,?,'active')`, f.coach, ats(assessmentNow.Add(-time.Hour)), ats(assessmentNow.Add(24*time.Hour)))
	template := ainsert(t, store.DB(), `INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Climbing','climbing',2,8,10,5,'')`)
	f.session = ainsert(t, store.DB(), `INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'completed',10)`, template, f.coach, ats(assessmentNow.Add(-3*time.Hour)), ats(assessmentNow.Add(-time.Hour)))
	f.assessment = ainsert(t, store.DB(), `INSERT INTO assessments(student_id,session_id,examiner_id,sport,level,status,score,notes,created_at) VALUES(?,?,?,'climbing',2,'submitted',0,'',?)`, f.student, f.session, f.coach, ats(assessmentNow))
	f.service = New(store, audit.New(store))
	f.service.SetClock(func() time.Time { return assessmentNow })
	return f
}
func ainsert(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	result, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}
func ats(v time.Time) string { return v.UTC().Format(time.RFC3339Nano) }
func acode(err error) string { _, code, _ := domain.ErrorDetails(err); return code }
func ap(id int64, role domain.Role) domain.Principal {
	return domain.Principal{AccountID: id, Role: role}
}

func TestPublishPassingAssessmentGrantsCertificationAndAudit(t *testing.T) {
	f := newAssessmentFixture(t)
	result, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "assessment-request", PublishRequest{AssessmentID: f.assessment, Score: 86, Notes: " controlled movement ", ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.Score != 86 || result.Notes != "controlled movement" || result.Version != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	var certifications int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM certifications WHERE student_id=? AND sport='climbing' AND level=2`, f.student).Scan(&certifications)
	if certifications != 1 {
		t.Fatalf("certifications=%d", certifications)
	}
	var audits int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE request_id='assessment-request' AND action='assessment.published'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
}

func TestPublishFailingAssessmentDoesNotGrantCertification(t *testing.T) {
	f := newAssessmentFixture(t)
	result, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: 60, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status=%s", result.Status)
	}
	var count int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM certifications`).Scan(&count)
	if count != 0 {
		t.Fatalf("certifications=%d", count)
	}
}

func TestPublishRejectsFailedAssessmentUntilItIsResubmitted(t *testing.T) {
	f := newAssessmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE assessments SET status='failed',version=2,score=60 WHERE id=?`, f.assessment)
	_, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: 75, ExpectedVersion: 2})
	if acode(err) != "illegal_transition" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPublishRejectsUnassignedCoach(t *testing.T) {
	f := newAssessmentFixture(t)
	_, err := f.service.Publish(context.Background(), ap(f.otherCoachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: 80, ExpectedVersion: 1})
	if acode(err) != "examiner_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAssessmentUnchanged(t, f)
}

func TestPublishRejectsExpiredQualification(t *testing.T) {
	f := newAssessmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE coach_qualifications SET valid_until=? WHERE coach_id=?`, ats(assessmentNow.Add(-time.Second)), f.coach)
	_, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: 80, ExpectedVersion: 1})
	if acode(err) != "examiner_unqualified" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAssessmentUnchanged(t, f)
}

func TestPublishRejectsStaleVersionWithoutCertification(t *testing.T) {
	f := newAssessmentFixture(t)
	_, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: 80, ExpectedVersion: 9})
	if err == nil {
		t.Fatal("expected conflict")
	}
	kind, _, _ := domain.ErrorDetails(err)
	if kind != domain.KindConflict {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAssessmentUnchanged(t, f)
}

func TestPublishRejectsInvalidScoresAndRoles(t *testing.T) {
	for _, score := range []int{-1, 101} {
		f := newAssessmentFixture(t)
		_, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: score, ExpectedVersion: 1})
		if acode(err) != "invalid_assessment" {
			t.Fatalf("score %d: %v", score, err)
		}
	}
	f := newAssessmentFixture(t)
	_, err := f.service.Publish(context.Background(), ap(1, domain.RoleGuardian), "request", PublishRequest{AssessmentID: f.assessment, Score: 80, ExpectedVersion: 1})
	if acode(err) != "assessment_forbidden" {
		t.Fatalf("unexpected role error: %v", err)
	}
}

func TestPublishRejectsTerminalPassedAssessment(t *testing.T) {
	f := newAssessmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE assessments SET status='passed' WHERE id=?`, f.assessment)
	_, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "request", PublishRequest{AssessmentID: f.assessment, Score: 90, ExpectedVersion: 1})
	if acode(err) != "unchanged_state" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertAssessmentUnchanged(t *testing.T, f assessmentFixture) {
	t.Helper()
	var status string
	var version, count int
	_ = f.store.DB().QueryRow(`SELECT status,version FROM assessments WHERE id=?`, f.assessment).Scan(&status, &version)
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM certifications`).Scan(&count)
	if status != "submitted" || version != 1 || count != 0 {
		t.Fatalf("status=%s version=%d certifications=%d", status, version, count)
	}
}
