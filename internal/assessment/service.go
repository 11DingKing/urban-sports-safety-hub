package assessment

import (
	"context"
	"database/sql"
	"errors"
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
type PublishRequest struct {
	AssessmentID    int64  `json:"assessment_id"`
	Score           int    `json:"score"`
	Notes           string `json:"notes"`
	ExpectedVersion int    `json:"expected_version"`
}

func New(store *dbstore.Store, a *audit.Service) *Service {
	return &Service{store: store, audit: a, now: func() time.Time { return time.Now().UTC() }}
}
func (s *Service) SetClock(now func() time.Time) { s.now = now }
func (s *Service) Publish(ctx context.Context, p domain.Principal, requestID string, req PublishRequest) (domain.Assessment, error) {
	if !domain.RoleAllowed(p.Role, domain.RoleCoach, domain.RoleAdministrator) {
		return domain.Assessment{}, domain.NewError(domain.KindForbidden, "assessment_forbidden", "coach role is required")
	}
	if req.Score < 0 || req.Score > 100 || req.AssessmentID < 1 {
		return domain.Assessment{}, domain.NewError(domain.KindInvalid, "invalid_assessment", "assessment and score from 0 to 100 are required")
	}
	var result domain.Assessment
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		var created string
		var examinerAccountID int64
		err := tx.QueryRowContext(ctx, `SELECT a.id,a.student_id,a.session_id,a.examiner_id,a.sport,a.level,a.status,a.score,a.notes,a.version,a.created_at,c.account_id FROM assessments a JOIN coaches c ON c.id=a.examiner_id WHERE a.id=?`, req.AssessmentID).Scan(&result.ID, &result.StudentID, &result.SessionID, &result.ExaminerID, &result.Sport, &result.Level, &result.Status, &result.Score, &result.Notes, &result.Version, &created, &examinerAccountID)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.KindNotFound, "assessment_not_found", "assessment was not found")
		}
		if err != nil {
			return err
		}
		result.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if examinerAccountID != p.AccountID && p.Role != domain.RoleAdministrator {
			return domain.NewError(domain.KindForbidden, "examiner_mismatch", "only the assigned examiner may publish this assessment")
		}
		target := "failed"
		if req.Score >= 70 {
			target = "passed"
		}
		if err := domain.ValidateTransition("assessment", result.Status, target); err != nil {
			return err
		}
		var qualified int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM coaches c JOIN coach_qualifications q ON q.coach_id=c.id WHERE c.account_id=? AND q.sport=? AND q.level>=? AND q.status='active' AND q.valid_from<=? AND q.valid_until>=?`, examinerAccountID, result.Sport, result.Level, s.now().Format(time.RFC3339Nano), s.now().Format(time.RFC3339Nano)).Scan(&qualified)
		if err != nil {
			return err
		}
		if qualified == 0 {
			return domain.NewError(domain.KindForbidden, "examiner_unqualified", "examiner qualification is not active")
		}
		update, err := tx.ExecContext(ctx, `UPDATE assessments SET status=?,score=?,notes=?,version=version+1 WHERE id=? AND version=? AND status='submitted'`, target, req.Score, strings.TrimSpace(req.Notes), req.AssessmentID, req.ExpectedVersion)
		if err != nil {
			return err
		}
		n, _ := update.RowsAffected()
		if n != 1 {
			return domain.ErrConflict
		}
		result.Status = target
		result.Score = req.Score
		result.Notes = strings.TrimSpace(req.Notes)
		result.Version++
		if target == "passed" {
			_, err = tx.ExecContext(ctx, `INSERT INTO certifications(student_id,sport,level,granted_by,granted_at) VALUES(?,?,?,?,?) ON CONFLICT(student_id,sport,level) DO UPDATE SET granted_by=excluded.granted_by,granted_at=excluded.granted_at`, result.StudentID, result.Sport, result.Level, result.ExaminerID, s.now().Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
		}
		return s.audit.Record(ctx, tx, p.AccountID, requestID, "assessment", result.ID, "assessment.published", "success", map[string]any{"status": target, "score": req.Score})
	})
	return result, err
}
