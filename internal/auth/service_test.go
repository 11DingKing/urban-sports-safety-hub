package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

type memoryRepository struct {
	mu                                            sync.Mutex
	accounts                                      map[string]domain.Account
	sessions                                      map[string]domain.Principal
	nextAccount, nextSession                      int64
	createAccountErr, createSessionErr, revokeErr error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{accounts: map[string]domain.Account{}, sessions: map[string]domain.Principal{}, nextAccount: 1, nextSession: 1}
}
func (m *memoryRepository) CreateAccount(_ context.Context, a domain.Account) (domain.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createAccountErr != nil {
		return a, m.createAccountErr
	}
	if _, ok := m.accounts[a.Email]; ok {
		return a, domain.NewError(domain.KindConflict, "email_exists", "exists")
	}
	a.ID = m.nextAccount
	m.nextAccount++
	m.accounts[a.Email] = a
	return a, nil
}
func (m *memoryRepository) AccountByEmail(_ context.Context, email string) (domain.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[strings.ToLower(strings.TrimSpace(email))]
	if !ok {
		return a, domain.NewError(domain.KindUnauthorized, "bad_credentials", "bad credentials")
	}
	return a, nil
}
func (m *memoryRepository) CreateSession(_ context.Context, accountID int64, hash string, expires time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createSessionErr != nil {
		return 0, m.createSessionErr
	}
	id := m.nextSession
	m.nextSession++
	var role domain.Role
	for _, a := range m.accounts {
		if a.ID == accountID {
			role = a.Role
		}
	}
	m.sessions[hash] = domain.Principal{AccountID: accountID, SessionID: id, Role: role, ExpiresAt: expires}
	return id, nil
}
func (m *memoryRepository) ResolveSession(_ context.Context, hash string, now time.Time) (domain.Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.sessions[hash]
	if !ok {
		return p, domain.NewError(domain.KindUnauthorized, "invalid_session", "invalid")
	}
	if !p.ExpiresAt.After(now) {
		return p, domain.NewError(domain.KindExpired, "session_expired", "expired")
	}
	return p, nil
}
func (m *memoryRepository) RevokeSession(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revokeErr != nil {
		return m.revokeErr
	}
	if _, ok := m.sessions[hash]; !ok {
		return domain.NewError(domain.KindUnauthorized, "invalid_session", "invalid")
	}
	delete(m.sessions, hash)
	return nil
}

func TestRegisterValidatesAndHashesCredentials(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	account, err := service.Register(context.Background(), " Guardian@Example.Test ", "correct-horse", "  Guardian Gao  ", domain.RoleGuardian)
	if err != nil {
		t.Fatal(err)
	}
	if account.Email != "guardian@example.test" || account.DisplayName != "Guardian Gao" || account.Role != domain.RoleGuardian || !account.Active {
		t.Fatalf("unexpected account: %+v", account)
	}
	if account.PasswordHash == "correct-horse" {
		t.Fatal("password stored in clear text")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte("correct-horse")); err != nil {
		t.Fatalf("password hash mismatch: %v", err)
	}
}

func TestRegisterAcceptsEverySupportedBusinessRole(t *testing.T) {
	cases := []domain.Role{domain.RoleGuardian, domain.RoleCoach, domain.RoleEquipmentManager, domain.RoleAdministrator}
	for i, role := range cases {
		repo := newMemoryRepository()
		service := New(repo, time.Hour)
		account, err := service.Register(context.Background(), string(rune('a'+i))+"@test.local", "long-password", "User", role)
		if err != nil {
			t.Fatalf("role %s: %v", role, err)
		}
		if account.Role != role {
			t.Fatalf("got %s want %s", account.Role, role)
		}
	}
}

func TestRegisterRejectsInvalidInputBeforeRepositoryMutation(t *testing.T) {
	cases := []struct {
		name, email, password, display string
		role                           domain.Role
	}{{"bad email", "missing-at", "long-password", "Name", domain.RoleGuardian}, {"short password", "user@test", "short", "Name", domain.RoleGuardian}, {"empty name", "user@test", "long-password", " ", domain.RoleGuardian}, {"unsupported role", "user@test", "long-password", "Name", domain.Role("observer")}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMemoryRepository()
			service := New(repo, time.Hour)
			_, err := service.Register(context.Background(), tc.email, tc.password, tc.display, tc.role)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if len(repo.accounts) != 0 {
				t.Fatal("repository mutated for invalid request")
			}
			kind, _, _ := domain.ErrorDetails(err)
			if kind != domain.KindInvalid {
				t.Fatalf("kind=%s", kind)
			}
		})
	}
}

