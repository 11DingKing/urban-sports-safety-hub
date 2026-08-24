package equipment

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
type CheckoutRequest struct {
	EquipmentID int64 `json:"equipment_id"`
	StudentID   int64 `json:"student_id"`
	SessionID   int64 `json:"session_id"`
}
type ReturnRequest struct {
	LoanID         int64  `json:"loan_id"`
	Damaged        bool   `json:"damaged"`
	DamageCode     string `json:"damage_code"`
	Responsibility string `json:"responsibility"`
	Notes          string `json:"notes"`
}

func New(store *dbstore.Store, auditService *audit.Service) *Service {
	return &Service{store: store, audit: auditService, now: func() time.Time { return time.Now().UTC() }}
}
func (s *Service) SetClock(now func() time.Time) { s.now = now }
func (s *Service) Checkout(ctx context.Context, p domain.Principal, requestID string, req CheckoutRequest) (domain.EquipmentLoan, error) {
	if !domain.RoleAllowed(p.Role, domain.RoleEquipmentManager, domain.RoleAdministrator) {
		return domain.EquipmentLoan{}, domain.NewError(domain.KindForbidden, "checkout_forbidden", "equipment manager role is required")
	}
	if req.EquipmentID < 1 || req.StudentID < 1 || req.SessionID < 1 {
		return domain.EquipmentLoan{}, domain.NewError(domain.KindInvalid, "invalid_checkout", "equipment, student and session are required")
	}
	var loan domain.EquipmentLoan
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := s.store.CheckoutSnapshot(ctx, tx, req.EquipmentID, req.StudentID, req.SessionID, s.now())
		if err != nil {
			return err
		}
		if snapshot.Equipment.Status != "available" {
			return domain.NewError(domain.KindConflict, "equipment_unavailable", "equipment is not available")
		}
		if !snapshot.InspectionValid {
			return domain.NewError(domain.KindForbidden, "inspection_expired", "equipment inspection is missing or expired")
		}
		if !snapshot.Enrolled {
			return domain.NewError(domain.KindForbidden, "student_not_enrolled", "student is not enrolled in this session")
		}
		if snapshot.Equipment.Sport != string(snapshot.CourseSport) {
			return domain.NewError(domain.KindInvalid, "equipment_sport_mismatch", "equipment is not suitable for the session sport")
		}
		expectedSize := snapshot.Student.HelmetSize
		if snapshot.Equipment.Kind == "shoes" {
			expectedSize = snapshot.Student.ShoeSize
		}
		if snapshot.Equipment.Size != expectedSize {
			return domain.NewError(domain.KindForbidden, "equipment_fit_mismatch", "equipment does not fit the student")
		}
		loan, err = s.store.AcquireEquipment(ctx, tx, snapshot, p.AccountID)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, p.AccountID, requestID, "equipment", req.EquipmentID, "equipment.checked_out", "success", map[string]any{"loan_id": loan.ID, "student_id": req.StudentID})
	})
	return loan, err
}
func (s *Service) Return(ctx context.Context, p domain.Principal, requestID string, req ReturnRequest) (*domain.MaintenanceCase, error) {
	if !domain.RoleAllowed(p.Role, domain.RoleEquipmentManager, domain.RoleAdministrator) {
		return nil, domain.NewError(domain.KindForbidden, "return_forbidden", "equipment manager role is required")
	}
	if req.Damaged && (strings.TrimSpace(req.DamageCode) == "" || strings.TrimSpace(req.Responsibility) == "") {
		return nil, domain.NewError(domain.KindInvalid, "damage_details_required", "damaged returns require a damage code and responsibility")
	}
	var maintenance *domain.MaintenanceCase
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		loan, equipment, err := s.store.ActiveLoan(ctx, tx, req.LoanID)
		if err != nil {
			return err
		}
		if err := domain.ValidateTransition("loan", loan.Status, map[bool]string{true: "damaged", false: "returned"}[req.Damaged]); err != nil {
			return err
		}
		maintenance, err = s.store.CompleteReturn(ctx, tx, loan, equipment, p.AccountID, req.Damaged, req.DamageCode, req.Responsibility, req.Notes)
		if err != nil {
			return err
		}
		detail := map[string]any{"loan_id": loan.ID, "damaged": req.Damaged}
		if maintenance != nil {
			detail["maintenance_id"] = maintenance.ID
		}
		return s.audit.Record(ctx, tx, p.AccountID, requestID, "equipment", equipment.ID, "equipment.returned", "success", detail)
	})
	return maintenance, err
}
