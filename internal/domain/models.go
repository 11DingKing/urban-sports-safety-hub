package domain

import "time"

type Role string

const (
	RoleGuardian         Role = "guardian"
	RoleCoach            Role = "coach"
	RoleEquipmentManager Role = "equipment_manager"
	RoleAdministrator    Role = "administrator"
)

type Sport string

const (
	SportClimbing      Sport = "climbing"
	SportSkateboarding Sport = "skateboarding"
	SportFlyingDisc    Sport = "flying_disc"
)

type Account struct {
	ID                               int64
	Email, PasswordHash, DisplayName string
	Role                             Role
	Active                           bool
	CreatedAt                        time.Time
}
type Principal struct {
	AccountID int64
	Role      Role
	SessionID int64
	ExpiresAt time.Time
}
type Student struct {
	ID, GuardianID       int64
	Name                 string
	BirthDate            time.Time
	ShoeSize, HelmetSize string
	Version              int64
	CreatedAt            time.Time
}
type Consent struct {
	ID, StudentID, GuardianID int64
	Scope                     string
	GrantedAt, ExpiresAt      time.Time
	RevokedAt                 *time.Time
}
type CoachQualification struct {
	ID, CoachID           int64
	Sport                 Sport
	Level                 int
	ValidFrom, ValidUntil time.Time
	Status                string
	Version               int64
}
type CourseTemplate struct {
	ID                                      int64
	Name                                    string
	Sport                                   Sport
	Level, MinimumAge, Capacity, CoachRatio int
	RequiredCertification                   string
}
type CourseSession struct {
	ID, TemplateID, CoachID     int64
	StartsAt, EndsAt            time.Time
	Status                      string
	Capacity, Enrolled, Version int
	CancelReason                string
}
type Enrollment struct {
	ID, SessionID, StudentID int64
	Status, IdempotencyKey   string
	Version                  int
	CreatedAt                time.Time
}
type TrainingGroup struct {
	ID, SessionID, CoachID int64
	Name                   string
	Capacity, Version      int
}
type Assessment struct {
	ID, StudentID, SessionID, ExaminerID int64
	Sport                                Sport
	Level                                int
	Status, Notes                        string
	Score, Version                       int
	CreatedAt                            time.Time
}
type Equipment struct {
	ID                                  int64
	AssetTag, Kind, Sport, Size, Status string
	Version                             int
	LastInspectedAt                     *time.Time
}
type EquipmentLoan struct {
	ID, EquipmentID, StudentID, SessionID, IssuedBy int64
	Status                                          string
	CheckedOutAt                                    time.Time
	ReturnedAt                                      *time.Time
	Version                                         int
}
type MaintenanceCase struct {
	ID, EquipmentID, LoanID, OpenedBy         int64
	Status, DamageCode, Responsibility, Notes string
	OpenedAt                                  time.Time
	ClosedAt                                  *time.Time
	Version                                   int
}
type AuditEvent struct {
	ID                     int64
	ActorID                *int64
	RequestID, ObjectType  string
	ObjectID               int64
	Action, Result, Detail string
	CreatedAt              time.Time
}
type Job struct {
	ID                         int64
	Kind, Key, Payload, Status string
	Attempts, MaxAttempts      int
	AvailableAt                time.Time
	LastError                  string
	LockedAt                   *time.Time
	CreatedAt, UpdatedAt       time.Time
}

func IsMinor(birth, at time.Time) bool { return birth.AddDate(18, 0, 0).After(at) }
func RoleAllowed(role Role, allowed ...Role) bool {
	for _, candidate := range allowed {
		if role == candidate {
			return true
		}
	}
	return false
}
