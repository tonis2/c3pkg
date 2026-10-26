package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/tonis/c3pkg/internal/auth"
)

const SessionTTL = 30 * 24 * time.Hour

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// CreateSession stores a new session and returns the raw token for the cookie.
func (s *Store) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	raw, hash, err := auth.NewToken("")
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(SessionTTL)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, expires_at) VALUES (?, ?, ?)`, hash, userID, ts(exp))
	return raw, exp, err
}

// UserBySession returns the user for a raw session token, or ErrNotFound if it is
// unknown, expired, or belongs to a banned user.
func (s *Store) UserBySession(ctx context.Context, raw string) (*User, error) {
	var id int64
	var exp string
	err := s.DB.QueryRowContext(ctx, `SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`, auth.HashToken(raw)).Scan(&id, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if t, perr := time.Parse(time.RFC3339, exp); perr != nil || time.Now().After(t) {
		s.DeleteSession(ctx, raw)
		return nil, ErrNotFound
	}
	u, err := s.UserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.Banned {
		return nil, ErrNotFound
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, raw string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, auth.HashToken(raw))
	return err
}

// DeleteUserSessions signs a user out everywhere (e.g. after a password change).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(time.Now()))
	return err
}
