package domain

import (
	"errors"
	"testing"
	"time"
)

func TestValidateTransitionAcceptsDocumentedLifecycleEdges(t *testing.T) {
	t.Parallel()
	cases := []struct{ machine, from, to string }{
		{"course", "scheduled", "in_progress"},
		{"course", "scheduled", "canceled"},
		{"course", "in_progress", "completed"},
		{"enrollment", "confirmed", "attended"},
		{"enrollment", "confirmed", "makeup_due"},
		{"enrollment", "makeup_due", "confirmed"},
		{"assessment", "draft", "submitted"},
		{"assessment", "submitted", "passed"},
		{"assessment", "failed", "submitted"},
		{"equipment", "available", "checked_out"},
		{"equipment", "checked_out", "isolated"},
		{"equipment", "isolated", "maintenance"},
		{"equipment", "maintenance", "retired"},
		{"loan", "active", "returned"},
		{"loan", "active", "damaged"},
		{"maintenance", "open", "repairing"},
		{"maintenance", "repairing", "released"},
		{"job", "pending", "running"},
		{"job", "running", "retry"},
		{"job", "running", "succeeded"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.machine+"_"+tc.from+"_"+tc.to, func(t *testing.T) {
			t.Parallel()
			if err := ValidateTransition(tc.machine, tc.from, tc.to); err != nil {
				t.Fatalf("expected valid transition, got %v", err)
			}
		})
	}
}

func TestValidateTransitionRejectsIllegalAndUnchangedEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		machine, from, to, code string
		kind                    ErrorKind
	}{
		{"course", "scheduled", "completed", "illegal_transition", KindConflict},
		{"course", "completed", "in_progress", "illegal_transition", KindConflict},
		{"assessment", "passed", "submitted", "illegal_transition", KindConflict},
		{"equipment", "available", "maintenance", "illegal_transition", KindConflict},
		{"equipment", "retired", "available", "illegal_transition", KindConflict},
		{"job", "succeeded", "running", "illegal_transition", KindConflict},
		{"loan", "active", "active", "unchanged_state", KindInvalid},
		{"unknown", "a", "b", "illegal_transition", KindConflict},
	}
	for _, tc := range cases {
		t.Run(tc.machine+"_"+tc.from+"_"+tc.to, func(t *testing.T) {
			err := ValidateTransition(tc.machine, tc.from, tc.to)
			if err == nil {
				t.Fatal("expected rejection")
			}
			kind, code, _ := ErrorDetails(err)
			if kind != tc.kind || code != tc.code {
				t.Fatalf("unexpected details: %s %s", kind, code)
			}
		})
	}
}

func TestTerminalStatesAreSpecificToEachMachine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		machine, state string
		terminal       bool
	}{
		{"course", "completed", true}, {"course", "canceled", true}, {"course", "scheduled", false},
		{"assessment", "passed", true}, {"assessment", "failed", false},
		{"maintenance", "released", true}, {"maintenance", "retired", true}, {"maintenance", "repairing", false},
		{"job", "succeeded", true}, {"job", "failed", true}, {"job", "retry", false},
		{"equipment", "retired", false}, {"unknown", "done", false},
	}
	for _, tc := range cases {
		if got := IsTerminal(tc.machine, tc.state); got != tc.terminal {
			t.Errorf("IsTerminal(%q,%q)=%v want %v", tc.machine, tc.state, got, tc.terminal)
		}
	}
}

func TestMinorBoundaryUsesEighteenthBirthday(t *testing.T) {
	t.Parallel()
	birth := time.Date(2008, time.August, 24, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		at    time.Time
		minor bool
	}{
		{"day before birthday", time.Date(2026, time.August, 23, 23, 59, 59, 0, time.UTC), true},
		{"start of birthday", time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC), true},
		{"exact birth instant", time.Date(2026, time.August, 24, 10, 0, 0, 0, time.UTC), false},
		{"after birthday", time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMinor(birth, tc.at); got != tc.minor {
				t.Fatalf("got %v want %v", got, tc.minor)
			}
		})
	}
}

func TestRoleAllowedUsesExplicitAllowList(t *testing.T) {
	t.Parallel()
	if !RoleAllowed(RoleCoach, RoleGuardian, RoleCoach) {
		t.Fatal("coach should match explicit allow list")
	}
	if RoleAllowed(RoleEquipmentManager, RoleGuardian, RoleCoach) {
		t.Fatal("equipment manager must not inherit unrelated authority")
	}
	if RoleAllowed(RoleAdministrator) {
		t.Fatal("empty allow list must deny")
	}
}

func TestDomainErrorsPreserveClassificationAndCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("disk unavailable")
	err := Wrap(KindUnavailable, "audit_write_failed", "could not write audit event", cause)
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause was not retained")
	}
	kind, code, message := ErrorDetails(err)
	if kind != KindUnavailable || code != "audit_write_failed" || message != "could not write audit event" {
		t.Fatalf("unexpected error details: %s %s %q", kind, code, message)
	}
	if err.Error() != "could not write audit event: disk unavailable" {
		t.Fatalf("unexpected rendered error: %q", err.Error())
	}
}

func TestUnknownErrorsMapToSafeUnavailableResponse(t *testing.T) {
	t.Parallel()
	kind, code, message := ErrorDetails(errors.New("secret database detail"))
	if kind != KindUnavailable || code != "internal_error" {
		t.Fatalf("unexpected classification: %s %s", kind, code)
	}
	if message != "the service could not complete the request" {
		t.Fatalf("unsafe message: %q", message)
	}
}
