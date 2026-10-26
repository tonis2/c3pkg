package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/tonis/c3pkg/internal/auth"
)

const APITokenPrefix = "c3p_"

type APIToken struct {
	ID         int64
	Name       string
	LastUsedAt string
	CreatedAt  string
}

// CreateAPIToken returns the raw token once; only its hash is stored.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string) (string, error) {
	raw, hash, err := auth.NewToken(APITokenPrefix)
	if err != nil {
		return "", err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO api_tokens(user_id, name, token_hash) VALUES (?, ?, ?)`, userID, name, hash)
	return raw, err
}

func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, COALESCE(last_used_at,''), created_at FROM api_tokens
		 WHERE user_id = ? AND revoked_at IS NULL ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIToken(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, ts(time.Now()), id, userID)
	return err
}

// UserByAPIToken resolves a raw API token and records its use.
func (s *Store) UserByAPIToken(ctx context.Context, raw string) (*User, error) {
	var tokenID, userID int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, user_id FROM api_tokens WHERE token_hash = ? AND revoked_at IS NULL`, auth.HashToken(raw)).Scan(&tokenID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.Banned {
		return nil, ErrNotFound
	}
	s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ts(time.Now()), tokenID)
	return u, nil
}
