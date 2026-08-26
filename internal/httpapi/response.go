package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	kind, code, message := domain.ErrorDetails(err)
	status := http.StatusInternalServerError
	switch kind {
	case domain.KindInvalid:
		status = http.StatusBadRequest
	case domain.KindUnauthorized, domain.KindExpired:
		status = http.StatusUnauthorized
	case domain.KindForbidden:
		status = http.StatusForbidden
	case domain.KindNotFound:
		status = http.StatusNotFound
	case domain.KindConflict:
		status = http.StatusConflict
	case domain.KindUnavailable:
		if errors.Is(err, contextCanceledError()) {
			status = 499
		} else {
			status = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message, RequestID: requestIDFrom(r.Context())}})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.Wrap(domain.KindInvalid, "invalid_json", "request body must be valid JSON", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.NewError(domain.KindInvalid, "multiple_json_values", "request body must contain one JSON value")
	}
	return nil
}

func contextCanceledError() error { return domain.ErrCanceled }
