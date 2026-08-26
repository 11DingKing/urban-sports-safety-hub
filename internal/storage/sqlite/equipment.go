package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type CheckoutSnapshot struct {
	Equipment                 domain.Equipment
	Student                   domain.Student
	Session                   domain.CourseSession
	CourseSport               domain.Sport
	Enrolled, InspectionValid bool
}

func (s *Store) CheckoutSnapshot(ctx context.Context, tx *sql.Tx, equipmentID, studentID, sessionID int64, now time.Time) (CheckoutSnapshot, error) {
	var out CheckoutSnapshot
	var inspected sql.NullString
	var birth, created, start, end string
	err := tx.QueryRowContext(ctx, `SELECT e.id,e.asset_tag,e.kind,e.sport,e.size,e.status,e.version,e.last_inspected_at,st.id,st.guardian_id,st.name,st.birth_date,st.shoe_size,st.helmet_size,st.version,st.created_at,cs.id,cs.template_id,cs.coach_id,cs.starts_at,cs.ends_at,cs.status,cs.capacity,cs.enrolled,cs.version,cs.cancel_reason,ct.sport FROM equipment e CROSS JOIN students st CROSS JOIN course_sessions cs JOIN course_templates ct ON ct.id=cs.template_id WHERE e.id=? AND st.id=? AND cs.id=?`, equipmentID, studentID, sessionID).Scan(&out.Equipment.ID, &out.Equipment.AssetTag, &out.Equipment.Kind, &out.Equipment.Sport, &out.Equipment.Size, &out.Equipment.Status, &out.Equipment.Version, &inspected, &out.Student.ID, &out.Student.GuardianID, &out.Student.Name, &birth, &out.Student.ShoeSize, &out.Student.HelmetSize, &out.Student.Version, &created, &out.Session.ID, &out.Session.TemplateID, &out.Session.CoachID, &start, &end, &out.Session.Status, &out.Session.Capacity, &out.Session.Enrolled, &out.Session.Version, &out.Session.CancelReason, &out.CourseSport)
	if errors.Is(err, sql.ErrNoRows) {
		return out, domain.NewError(domain.KindNotFound, "checkout_target_not_found", "equipment, student or course session was not found")
	}
	if err != nil {
		return out, err
	}
	out.Equipment.LastInspectedAt, _ = nullableTime(inspected)
	out.Student.BirthDate, _ = parseTime(birth)
	out.Student.CreatedAt, _ = parseTime(created)
	out.Session.StartsAt, _ = parseTime(start)
	out.Session.EndsAt, _ = parseTime(end)
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrollments WHERE student_id=? AND session_id=? AND status='confirmed'`, studentID, sessionID).Scan(&count); err != nil {
		return out, err
	}
	out.Enrolled = count == 1
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM equipment_inspections WHERE equipment_id=? AND outcome='passed' AND valid_until>?`, equipmentID, timeText(now)).Scan(&count); err != nil {
		return out, err
	}
	out.InspectionValid = count > 0
	return out, nil
}

