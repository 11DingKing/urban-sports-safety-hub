package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/11DingKing/urban-sports-safety-hub/internal/auth"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"github.com/11DingKing/urban-sports-safety-hub/internal/enrollment"
	"github.com/11DingKing/urban-sports-safety-hub/internal/equipment"
)

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.auth.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err := a.auth.Logout(r.Context(), token); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) enroll(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, r, domain.NewError(domain.KindUnauthorized, "missing_principal", "authentication is required"))
		return
	}
	var input enrollment.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.enrollment.Enroll(r.Context(), principal, requestIDFrom(r.Context()), input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) cancelCourse(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	id, err := trailingID(r.URL.Path, "/api/course-sessions/", "/cancel")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	count, err := a.enrollment.CancelCourse(r.Context(), principal, requestIDFrom(r.Context()), id, input.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"makeup_entitlements_created": count})
}

func (a *API) checkout(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	var input equipment.CheckoutRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.equipment.Checkout(r.Context(), principal, requestIDFrom(r.Context()), input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *API) returnEquipment(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	var input equipment.ReturnRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.equipment.Return(r.Context(), principal, requestIDFrom(r.Context()), input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": result})
}

func trailingID(path, prefix, suffix string) (int64, error) {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return 0, domain.NewError(domain.KindNotFound, "route_not_found", "route was not found")
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	raw = strings.Trim(raw, "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return 0, domain.NewError(domain.KindInvalid, "invalid_id", "resource ID must be a positive integer")
	}
	return id, nil
}

var _ = auth.LoginResult{}
