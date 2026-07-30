package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (s *Store) CreateUser(ctx context.Context, user User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, name, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		user.ID, user.Login, user.PasswordHash, user.Name, user.Role, user.CreatedAt,
	)
	return err
}

func (s *Store) UserByLogin(ctx context.Context, login string) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, name, role, created_at FROM users WHERE lower(email) = lower(?)`,
		login,
	).Scan(&user.ID, &user.Login, &user.PasswordHash, &user.Name, &user.Role, &user.CreatedAt)
	return user, err
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, name, role, created_at FROM users WHERE id = ?`,
		id,
	).Scan(&user.ID, &user.Login, &user.PasswordHash, &user.Name, &user.Role, &user.CreatedAt)
	return user, err
}

func (s *Store) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)
	return err
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		session.TokenHash, session.User.ID, session.CSRFToken, session.ExpiresAt, time.Now(),
	)
	return err
}

func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (Session, error) {
	var session Session
	err := s.db.QueryRowContext(ctx, `
		SELECT s.token_hash, s.csrf_token, s.expires_at,
		       u.id, u.email, u.password_hash, u.name, u.role, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > now()
	`, tokenHash).Scan(
		&session.TokenHash, &session.CSRFToken, &session.ExpiresAt,
		&session.User.ID, &session.User.Login, &session.User.PasswordHash,
		&session.User.Name, &session.User.Role, &session.User.CreatedAt,
	)
	return session, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return err
}

func (s *Store) LockoutRemaining(ctx context.Context, identifier string) (time.Duration, error) {
	var lockedUntil time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT locked_until FROM login_attempts WHERE identifier = ?`,
		identifier,
	).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	remaining := time.Until(lockedUntil)
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}

func (s *Store) RegisterLoginFailure(ctx context.Context, identifier string, maxFailures int, lockout time.Duration) error {
	var failCount int
	err := s.db.QueryRowContext(ctx,
		`SELECT fail_count FROM login_attempts WHERE identifier = ?`,
		identifier,
	).Scan(&failCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	failCount++
	lockedUntil := time.Time{}
	if failCount >= maxFailures {
		lockedUntil = time.Now().Add(lockout)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE identifier = ?`, identifier); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO login_attempts (identifier, fail_count, locked_until, last_attempt) VALUES (?, ?, ?, ?)`,
		identifier, failCount, lockedUntil, time.Now(),
	)
	return err
}

func (s *Store) ClearLoginFailures(ctx context.Context, identifier string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE identifier = ?`, identifier)
	return err
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func WrapAuthError(action string, err error) error {
	return fmt.Errorf("%s: %w", action, err)
}
