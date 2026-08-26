package domain

import "fmt"

var transitions = map[string]map[string]bool{
	"course":      {"scheduled:in_progress": true, "scheduled:canceled": true, "in_progress:completed": true, "in_progress:canceled": true},
	"enrollment":  {"confirmed:attended": true, "confirmed:canceled": true, "confirmed:makeup_due": true, "makeup_due:confirmed": true},
	"assessment":  {"draft:submitted": true, "submitted:passed": true, "submitted:failed": true, "failed:submitted": true},
	"equipment":   {"available:checked_out": true, "checked_out:available": true, "checked_out:isolated": true, "isolated:maintenance": true, "maintenance:available": true, "maintenance:retired": true},
	"loan":        {"active:returned": true, "active:damaged": true},
	"maintenance": {"open:repairing": true, "repairing:released": true, "repairing:retired": true},
	"job":         {"pending:running": true, "retry:running": true, "running:succeeded": true, "running:retry": true, "running:failed": true},
}

func ValidateTransition(machine, from, to string) error {
	if from == to {
		return NewError(KindInvalid, "unchanged_state", "state transition must change the state")
	}
	if transitions[machine][from+":"+to] {
		return nil
	}
	return NewError(KindConflict, "illegal_transition", fmt.Sprintf("cannot move %s from %s to %s", machine, from, to))
}

func IsTerminal(machine, state string) bool {
	switch machine {
	case "course":
		return state == "completed" || state == "canceled"
	case "assessment":
		return state == "passed"
	case "maintenance":
		return state == "released" || state == "retired"
	case "job":
		return state == "succeeded" || state == "failed"
	default:
		return false
	}
}
