package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

type Repository interface {
	CreateAccount(context.Context, domain.Account) (domain.Account, error)
	AccountByEmail(context.Context, string) (domain.Account, error)
	CreateSession(context.Context, int64, string, time.Time) (int64, error)
	ResolveSession(context.Context, string, time.Time) (domain.Principal, error)
	RevokeSession(context.Context, string) error
}
type Service struct {
	repo Repository
	ttl  time.Duration
	now  func() time.Time
}
type LoginResult struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expires_at"`
	AccountID int64       `json:"account_id"`
	Role      domain.Role `json:"role"`
}

func New(repo Repository, ttl time.Duration) *Service {
	return &Service{repo: repo, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}
}
func (s *Service) SetClock(now func() time.Time) { s.now = now }
func (s *Service) Register(ctx context.Context, email, password, name string, role domain.Role) (domain.Account, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	name = strings.TrimSpace(name)
	if !strings.Contains(email, "@") || len(password) < 10 || name == "" {
		return domain.Account{}, domain.NewError(domain.KindInvalid, "invalid_registration", "valid email, display name and a password of at least 10 characters are required")
	}
	if !domain.RoleAllowed(role, domain.RoleGuardian, domain.RoleCoach, domain.RoleEquipmentManager, domain.RoleAdministrator) {
		return domain.Account{}, domain.NewError(domain.KindInvalid, "invalid_role", "unsupported account role")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return domain.Account{}, domain.Wrap(domain.KindUnavailable, "password_hash_failed", "could not secure password", err)
	}
	return s.repo.CreateAccount(ctx, domain.Account{Email: email, PasswordHash: string(hash), DisplayName: name, Role: role, Active: true})
}
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	account, err := s.repo.AccountByEmail(ctx, email)
	if err != nil {
		return LoginResult{}, err
	}
	if !account.Active {
		return LoginResult{}, domain.NewError(domain.KindForbidden, "account_disabled", "account is disabled")
	}
	if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)) != nil {
		return LoginResult{}, domain.NewError(domain.KindUnauthorized, "bad_credentials", "email or password is incorrect")
	}
	token, err := newToken()
	if err != nil {
		return LoginResult{}, domain.Wrap(domain.KindUnavailable, "token_generation_failed", "could not create session", err)
	}
	expires := s.now().Add(s.ttl)
	if _, err = s.repo.CreateSession(ctx, account.ID, hashToken(token), expires); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, ExpiresAt: expires, AccountID: account.ID, Role: account.Role}, nil
}
func (s *Service) Authenticate(ctx context.Context, token string) (domain.Principal, error) {
	if strings.TrimSpace(token) == "" {
		return domain.Principal{}, domain.NewError(domain.KindUnauthorized, "missing_session", "authentication token is required")
	}
	return s.repo.ResolveSession(ctx, hashToken(token), s.now())
}
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return domain.NewError(domain.KindUnauthorized, "missing_session", "authentication token is required")
	}
	return s.repo.RevokeSession(ctx, hashToken(token))
}
func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
