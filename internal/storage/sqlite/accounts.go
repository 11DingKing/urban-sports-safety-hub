package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

func (s *Store) CreateAccount(ctx context.Context, account domain.Account) (domain.Account, error) {
	account.Email = strings.ToLower(strings.TrimSpace(account.Email))
	account.CreatedAt = s.now()
	result, err := s.db.ExecContext(ctx, `INSERT INTO accounts(email,password_hash,display_name,role,active,created_at) VALUES(?,?,?,?,?,?)`, account.Email, account.PasswordHash, account.DisplayName, account.Role, boolInt(account.Active), timeText(account.CreatedAt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return account, domain.NewError(domain.KindConflict, "email_exists", "an account already uses that email")
		}
		return account, fmt.Errorf("insert account: %w", err)
	}
	account.ID, err = result.LastInsertId()
	return account, err
}

func (s *Store) AccountByEmail(ctx context.Context, email string) (domain.Account, error) {
	var a domain.Account
	var active int
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,email,password_hash,display_name,role,active,created_at FROM accounts WHERE email=?`, strings.ToLower(strings.TrimSpace(email))).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.DisplayName, &a.Role, &active, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, domain.NewError(domain.KindUnauthorized, "bad_credentials", "email or password is incorrect")
	}
	if err != nil {
		return a, fmt.Errorf("query account: %w", err)
	}
	a.Active = active == 1
	a.CreatedAt, err = parseTime(created)
	return a, err
}

func (s *Store) AccountByID(ctx context.Context, id int64) (domain.Account, error) {
	var a domain.Account
	var active int
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,email,password_hash,display_name,role,active,created_at FROM accounts WHERE id=?`, id).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.DisplayName, &a.Role, &active, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, domain.NewError(domain.KindNotFound, "account_not_found", "account was not found")
	}
	if err != nil {
		return a, err
	}
	a.Active = active == 1
	a.CreatedAt, err = parseTime(created)
	return a, err
}

func (s *Store) CreateSession(ctx context.Context, accountID int64, tokenHash string, expires time.Time) (int64, error) {
	persistCtx := context.WithoutCancel(ctx)
	result, err := s.db.ExecContext(persistCtx, `INSERT INTO sessions(token_hash,account_id,expires_at,created_at) VALUES(?,?,?,?)`, tokenHash, accountID, timeText(expires), timeText(s.now()))
	if err != nil {
		return 0, fmt.Errorf("insert session: %w", err)
	}
	return result.LastInsertId()
}
func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, timeText(s.now()), tokenHash)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return domain.NewError(domain.KindUnauthorized, "invalid_session", "session is not active")
	}
	return nil
}
func (s *Store) ResolveSession(ctx context.Context, tokenHash string, now time.Time) (domain.Principal, error) {
	var p domain.Principal
	var expires string
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.role,s.id,s.expires_at,a.active FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.token_hash=? AND s.revoked_at IS NULL`, tokenHash).Scan(&p.AccountID, &p.Role, &p.SessionID, &expires, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return p, domain.NewError(domain.KindUnauthorized, "invalid_session", "session is not active")
	}
	if err != nil {
		return p, err
	}
	p.ExpiresAt, err = parseTime(expires)
	if err != nil {
		return p, err
	}
	if !p.ExpiresAt.After(now) {
		return p, domain.NewError(domain.KindExpired, "session_expired", "session has expired")
	}
	if active != 1 {
		return p, domain.NewError(domain.KindForbidden, "account_disabled", "account is disabled")
	}
	return p, nil
}
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=? OR (revoked_at IS NOT NULL AND revoked_at<=?)`, timeText(now), timeText(now.Add(-24*time.Hour)))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