func (s *Store) AcquireEquipment(ctx context.Context, tx *sql.Tx, snapshot CheckoutSnapshot, actorID int64) (domain.EquipmentLoan, error) {
	result, err := tx.ExecContext(ctx, `UPDATE equipment SET status='checked_out',version=version+1 WHERE id=? AND version=? AND status='available'`, snapshot.Equipment.ID, snapshot.Equipment.Version)
	if err != nil {
		return domain.EquipmentLoan{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return domain.EquipmentLoan{}, domain.ErrConflict
	}
	now := s.now()
	result, err = tx.ExecContext(ctx, `INSERT INTO equipment_loans(equipment_id,student_id,session_id,issued_by,status,checked_out_at) VALUES(?,?,?,?, 'active',?)`, snapshot.Equipment.ID, snapshot.Student.ID, snapshot.Session.ID, actorID, timeText(now))
	if err != nil {
		return domain.EquipmentLoan{}, fmt.Errorf("insert loan: %w", err)
	}
	id, err := result.LastInsertId()
	return domain.EquipmentLoan{ID: id, EquipmentID: snapshot.Equipment.ID, StudentID: snapshot.Student.ID, SessionID: snapshot.Session.ID, IssuedBy: actorID, Status: "active", CheckedOutAt: now, Version: 1}, err
}

func (s *Store) ActiveLoan(ctx context.Context, tx *sql.Tx, loanID int64) (domain.EquipmentLoan, domain.Equipment, error) {
	var loan domain.EquipmentLoan
	var equipment domain.Equipment
	var checked string
	var returned sql.NullString
	var inspected sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT l.id,l.equipment_id,l.student_id,l.session_id,l.issued_by,l.status,l.checked_out_at,l.returned_at,l.version,e.id,e.asset_tag,e.kind,e.sport,e.size,e.status,e.version,e.last_inspected_at FROM equipment_loans l JOIN equipment e ON e.id=l.equipment_id WHERE l.id=?`, loanID).Scan(&loan.ID, &loan.EquipmentID, &loan.StudentID, &loan.SessionID, &loan.IssuedBy, &loan.Status, &checked, &returned, &loan.Version, &equipment.ID, &equipment.AssetTag, &equipment.Kind, &equipment.Sport, &equipment.Size, &equipment.Status, &equipment.Version, &inspected)
	if errors.Is(err, sql.ErrNoRows) {
		return loan, equipment, domain.NewError(domain.KindNotFound, "loan_not_found", "equipment loan was not found")
	}
	loan.CheckedOutAt, _ = parseTime(checked)
	loan.ReturnedAt, _ = nullableTime(returned)
	equipment.LastInspectedAt, _ = nullableTime(inspected)
	return loan, equipment, err
}

func (s *Store) CompleteReturn(ctx context.Context, tx *sql.Tx, loan domain.EquipmentLoan, equipment domain.Equipment, actorID int64, damaged bool, damageCode, responsibility, notes string) (*domain.MaintenanceCase, error) {
	loanState, equipmentState := "returned", "available"
	if damaged {
		loanState, equipmentState = "damaged", "isolated"
	}
	now := s.now()
	result, err := tx.ExecContext(ctx, `UPDATE equipment_loans SET status=?,returned_at=?,version=version+1 WHERE id=? AND version=? AND status='active'`, loanState, timeText(now), loan.ID, loan.Version)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return nil, domain.ErrConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE equipment SET status=?,version=version+1 WHERE id=? AND version=? AND status='checked_out'`, equipmentState, equipment.ID, equipment.Version)
	if err != nil {
		return nil, err
	}
	n, _ = result.RowsAffected()
	if n != 1 {
		return nil, domain.ErrConflict
	}
	if !damaged {
		return nil, nil
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO maintenance_cases(equipment_id,loan_id,opened_by,status,damage_code,responsibility,notes,opened_at) VALUES(?,?,?,'open',?,?,?,?)`, equipment.ID, loan.ID, actorID, damageCode, responsibility, notes, timeText(now))
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &domain.MaintenanceCase{ID: id, EquipmentID: equipment.ID, LoanID: loan.ID, OpenedBy: actorID, Status: "open", DamageCode: damageCode, Responsibility: responsibility, Notes: notes, OpenedAt: now, Version: 1}, nil
}

func (s *Store) CompleteReturnAtomic(ctx context.Context, loan domain.EquipmentLoan, equipment domain.Equipment, actorID int64, damaged bool, damageCode, responsibility, notes string) (*domain.MaintenanceCase, error) {
	var maintenance *domain.MaintenanceCase
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		var err error
		maintenance, err = s.CompleteReturn(ctx, tx, loan, equipment, actorID, damaged, damageCode, responsibility, notes)
		return err
	})
	return maintenance, err
}
