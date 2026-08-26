package equipment

import (
	"context"
	"testing"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

func TestDamagedReturnAuditFailureRollsBackCustodyLifecycle(t *testing.T) {
	f := newEquipmentFixture(t)
	loan, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout-private", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.DB().Exec(`CREATE TRIGGER reject_return_audit BEFORE INSERT ON audit_events WHEN NEW.action='equipment.returned' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	req := ReturnRequest{LoanID: loan.ID, Damaged: true, DamageCode: "shell_crack", Responsibility: "program", Notes: "impact during supervised drill"}
	if _, err = f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "return-failed", req); err == nil {
		t.Fatal("expected audit failure")
	}
	var loanStatus, assetStatus string
	var maintenance int
	_ = f.store.DB().QueryRow(`SELECT status FROM equipment_loans WHERE id=?`, loan.ID).Scan(&loanStatus)
	_ = f.store.DB().QueryRow(`SELECT status FROM equipment WHERE id=?`, f.equipment).Scan(&assetStatus)
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM maintenance_cases WHERE loan_id=?`, loan.ID).Scan(&maintenance)
	if loanStatus != "active" || assetStatus != "checked_out" || maintenance != 0 {
		t.Fatalf("failed return leaked custody state: loan=%s asset=%s maintenance=%d", loanStatus, assetStatus, maintenance)
	}
	if _, err = f.store.DB().Exec(`DROP TRIGGER reject_return_audit`); err != nil {
		t.Fatal(err)
	}
	caseRecord, err := f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "return-valid", req)
	if err != nil || caseRecord == nil {
		t.Fatalf("valid damaged return failed: case=%+v error=%v", caseRecord, err)
	}
}
