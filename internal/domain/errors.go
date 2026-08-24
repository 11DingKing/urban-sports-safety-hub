package domain

import (
	"errors"
	"fmt"
)

type ErrorKind string

const (
	KindInvalid      ErrorKind = "invalid"
	KindUnauthorized ErrorKind = "unauthorized"
	KindForbidden    ErrorKind = "forbidden"
	KindNotFound     ErrorKind = "not_found"
	KindConflict     ErrorKind = "conflict"
	KindExpired      ErrorKind = "expired"
	KindUnavailable  ErrorKind = "unavailable"
)

type Error struct {
	Kind    ErrorKind
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(kind ErrorKind, code, message string) error {
	return &Error{Kind: kind, Code: code, Message: message}
}

func Wrap(kind ErrorKind, code, message string, cause error) error {
	return &Error{Kind: kind, Code: code, Message: message, Cause: cause}
}

func ErrorDetails(err error) (ErrorKind, string, string) {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind, target.Code, target.Message
	}
	return KindUnavailable, "internal_error", "the service could not complete the request"
}

var (
	ErrConflict = NewError(KindConflict, "version_conflict", "the record changed concurrently")
	ErrCanceled = NewError(KindUnavailable, "operation_canceled", "the operation was canceled")
)