func TestRegisterPropagatesRepositoryConflict(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	_, _ = service.Register(context.Background(), "repeat@test", "long-password", "First", domain.RoleGuardian)
	_, err := service.Register(context.Background(), "repeat@test", "other-password", "Second", domain.RoleCoach)
	if err == nil {
		t.Fatal("expected conflict")
	}
	kind, code, _ := domain.ErrorDetails(err)
	if kind != domain.KindConflict || code != "email_exists" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginCreatesOpaqueExpiringSession(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, 90*time.Minute)
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	account, err := service.Register(context.Background(), "coach@test", "coach-password", "Coach", domain.RoleCoach)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Login(context.Background(), "COACH@test", "coach-password")
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || len(result.Token) < 40 {
		t.Fatalf("token does not appear opaque: %q", result.Token)
	}
	if result.AccountID != account.ID || result.Role != domain.RoleCoach || !result.ExpiresAt.Equal(now.Add(90*time.Minute)) {
		t.Fatalf("unexpected login: %+v", result)
	}
	if _, exists := repo.sessions[result.Token]; exists {
		t.Fatal("raw token was persisted")
	}
	principal, err := service.Authenticate(context.Background(), result.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.AccountID != account.ID || principal.Role != domain.RoleCoach {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestLoginRejectsWrongPasswordWithoutCreatingSession(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	_, _ = service.Register(context.Background(), "guardian@test", "guardian-password", "Guardian", domain.RoleGuardian)
	_, err := service.Login(context.Background(), "guardian@test", "wrong-password")
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	if len(repo.sessions) != 0 {
		t.Fatal("session created for wrong password")
	}
	kind, code, _ := domain.ErrorDetails(err)
	if kind != domain.KindUnauthorized || code != "bad_credentials" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginRejectsDisabledAccount(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	account, _ := service.Register(context.Background(), "disabled@test", "disabled-password", "Disabled", domain.RoleCoach)
	account.Active = false
	repo.accounts[account.Email] = account
	_, err := service.Login(context.Background(), account.Email, "disabled-password")
	if err == nil {
		t.Fatal("expected disabled rejection")
	}
	kind, code, _ := domain.ErrorDetails(err)
	if kind != domain.KindForbidden || code != "account_disabled" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginPropagatesSessionPersistenceFailure(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	_, _ = service.Register(context.Background(), "persist@test", "persist-password", "Persist", domain.RoleGuardian)
	sentinel := errors.New("database offline")
	repo.createSessionErr = sentinel
	_, err := service.Login(context.Background(), "persist@test", "persist-password")
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v want sentinel", err)
	}
}

func TestAuthenticateRequiresTokenAndEnforcesExpiration(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Minute)
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	_, _ = service.Register(context.Background(), "expiring@test", "expiring-password", "Expiring", domain.RoleGuardian)
	login, _ := service.Login(context.Background(), "expiring@test", "expiring-password")
	if _, err := service.Authenticate(context.Background(), ""); err == nil {
		t.Fatal("empty token accepted")
	}
	now = now.Add(2 * time.Minute)
	_, err := service.Authenticate(context.Background(), login.Token)
	if err == nil {
		t.Fatal("expired token accepted")
	}
	kind, code, _ := domain.ErrorDetails(err)
	if kind != domain.KindExpired || code != "session_expired" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLogoutRevokesOnlyPresentedSession(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	_, _ = service.Register(context.Background(), "multi@test", "multiple-password", "Multi", domain.RoleGuardian)
	first, _ := service.Login(context.Background(), "multi@test", "multiple-password")
	second, _ := service.Login(context.Background(), "multi@test", "multiple-password")
	if err := service.Logout(context.Background(), first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), first.Token); err == nil {
		t.Fatal("revoked token remains active")
	}
	if _, err := service.Authenticate(context.Background(), second.Token); err != nil {
		t.Fatalf("other session was revoked: %v", err)
	}
}

func TestLogoutRejectsMissingAndAlreadyRevokedTokens(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	if err := service.Logout(context.Background(), ""); err == nil {
		t.Fatal("missing token accepted")
	}
	_, _ = service.Register(context.Background(), "logout@test", "logout-password", "Logout", domain.RoleGuardian)
	login, _ := service.Login(context.Background(), "logout@test", "logout-password")
	if err := service.Logout(context.Background(), login.Token); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(context.Background(), login.Token); err == nil {
		t.Fatal("second logout accepted")
	}
}

func TestGeneratedTokensAreUniqueAcrossSessions(t *testing.T) {
	repo := newMemoryRepository()
	service := New(repo, time.Hour)
	_, _ = service.Register(context.Background(), "unique@test", "unique-password", "Unique", domain.RoleGuardian)
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		login, err := service.Login(context.Background(), "unique@test", "unique-password")
		if err != nil {
			t.Fatal(err)
		}
		if seen[login.Token] {
			t.Fatalf("duplicate token at iteration %d", i)
		}
		seen[login.Token] = true
	}
}
