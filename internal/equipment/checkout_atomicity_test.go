package equipment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

func TestCheckoutAuditFailureDoesNotStrandEquipmentWithoutLoan(t *testing.T) {
	ctx := context.Background()
	store, err := dbstore.Open(ctx, filepath.Join(t.TempDir(), "checkout-atomicity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	insert := func(query string, args ...any) int64 {
		result, insertErr := store.DB().ExecContext(ctx, query, args...)
		if insertErr != nil {
			t.Fatalf("seed checkout: %v", insertErr)
		}
		id, insertErr := result.LastInsertId()
		if insertErr != nil {
			t.Fatalf("read inserted id: %v", insertErr)
		}
		return id
	}
	stamp := now.Format(time.RFC3339Nano)
	manager := insert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('atomic-manager@test','h','Manager','equipment_manager',1,?)`, stamp)
	guardianAccount := insert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('atomic-guardian@test','h','Guardian','guardian',1,?)`, stamp)
	guardian := insert(`INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'1',?)`, guardianAccount, stamp)
	student := insert(`INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Climber',?,'38','M',?)`, guardian, now.AddDate(-12, 0, 0).Format(time.RFC3339Nano), stamp)
	coachAccount := insert(`INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('atomic-coach@test','h','Coach','coach',1,?)`, stamp)
	coach := insert(`INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'2',?)`, coachAccount, stamp)
	template := insert(`INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Atomic climbing','climbing',1,8,10,5,'')`)
	session := insert(`INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',10)`, template, coach, now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(3*time.Hour).Format(time.RFC3339Nano))
	insert(`INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','atomic-checkout',?)`, session, student, stamp)
	equipmentID := insert(`INSERT INTO equipment(asset_tag,kind,sport,size,status,last_inspected_at) VALUES('ATOMIC-HELMET','helmet','climbing','M','available',?)`, stamp)
	insert(`INSERT INTO equipment_inspections(equipment_id,inspector_id,outcome,notes,inspected_at,valid_until) VALUES(?,?,'passed','safe',?,?)`, equipmentID, manager, stamp, now.Add(24*time.Hour).Format(time.RFC3339Nano))
	if _, err := store.DB().ExecContext(ctx, `CREATE TRIGGER reject_checkout_audit BEFORE INSERT ON audit_events WHEN NEW.request_id='checkout-audit-failure' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}

	service := New(store, audit.New(store))
	service.SetClock(func() time.Time { return now })
	principal := domain.Principal{AccountID: manager, Role: domain.RoleEquipmentManager}
	request := CheckoutRequest{EquipmentID: equipmentID, StudentID: student, SessionID: session}
	if _, err := service.Checkout(ctx, principal, "checkout-audit-failure", request); err == nil {
		t.Fatal("checkout unexpectedly succeeded while audit storage rejected the request")
	}
	var status string
	var loans, failedAudits int
	_ = store.DB().QueryRowContext(ctx, `SELECT status FROM equipment WHERE id=?`, equipmentID).Scan(&status)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM equipment_loans WHERE equipment_id=?`, equipmentID).Scan(&loans)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE request_id='checkout-audit-failure'`).Scan(&failedAudits)
	if status != "available" || loans != 0 || failedAudits != 0 {
		t.Fatalf("failed checkout stranded custody state: status=%s loans=%d audits=%d", status, loans, failedAudits)
	}

	if _, err := store.DB().ExecContext(ctx, `DROP TRIGGER reject_checkout_audit`); err != nil {
		t.Fatal(err)
	}
	loan, err := service.Checkout(ctx, principal, "checkout-after-recovery", request)
	if err != nil {
		t.Fatalf("checkout remained blocked after audit recovery: %v", err)
	}
	var recoveredStatus, loanStatus string
	var recoveredAudits int
	_ = store.DB().QueryRowContext(ctx, `SELECT status FROM equipment WHERE id=?`, equipmentID).Scan(&recoveredStatus)
	_ = store.DB().QueryRowContext(ctx, `SELECT status FROM equipment_loans WHERE id=?`, loan.ID).Scan(&loanStatus)
	_ = store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE request_id='checkout-after-recovery'`).Scan(&recoveredAudits)
	if recoveredStatus != "checked_out" || loanStatus != "active" || recoveredAudits != 1 {
		t.Fatalf("recovered checkout incomplete: equipment=%s loan=%s audits=%d", recoveredStatus, loanStatus, recoveredAudits)
	}
}
