package assessment

import (
	"context"
	"testing"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

func TestAuditFailureDoesNotPublishAssessmentOrCertification(t *testing.T) {
	f := newAssessmentFixture(t)
	_, err := f.store.DB().Exec(`CREATE TRIGGER reject_assessment_audit BEFORE INSERT ON audit_events WHEN NEW.action='assessment.published' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	request := PublishRequest{AssessmentID: f.assessment, Score: 88, Notes: "stable landing", ExpectedVersion: 1}
	if _, err = f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "publish-failed", request); err == nil {
		t.Fatal("expected audit failure")
	}
	var status string
	var version, certifications int
	_ = f.store.DB().QueryRow(`SELECT status,version FROM assessments WHERE id=?`, f.assessment).Scan(&status, &version)
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM certifications WHERE student_id=?`, f.student).Scan(&certifications)
	if status != "submitted" || version != 1 || certifications != 0 {
		t.Fatalf("failed publication leaked result: status=%s version=%d certifications=%d", status, version, certifications)
	}
	if _, err = f.store.DB().Exec(`DROP TRIGGER reject_assessment_audit`); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Publish(context.Background(), ap(f.coachAccount, domain.RoleCoach), "publish-valid", request)
	if err != nil || result.Status != "passed" {
		t.Fatalf("valid publication failed: result=%+v error=%v", result, err)
	}
}
