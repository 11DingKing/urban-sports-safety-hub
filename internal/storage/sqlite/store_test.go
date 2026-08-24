package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

var fixedNow = time.Date(2026, time.August, 24, 8, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "sports.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.SetClock(func() time.Time { return fixedNow })
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func execSQL(t *testing.T, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) int64 {
	t.Helper()
	result, err := exec.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

type fixture struct{ guardianAccount, guardian, student, coachAccount, coach, managerAccount, template, session, equipment int64 }

func seedFixture(t *testing.T, store *Store) fixture {
	t.Helper()
	f := fixture{}
	f.guardianAccount = execSQL(t, store.db, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('guardian@test','hash','Guardian','guardian',1,?)`, timeText(fixedNow))
	f.guardian = execSQL(t, store.db, `INSERT INTO guardians(account_id,phone,created_at) VALUES(?,'10086',?)`, f.guardianAccount, timeText(fixedNow))
	f.student = execSQL(t, store.db, `INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(?,'Lin',?,'38','M',?)`, f.guardian, timeText(fixedNow.AddDate(-13, 0, 0)), timeText(fixedNow))
	f.coachAccount = execSQL(t, store.db, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('coach@test','hash','Coach','coach',1,?)`, timeText(fixedNow))
	f.coach = execSQL(t, store.db, `INSERT INTO coaches(account_id,emergency_phone,created_at) VALUES(?,'10010',?)`, f.coachAccount, timeText(fixedNow))
	f.managerAccount = execSQL(t, store.db, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('manager@test','hash','Manager','equipment_manager',1,?)`, timeText(fixedNow))
	f.template = execSQL(t, store.db, `INSERT INTO course_templates(name,sport,level,minimum_age,capacity,coach_ratio,required_certification) VALUES('Climbing L1','climbing',1,8,2,2,'')`)
	f.session = execSQL(t, store.db, `INSERT INTO course_sessions(template_id,coach_id,starts_at,ends_at,status,capacity) VALUES(?,?,?,?, 'scheduled',2)`, f.template, f.coach, timeText(fixedNow.Add(24*time.Hour)), timeText(fixedNow.Add(26*time.Hour)))
	execSQL(t, store.db, `INSERT INTO coach_qualifications(coach_id,sport,level,valid_from,valid_until,status) VALUES(?,'climbing',2,?,?,'active')`, f.coach, timeText(fixedNow.Add(-24*time.Hour)), timeText(fixedNow.AddDate(0, 1, 0)))
	execSQL(t, store.db, `INSERT INTO guardian_consents(student_id,guardian_id,scope,granted_at,expires_at) VALUES(?,?,'sports_participation',?,?)`, f.student, f.guardian, timeText(fixedNow.Add(-time.Hour)), timeText(fixedNow.AddDate(0, 1, 0)))
	f.equipment = execSQL(t, store.db, `INSERT INTO equipment(asset_tag,kind,sport,size,status,last_inspected_at) VALUES('CLIMB-001','helmet','climbing','M','available',?)`, timeText(fixedNow))
	execSQL(t, store.db, `INSERT INTO equipment_inspections(equipment_id,inspector_id,outcome,notes,inspected_at,valid_until) VALUES(?,?,'passed','ready',?,?)`, f.equipment, f.managerAccount, timeText(fixedNow), timeText(fixedNow.Add(7*24*time.Hour)))
	return f
}

func TestMigrationsCreateRelationalSchemaAndAreRepeatable(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	expected := []string{"accounts", "assessments", "audit_events", "certifications", "coach_qualifications", "coaches", "course_prerequisites", "course_sessions", "course_templates", "enrollments", "equipment", "equipment_inspections", "equipment_loans", "group_members", "guardian_consents", "guardians", "idempotency_keys", "maintenance_cases", "makeup_entitlements", "schema_migrations", "sessions", "students", "training_groups", "worker_jobs"}
	sort.Strings(expected)
	if fmt.Sprint(names) != fmt.Sprint(expected) {
		t.Fatalf("schema mismatch\ngot  %v\nwant %v", names, expected)
	}
	if err := migrate(ctx, store.db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var versions int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 3 {
		t.Fatalf("migration versions=%d want 3", versions)
	}
}

func TestForeignKeysRejectOrphanedBusinessRecords(t *testing.T) {
	store := openTestStore(t)
	_, err := store.db.Exec(`INSERT INTO students(guardian_id,name,birth_date,shoe_size,helmet_size,created_at) VALUES(999,'orphan',?,?,?,?)`, timeText(fixedNow), "1", "S", timeText(fixedNow))
	if err == nil {
		t.Fatal("expected foreign key rejection")
	}
	_, err = store.db.Exec(`INSERT INTO equipment_loans(equipment_id,student_id,session_id,issued_by,status,checked_out_at) VALUES(999,999,999,999,'active',?)`, timeText(fixedNow))
	if err == nil {
		t.Fatal("expected loan foreign key rejection")
	}
}

func TestAccountLifecycleNormalizesEmailAndRejectsDuplicates(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	created, err := store.CreateAccount(ctx, domain.Account{Email: "  GUARDIAN@Example.Test ", PasswordHash: "secret-hash", DisplayName: "Gao", Role: domain.RoleGuardian, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID < 1 || created.Email != "guardian@example.test" || !created.CreatedAt.Equal(fixedNow) {
		t.Fatalf("unexpected account: %+v", created)
	}
	found, err := store.AccountByEmail(ctx, "Guardian@Example.Test")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != created.ID || found.Role != domain.RoleGuardian || !found.Active {
		t.Fatalf("unexpected found account: %+v", found)
	}
	_, err = store.CreateAccount(ctx, domain.Account{Email: "guardian@example.test", PasswordHash: "other", DisplayName: "Other", Role: domain.RoleCoach, Active: true})
	if err == nil {
		t.Fatal("expected duplicate email conflict")
	}
	kind, code, _ := domain.ErrorDetails(err)
	if kind != domain.KindConflict || code != "email_exists" {
		t.Fatalf("unexpected duplicate error: %v", err)
	}
}

func TestSessionResolutionRevocationAndExpiration(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	account, err := store.CreateAccount(ctx, domain.Account{Email: "coach@example.test", PasswordHash: "hash", DisplayName: "Coach", Role: domain.RoleCoach, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateSession(ctx, account.ID, "token-hash", fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.ResolveSession(ctx, "token-hash", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if principal.AccountID != account.ID || principal.SessionID != id || principal.Role != domain.RoleCoach {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if _, err := store.ResolveSession(ctx, "token-hash", fixedNow.Add(2*time.Hour)); err == nil {
		t.Fatal("expected expiry")
	}
	if err := store.RevokeSession(ctx, "token-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSession(ctx, "token-hash", fixedNow); err == nil {
		t.Fatal("expected revoked session rejection")
	}
	if err := store.RevokeSession(ctx, "token-hash"); err == nil {
		t.Fatal("second revocation should reject inactive session")
	}
}

func TestDisabledAccountInvalidatesExistingSession(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	account, _ := store.CreateAccount(ctx, domain.Account{Email: "disabled@example.test", PasswordHash: "hash", DisplayName: "Disabled", Role: domain.RoleCoach, Active: true})
	_, _ = store.CreateSession(ctx, account.ID, "disabled-token", fixedNow.Add(time.Hour))
	if _, err := store.db.ExecContext(ctx, `UPDATE accounts SET active=0 WHERE id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	_, err := store.ResolveSession(ctx, "disabled-token", fixedNow)
	if err == nil {
		t.Fatal("expected disabled account rejection")
	}
	kind, code, _ := domain.ErrorDetails(err)
	if kind != domain.KindForbidden || code != "account_disabled" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExpiredSessionCleanupRetainsActiveSessions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	account, _ := store.CreateAccount(ctx, domain.Account{Email: "cleanup@example.test", PasswordHash: "hash", DisplayName: "Cleanup", Role: domain.RoleGuardian, Active: true})
	_, _ = store.CreateSession(ctx, account.ID, "expired", fixedNow.Add(-time.Minute))
	_, _ = store.CreateSession(ctx, account.ID, "active", fixedNow.Add(time.Hour))
	count, err := store.DeleteExpiredSessions(ctx, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("deleted=%d want 1", count)
	}
	if _, err := store.ResolveSession(ctx, "active", fixedNow); err != nil {
		t.Fatalf("active session deleted: %v", err)
	}
}

func TestInTxCommitsAllWritesTogether(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('a@test','h','A','guardian',1,?)`, timeText(fixedNow)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('b@test','h','B','coach',1,?)`, timeText(fixedNow)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count=%d want 2", count)
	}
}

func TestInTxRollsBackAllWritesAfterFailure(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	sentinel := errors.New("audit storage failed")
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES('rollback@test','h','Rollback','guardian',1,?)`, timeText(fixedNow)); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v want sentinel", err)
	}
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE email='rollback@test'`).Scan(&count)
	if count != 0 {
		t.Fatalf("rolled back account remains: %d", count)
	}
}

func TestEnrollmentSnapshotCombinesConsentPrerequisiteAndCoachValidity(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	ctx := context.Background()
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.EnrollmentSnapshot(ctx, tx, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		if !snapshot.ConsentValid || !snapshot.PrerequisitesMet || !snapshot.CoachQualified {
			t.Fatalf("unexpected eligibility snapshot: %+v", snapshot)
		}
		if snapshot.Template.Sport != domain.SportClimbing || snapshot.Session.Capacity != 2 {
			t.Fatalf("unexpected joined records: %+v", snapshot)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentSnapshotDetectsExpiredConsent(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	if _, err := store.db.Exec(`UPDATE guardian_consents SET expires_at=? WHERE student_id=?`, timeText(fixedNow.Add(time.Hour)), f.student); err != nil {
		t.Fatal(err)
	}
	err := store.InTx(context.Background(), func(tx *sql.Tx) error {
		snapshot, err := store.EnrollmentSnapshot(context.Background(), tx, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		if snapshot.ConsentValid {
			t.Fatal("consent should not cover full session")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentSnapshotDetectsMissingPrerequisite(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	execSQL(t, store.db, `INSERT INTO course_prerequisites(template_id,required_sport,required_level) VALUES(?,'climbing',1)`, f.template)
	err := store.InTx(context.Background(), func(tx *sql.Tx) error {
		snapshot, err := store.EnrollmentSnapshot(context.Background(), tx, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		if snapshot.PrerequisitesMet {
			t.Fatal("missing certification was not detected")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReserveEnrollmentAtomicallyConsumesCapacity(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	ctx := context.Background()
	var enrolled domain.Enrollment
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.EnrollmentSnapshot(ctx, tx, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		enrolled, err = store.ReserveEnrollment(ctx, tx, f.student, snapshot, "reserve-1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.Status != "confirmed" || enrolled.Version != 1 {
		t.Fatalf("unexpected enrollment: %+v", enrolled)
	}
	session, err := store.SessionByID(ctx, f.session)
	if err != nil {
		t.Fatal(err)
	}
	if session.Enrolled != 1 || session.Version != 2 {
		t.Fatalf("capacity/version not updated: %+v", session)
	}
}

func TestReserveEnrollmentRejectsStaleSnapshotWithoutPartialInsert(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	ctx := context.Background()
	var snapshot EnrollmentSnapshot
	if err := store.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		snapshot, err = store.EnrollmentSnapshot(ctx, tx, f.student, f.session, fixedNow)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE course_sessions SET version=version+1 WHERE id=?`, f.session); err != nil {
		t.Fatal(err)
	}
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		_, err := store.ReserveEnrollment(ctx, tx, f.student, snapshot, "stale-key")
		return err
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("got %v want conflict", err)
	}
	var count int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM enrollments WHERE idempotency_key='stale-key'`).Scan(&count)
	if count != 0 {
		t.Fatalf("partial enrollment inserted: %d", count)
	}
}

func TestCancelSessionCreatesOneMakeupPerConfirmedEnrollment(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	ctx := context.Background()
	var enrollmentID int64
	if err := store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.EnrollmentSnapshot(ctx, tx, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		enrollment, err := store.ReserveEnrollment(ctx, tx, f.student, snapshot, "cancel-key")
		enrollmentID = enrollment.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	count, err := func() (int64, error) {
		var result int64
		err := store.InTx(ctx, func(tx *sql.Tx) error {
			var err error
			result, err = store.CancelSessionRows(ctx, tx, f.session, "storm", 2, fixedNow.AddDate(0, 2, 0))
			return err
		})
		return result, err
	}()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("makeups=%d want 1", count)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM enrollments WHERE id=?`, enrollmentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "makeup_due" {
		t.Fatalf("enrollment status=%q", status)
	}
}

func TestAuditEventsRetainActorRequestObjectAndDetails(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	ctx := context.Background()
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		actor := f.managerAccount
		_, err := store.AppendAudit(ctx, tx, domain.AuditEvent{ActorID: &actor, RequestID: "request-77", ObjectType: "equipment", ObjectID: f.equipment, Action: "equipment.checked", Result: "success", Detail: `{"outcome":"passed"}`})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ListAudit(ctx, "equipment", f.equipment, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ActorID == nil || *events[0].ActorID != f.managerAccount || events[0].RequestID != "request-77" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[0].Detail != `{"outcome":"passed"}` || !events[0].CreatedAt.Equal(fixedNow) {
		t.Fatalf("event lost detail/time: %+v", events[0])
	}
}

func TestAuditRejectsIncompleteTraceability(t *testing.T) {
	store := openTestStore(t)
	err := store.InTx(context.Background(), func(tx *sql.Tx) error {
		_, err := store.AppendAudit(context.Background(), tx, domain.AuditEvent{ObjectType: "equipment", ObjectID: 1, Action: "changed"})
		return err
	})
	if err == nil {
		t.Fatal("expected audit validation error")
	}
	_, code, _ := domain.ErrorDetails(err)
	if code != "invalid_audit" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckoutSnapshotRequiresEnrollmentAndFreshInspection(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	ctx := context.Background()
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.CheckoutSnapshot(ctx, tx, f.equipment, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		if snapshot.Enrolled {
			t.Fatal("student should not yet be enrolled")
		}
		if !snapshot.InspectionValid {
			t.Fatal("fresh passing inspection should be valid")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, store.db, `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','checkout-enrollment',?)`, f.session, f.student, timeText(fixedNow))
	if _, err := store.db.Exec(`UPDATE equipment_inspections SET valid_until=? WHERE equipment_id=?`, timeText(fixedNow.Add(-time.Second)), f.equipment); err != nil {
		t.Fatal(err)
	}
	err = store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.CheckoutSnapshot(ctx, tx, f.equipment, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		if !snapshot.Enrolled || snapshot.InspectionValid {
			t.Fatalf("unexpected snapshot: %+v", snapshot)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAcquireEquipmentCreatesExclusiveLoanAndChangesAssetState(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	execSQL(t, store.db, `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','loan-enrollment',?)`, f.session, f.student, timeText(fixedNow))
	ctx := context.Background()
	var loan domain.EquipmentLoan
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.CheckoutSnapshot(ctx, tx, f.equipment, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		loan, err = store.AcquireEquipment(ctx, tx, snapshot, f.managerAccount)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if loan.Status != "active" || loan.EquipmentID != f.equipment || !loan.CheckedOutAt.Equal(fixedNow) {
		t.Fatalf("unexpected loan: %+v", loan)
	}
	var status string
	var version int
	if err := store.db.QueryRow(`SELECT status,version FROM equipment WHERE id=?`, f.equipment).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "checked_out" || version != 2 {
		t.Fatalf("asset state=%s version=%d", status, version)
	}
}

func TestConcurrentEquipmentAcquisitionHasSingleWinner(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	execSQL(t, store.db, `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','concurrent-enrollment',?)`, f.session, f.student, timeText(fixedNow))
	ctx := context.Background()
	var snapshot CheckoutSnapshot
	if err := store.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		snapshot, err = store.CheckoutSnapshot(ctx, tx, f.equipment, f.student, f.session, fixedNow)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- store.InTx(ctx, func(tx *sql.Tx) error {
				_, err := store.AcquireEquipment(ctx, tx, snapshot, f.managerAccount)
				return err
			})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, failures := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes=%d failures=%d", successes, failures)
	}
	var loans int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM equipment_loans WHERE equipment_id=? AND status='active'`, f.equipment).Scan(&loans)
	if loans != 1 {
		t.Fatalf("active loans=%d", loans)
	}
}

func TestDamagedReturnClosesLoanAndOpensIsolationCaseAtomically(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	execSQL(t, store.db, `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','return-enrollment',?)`, f.session, f.student, timeText(fixedNow))
	ctx := context.Background()
	var loan domain.EquipmentLoan
	if err := store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.CheckoutSnapshot(ctx, tx, f.equipment, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		loan, err = store.AcquireEquipment(ctx, tx, snapshot, f.managerAccount)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var maintenance *domain.MaintenanceCase
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		active, equipment, err := store.ActiveLoan(ctx, tx, loan.ID)
		if err != nil {
			return err
		}
		maintenance, err = store.CompleteReturn(ctx, tx, active, equipment, f.managerAccount, true, "shell_crack", "program", "impact on return")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if maintenance == nil || maintenance.Status != "open" || maintenance.DamageCode != "shell_crack" {
		t.Fatalf("unexpected maintenance: %+v", maintenance)
	}
	var loanStatus, equipmentStatus string
	_ = store.db.QueryRow(`SELECT status FROM equipment_loans WHERE id=?`, loan.ID).Scan(&loanStatus)
	_ = store.db.QueryRow(`SELECT status FROM equipment WHERE id=?`, f.equipment).Scan(&equipmentStatus)
	if loanStatus != "damaged" || equipmentStatus != "isolated" {
		t.Fatalf("loan=%s equipment=%s", loanStatus, equipmentStatus)
	}
}

func TestCleanReturnMakesEquipmentAvailableWithoutMaintenance(t *testing.T) {
	store := openTestStore(t)
	f := seedFixture(t, store)
	execSQL(t, store.db, `INSERT INTO enrollments(session_id,student_id,status,idempotency_key,created_at) VALUES(?,?,'confirmed','clean-enrollment',?)`, f.session, f.student, timeText(fixedNow))
	ctx := context.Background()
	var loan domain.EquipmentLoan
	_ = store.InTx(ctx, func(tx *sql.Tx) error {
		snapshot, err := store.CheckoutSnapshot(ctx, tx, f.equipment, f.student, f.session, fixedNow)
		if err != nil {
			return err
		}
		loan, err = store.AcquireEquipment(ctx, tx, snapshot, f.managerAccount)
		return err
	})
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		active, equipment, err := store.ActiveLoan(ctx, tx, loan.ID)
		if err != nil {
			return err
		}
		maintenance, err := store.CompleteReturn(ctx, tx, active, equipment, f.managerAccount, false, "", "", "")
		if maintenance != nil {
			t.Fatal("clean return created maintenance")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	_ = store.db.QueryRow(`SELECT status FROM equipment WHERE id=?`, f.equipment).Scan(&status)
	if status != "available" {
		t.Fatalf("status=%s", status)
	}
}

func TestJobsPersistClaimRetryAndSucceed(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	job, err := store.EnqueueJob(ctx, "expire_sessions", "daily-expiry", `{"before":"2026-08-24T08:00:00Z"}`, 3, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "pending" || job.Attempts != 0 {
		t.Fatalf("unexpected job: %+v", job)
	}
	claimed, err := store.ClaimJobs(ctx, 5, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != job.ID || claimed[0].Attempts != 1 || claimed[0].Status != "running" {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	if err := store.FinishJob(ctx, job.ID, errors.New("database busy"), fixedNow); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimJobs(ctx, 5, fixedNow.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("unexpected retry claim: %+v", claimed)
	}
	if err := store.FinishJob(ctx, job.ID, nil, fixedNow.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimJobs(ctx, 5, fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("completed job claimed again: %+v", claimed)
	}
}

func TestJobBecomesPermanentlyFailedAtAttemptLimit(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	job, _ := store.EnqueueJob(ctx, "unknown", "limited", "{}", 1, fixedNow)
	claimed, _ := store.ClaimJobs(ctx, 1, fixedNow)
	if len(claimed) != 1 {
		t.Fatal("job not claimed")
	}
	if err := store.FinishJob(ctx, job.ID, errors.New("permanent"), fixedNow); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	if err := store.db.QueryRow(`SELECT status,last_error FROM worker_jobs WHERE id=?`, job.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError != "permanent" {
		t.Fatalf("status=%s error=%q", status, lastError)
	}
}

func TestRecoverJobsMakesOnlyStaleClaimsRetryable(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	old, _ := store.EnqueueJob(ctx, "expire_sessions", "old", "{}", 3, fixedNow)
	fresh, _ := store.EnqueueJob(ctx, "expire_sessions", "fresh", "{}", 3, fixedNow)
	_, _ = store.ClaimJobs(ctx, 2, fixedNow)
	_, _ = store.db.Exec(`UPDATE worker_jobs SET locked_at=? WHERE id=?`, timeText(fixedNow.Add(-10*time.Minute)), old.ID)
	_, _ = store.db.Exec(`UPDATE worker_jobs SET locked_at=? WHERE id=?`, timeText(fixedNow.Add(-time.Minute)), fresh.ID)
	count, err := store.RecoverJobs(ctx, fixedNow.Add(-5*time.Minute), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("recovered=%d want 1", count)
	}
	var oldStatus, freshStatus string
	_ = store.db.QueryRow(`SELECT status FROM worker_jobs WHERE id=?`, old.ID).Scan(&oldStatus)
	_ = store.db.QueryRow(`SELECT status FROM worker_jobs WHERE id=?`, fresh.ID).Scan(&freshStatus)
	if oldStatus != "retry" || freshStatus != "running" {
		t.Fatalf("old=%s fresh=%s", oldStatus, freshStatus)
	}
}

func TestDatabaseStateSurvivesCloseAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	ctx := context.Background()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, domain.Account{Email: "restart@test", PasswordHash: "hash", DisplayName: "Restart", Role: domain.RoleGuardian, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	found, err := reopened.AccountByID(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Email != "restart@test" || found.Role != domain.RoleGuardian {
		t.Fatalf("recovered account mismatch: %+v", found)
	}
}
