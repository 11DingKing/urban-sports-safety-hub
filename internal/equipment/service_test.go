package equipment

import (
	"context"
	"database/sql"
	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var equipmentNow = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)

type equipmentFixture struct {
	store                                                           *dbstore.Store
	service                                                         *Service
	manager, guardian, student, coach, template, session, equipment int64
}

func newEquipmentFixture(t *testing.T) equipmentFixture {
	t.Helper()
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "equipment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetClock(func() time.Time { return equipmentNow })
	f := equipmentFixture{store: store}
	f.manager = einsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('manager@test','h','Manager','equipment_manager',1,?)`, ets(equipmentNow))
	guardianAccount := einsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('guardian@test','h','Guardian','guardian',1,?)`, ets(equipmentNow))
	f.guardian = einsert(t, store.DB(), `INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'1',?)`, guardianAccount, ets(equipmentNow))
	f.student = einsert(t, store.DB(), `INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Student',?,'38','M',?)`, f.guardian, ets(equipmentNow.AddDate(-12, 0, 0)), ets(equipmentNow))
	coachAccount := einsert(t, store.DB(), `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('coach@test','h','Coach','coach',1,?)`, ets(equipmentNow))
	f.coach = einsert(t, store.DB(), `INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'2',?)`, coachAccount, ets(equipmentNow))
	f.template = einsert(t, store.DB(), `INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Climbing','climbing',1,8,10,5,'')`)
	f.session = einsert(t, store.DB(), `INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',10)`, f.template, f.coach, ets(equipmentNow.Add(time.Hour)), ets(equipmentNow.Add(3*time.Hour)))
	einsert(t, store.DB(), `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','enrolled',?)`, f.session, f.student, ets(equipmentNow))
	f.equipment = einsert(t, store.DB(), `INSERT INTO equipment(asset_tag,kind,sport,size,status,last_inspected_at) VALUES('HELMET-1','helmet','climbing','M','available',?)`, ets(equipmentNow))
	einsert(t, store.DB(), `INSERT INTO equipment_inspections(equipment_id,inspector_id,outcome,notes,inspected_at,valid_until) VALUES(?,?,'passed','safe',?,?)`, f.equipment, f.manager, ets(equipmentNow), ets(equipmentNow.Add(24*time.Hour)))
	f.service = New(store, audit.New(store))
	f.service.SetClock(func() time.Time { return equipmentNow })
	return f
}
func einsert(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	result, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}
func ets(v time.Time) string { return v.UTC().Format(time.RFC3339Nano) }
func ep(id int64, role domain.Role) domain.Principal {
	return domain.Principal{AccountID: id, Role: role, SessionID: 1, ExpiresAt: equipmentNow.Add(time.Hour)}
}
func ecode(err error) string { _, code, _ := domain.ErrorDetails(err); return code }

func TestCheckoutAcquiresFittedInspectedEquipmentAndAudits(t *testing.T) {
	f := newEquipmentFixture(t)
	loan, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout-request", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	if err != nil {
		t.Fatal(err)
	}
	if loan.Status != "active" || loan.StudentID != f.student || loan.EquipmentID != f.equipment {
		t.Fatalf("unexpected loan: %+v", loan)
	}
	var status string
	_ = f.store.DB().QueryRow(`SELECT status FROM equipment WHERE id=?`, f.equipment).Scan(&status)
	if status != "checked_out" {
		t.Fatalf("status=%s", status)
	}
	var auditCount int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='equipment.checked_out' AND request_id='checkout-request'`).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("audits=%d", auditCount)
	}
}

func TestCheckoutRequiresEquipmentManagerOrAdministrator(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleGuardian, domain.RoleCoach} {
		t.Run(string(role), func(t *testing.T) {
			f := newEquipmentFixture(t)
			_, err := f.service.Checkout(context.Background(), ep(100, role), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
			if ecode(err) != "checkout_forbidden" {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	f := newEquipmentFixture(t)
	if _, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleAdministrator), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session}); err != nil {
		t.Fatalf("administrator checkout: %v", err)
	}
}

func TestCheckoutRejectsInvalidIdentifiers(t *testing.T) {
	f := newEquipmentFixture(t)
	cases := []CheckoutRequest{{StudentID: f.student, SessionID: f.session}, {EquipmentID: f.equipment, SessionID: f.session}, {EquipmentID: f.equipment, StudentID: f.student}}
	for _, req := range cases {
		_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", req)
		if ecode(err) != "invalid_checkout" {
			t.Fatalf("request %+v: %v", req, err)
		}
	}
}

func TestCheckoutRejectsUnenrolledStudent(t *testing.T) {
	f := newEquipmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE enrollments SET status='canceled' WHERE student_id=?`, f.student)
	_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	if ecode(err) != "student_not_enrolled" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAssetAvailable(t, f.store.DB(), f.equipment)
}

