package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/auth"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"github.com/11DingKing/urban-sports-safety-hub/internal/enrollment"
	"github.com/11DingKing/urban-sports-safety-hub/internal/equipment"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
)

type failingHealth struct{ err error }

func (f failingHealth) Ping(context.Context) error { return f.err }

type apiFixture struct {
	store   *dbstore.Store
	auth    *auth.Service
	handler http.Handler
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	store, err := dbstore.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authService := auth.New(store, time.Hour)
	auditService := audit.New(store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(authService, enrollment.New(store, auditService), equipment.New(store, auditService), store, logger)
	return apiFixture{store: store, auth: authService, handler: api.Handler()}
}

func perform(handler http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func decodeError(t *testing.T, response *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var body errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", response.Body.String(), err)
	}
	return body
}

func TestHealthEndpointIsPublicAndReturnsAlive(t *testing.T) {
	f := newAPIFixture(t)
	response := perform(f.handler, http.MethodGet, "/healthz", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type=%q", response.Header().Get("Content-Type"))
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "alive" {
		t.Fatalf("body=%v", body)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("request ID header missing")
	}
}

func TestReadinessChecksDatabaseDependency(t *testing.T) {
	f := newAPIFixture(t)
	response := perform(f.handler, http.MethodGet, "/readyz", nil, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReadinessMapsDependencyFailureWithoutLeakingCause(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(auth.New(&unusableAuthRepository{}, time.Hour), nil, nil, failingHealth{err: errors.New("secret dsn and password")}, logger)
	response := perform(api.Handler(), http.MethodGet, "/readyz", nil, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := decodeError(t, response)
	if body.Error.Code != "internal_error" || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("unsafe error body: %s", response.Body.String())
	}
}

type unusableAuthRepository struct{}

func (*unusableAuthRepository) CreateAccount(context.Context, domain.Account) (domain.Account, error) {
	return domain.Account{}, errors.New("unused")
}
func (*unusableAuthRepository) AccountByEmail(context.Context, string) (domain.Account, error) {
	return domain.Account{}, errors.New("unused")
}
func (*unusableAuthRepository) CreateSession(context.Context, int64, string, time.Time) (int64, error) {
	return 0, errors.New("unused")
}
func (*unusableAuthRepository) ResolveSession(context.Context, string, time.Time) (domain.Principal, error) {
	return domain.Principal{}, errors.New("unused")
}
func (*unusableAuthRepository) RevokeSession(context.Context, string) error {
	return errors.New("unused")
}

func TestLoginReturnsOpaqueSessionAndRole(t *testing.T) {
	f := newAPIFixture(t)
	account, err := f.auth.Register(context.Background(), "guardian@example.test", "guardian-password", "Guardian", domain.RoleGuardian)
	if err != nil {
		t.Fatal(err)
	}
	response := perform(f.handler, http.MethodPost, "/api/login", strings.NewReader(`{"email":"guardian@example.test","password":"guardian-password"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body auth.LoginResult
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Token == "" || body.AccountID != account.ID || body.Role != domain.RoleGuardian || body.ExpiresAt.IsZero() {
		t.Fatalf("unexpected login body: %+v", body)
	}
}

func TestLoginRejectsInvalidCredentialsWithStableError(t *testing.T) {
	f := newAPIFixture(t)
	_, _ = f.auth.Register(context.Background(), "guardian@example.test", "guardian-password", "Guardian", domain.RoleGuardian)
	response := perform(f.handler, http.MethodPost, "/api/login", strings.NewReader(`{"email":"guardian@example.test","password":"wrong-password"}`), nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	body := decodeError(t, response)
	if body.Error.Code != "bad_credentials" || body.Error.RequestID == "" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestLoginRejectsUnknownAndMultipleJSONFields(t *testing.T) {
	cases := []struct{ name, body, code string }{{"unknown", `{"email":"a@test","password":"long-password","admin":true}`, "invalid_json"}, {"multiple", `{"email":"a@test","password":"long-password"} {}`, "multiple_json_values"}, {"malformed", `{"email":`, "invalid_json"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPIFixture(t)
			response := perform(f.handler, http.MethodPost, "/api/login", strings.NewReader(tc.body), nil)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if body := decodeError(t, response); body.Error.Code != tc.code {
				t.Fatalf("code=%s want %s", body.Error.Code, tc.code)
			}
		})
	}
}

func TestProtectedEndpointsRequireBearerSession(t *testing.T) {
	f := newAPIFixture(t)
	paths := []string{"/api/logout", "/api/enrollments", "/api/equipment/checkout", "/api/equipment/return", "/api/course-sessions/1/cancel"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := perform(f.handler, http.MethodPost, path, strings.NewReader(`{}`), nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if body := decodeError(t, response); body.Error.Code != "missing_session" {
				t.Fatalf("error=%+v", body)
			}
		})
	}
}

func TestProtectedEndpointsRejectUnknownToken(t *testing.T) {
	f := newAPIFixture(t)
	response := perform(f.handler, http.MethodPost, "/api/enrollments", strings.NewReader(`{}`), map[string]string{"Authorization": "Bearer does-not-exist"})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body := decodeError(t, response); body.Error.Code != "invalid_session" {
		t.Fatalf("error=%+v", body)
	}
}

func TestLogoutRevokesTokenForSubsequentRequests(t *testing.T) {
	f := newAPIFixture(t)
	_, _ = f.auth.Register(context.Background(), "logout@example.test", "logout-password", "Logout", domain.RoleGuardian)
	login, err := f.auth.Login(context.Background(), "logout@example.test", "logout-password")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + login.Token}
	response := perform(f.handler, http.MethodPost, "/api/logout", nil, headers)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = perform(f.handler, http.MethodPost, "/api/enrollments", strings.NewReader(`{}`), headers)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequestIDIsAcceptedAndEchoed(t *testing.T) {
	f := newAPIFixture(t)
	response := perform(f.handler, http.MethodGet, "/healthz", nil, map[string]string{"X-Request-ID": "edge-request-42"})
	if got := response.Header().Get("X-Request-ID"); got != "edge-request-42" {
		t.Fatalf("request id=%q", got)
	}
}

func TestOversizedRequestBodyIsRejected(t *testing.T) {
	f := newAPIFixture(t)
	large := bytes.Repeat([]byte("x"), (1<<20)+100)
	payload := append([]byte(`{"email":"`), large...)
	payload = append(payload, []byte(`","password":"long-password"}`)...)
	response := perform(f.handler, http.MethodPost, "/api/login", bytes.NewReader(payload), nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body prefix=%s", response.Code, response.Body.String()[:min(100, response.Body.Len())])
	}
	if body := decodeError(t, response); body.Error.Code != "invalid_json" {
		t.Fatalf("error=%+v", body)
	}
}

func TestUnknownRouteReturnsStandardNotFound(t *testing.T) {
	f := newAPIFixture(t)
	response := perform(f.handler, http.MethodGet, "/api/not-real", nil, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