func TestCheckoutRejectsExpiredOrFailedInspection(t *testing.T) {
	cases := []struct {
		name, outcome string
		valid         time.Time
	}{{"expired", "passed", equipmentNow.Add(-time.Second)}, {"failed", "failed", equipmentNow.Add(time.Hour)}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEquipmentFixture(t)
			_, _ = f.store.DB().Exec(`UPDATE equipment_inspections SET outcome=?,valid_until=? WHERE equipment_id=?`, tc.outcome, ets(tc.valid), f.equipment)
			_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
			if ecode(err) != "inspection_expired" {
				t.Fatalf("unexpected error: %v", err)
			}
			assertAssetAvailable(t, f.store.DB(), f.equipment)
		})
	}
}

func TestCheckoutRejectsSportMismatch(t *testing.T) {
	f := newEquipmentFixture(t)
	_, _ = f.store.DB().Exec(`UPDATE equipment SET sport='skateboarding' WHERE id=?`, f.equipment)
	_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	if ecode(err) != "equipment_sport_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAssetAvailable(t, f.store.DB(), f.equipment)
}

func TestCheckoutRejectsHelmetAndShoeFitMismatch(t *testing.T) {
	t.Run("helmet", func(t *testing.T) {
		f := newEquipmentFixture(t)
		_, _ = f.store.DB().Exec(`UPDATE equipment SET size='L' WHERE id=?`, f.equipment)
		_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
		if ecode(err) != "equipment_fit_mismatch" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("shoes", func(t *testing.T) {
		f := newEquipmentFixture(t)
		_, _ = f.store.DB().Exec(`UPDATE equipment SET kind='shoes',size='39' WHERE id=?`, f.equipment)
		_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
		if ecode(err) != "equipment_fit_mismatch" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCheckoutRejectsIsolatedOrAlreadyCheckedOutAsset(t *testing.T) {
	for _, status := range []string{"isolated", "maintenance", "retired", "checked_out"} {
		t.Run(status, func(t *testing.T) {
			f := newEquipmentFixture(t)
			_, _ = f.store.DB().Exec(`UPDATE equipment SET status=? WHERE id=?`, status, f.equipment)
			_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "req", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
			if ecode(err) != "equipment_unavailable" {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConcurrentCheckoutProducesExactlyOneCustodian(t *testing.T) {
	f := newEquipmentFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "concurrent", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, failed := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			failed++
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("success=%d failed=%d", success, failed)
	}
	var loans int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM equipment_loans WHERE equipment_id=? AND status='active'`, f.equipment).Scan(&loans)
	if loans != 1 {
		t.Fatalf("active loans=%d", loans)
	}
}

func TestCleanReturnReleasesEquipmentAndClosesCustody(t *testing.T) {
	f := newEquipmentFixture(t)
	loan, err := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "return", ReturnRequest{LoanID: loan.ID})
	if err != nil {
		t.Fatal(err)
	}
	if maintenance != nil {
		t.Fatalf("unexpected maintenance: %+v", maintenance)
	}
	var loanStatus, assetStatus string
	_ = f.store.DB().QueryRow(`SELECT status FROM equipment_loans WHERE id=?`, loan.ID).Scan(&loanStatus)
	_ = f.store.DB().QueryRow(`SELECT status FROM equipment WHERE id=?`, f.equipment).Scan(&assetStatus)
	if loanStatus != "returned" || assetStatus != "available" {
		t.Fatalf("loan=%s asset=%s", loanStatus, assetStatus)
	}
}

func TestDamagedReturnIsolatesAssetAndRecordsResponsibility(t *testing.T) {
	f := newEquipmentFixture(t)
	loan, _ := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	maintenance, err := f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "damaged-return", ReturnRequest{LoanID: loan.ID, Damaged: true, DamageCode: "strap_cut", Responsibility: "student", Notes: "cut during fall"})
	if err != nil {
		t.Fatal(err)
	}
	if maintenance == nil || maintenance.DamageCode != "strap_cut" || maintenance.Responsibility != "student" {
		t.Fatalf("unexpected maintenance: %+v", maintenance)
	}
	var assetStatus string
	_ = f.store.DB().QueryRow(`SELECT status FROM equipment WHERE id=?`, f.equipment).Scan(&assetStatus)
	if assetStatus != "isolated" {
		t.Fatalf("asset status=%s", assetStatus)
	}
	var auditCount int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE request_id='damaged-return' AND action='equipment.returned'`).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("audits=%d", auditCount)
	}
}

func TestDamagedReturnRequiresClassificationBeforeMutation(t *testing.T) {
	cases := []ReturnRequest{{Damaged: true, DamageCode: "", Responsibility: "student"}, {Damaged: true, DamageCode: "crack", Responsibility: ""}}
	for _, req := range cases {
		f := newEquipmentFixture(t)
		loan, _ := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
		req.LoanID = loan.ID
		_, err := f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "return", req)
		if ecode(err) != "damage_details_required" {
			t.Fatalf("unexpected error: %v", err)
		}
		var status string
		_ = f.store.DB().QueryRow(`SELECT status FROM equipment_loans WHERE id=?`, loan.ID).Scan(&status)
		if status != "active" {
			t.Fatalf("loan changed to %s", status)
		}
	}
}

func TestReturnRequiresEquipmentRole(t *testing.T) {
	f := newEquipmentFixture(t)
	loan, _ := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	_, err := f.service.Return(context.Background(), ep(100, domain.RoleGuardian), "return", ReturnRequest{LoanID: loan.ID})
	if ecode(err) != "return_forbidden" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecondReturnIsRejectedAndDoesNotDuplicateMaintenance(t *testing.T) {
	f := newEquipmentFixture(t)
	loan, _ := f.service.Checkout(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "checkout", CheckoutRequest{EquipmentID: f.equipment, StudentID: f.student, SessionID: f.session})
	req := ReturnRequest{LoanID: loan.ID, Damaged: true, DamageCode: "crack", Responsibility: "program"}
	if _, err := f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "return-1", req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Return(context.Background(), ep(f.manager, domain.RoleEquipmentManager), "return-2", req); ecode(err) != "unchanged_state" {
		t.Fatalf("unexpected second return error: %v", err)
	}
	var count int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM maintenance_cases WHERE loan_id=?`, loan.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("maintenance count=%d", count)
	}
}

func assertAssetAvailable(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	var status string
	var loans int
	_ = db.QueryRow(`SELECT status FROM equipment WHERE id=?`, id).Scan(&status)
	_ = db.QueryRow(`SELECT COUNT(*) FROM equipment_loans WHERE equipment_id=?`, id).Scan(&loans)
	if status != "available" || loans != 0 {
		t.Fatalf("asset=%s loans=%d", status, loans)
	}
}
