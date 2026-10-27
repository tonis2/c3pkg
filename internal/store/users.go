// Package store holds the SQL queries behind the HTTP handlers.
package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrEmailTaken = errors.New("email already registered")
	ErrLoginTaken = errors.New("username already taken")
)

type Store struct{ DB *sql.DB }

type User struct {
	ID           int64
	Email        string
	Login        string
	Name         string
	PasswordHash string
	GitHubID     int64
	GitHubLogin  string
	Bio          string
	AvatarURL    string
	IsAdmin      bool
	Banned       bool
	CreatedAt    string
}

const userCols = `id, email, login, name, COALESCE(password_hash,''), COALESCE(github_id,0), github_login,
	bio, avatar_url, is_admin, banned_at IS NOT NULL, created_at`

type scanner interface{ Scan(...any) error }

func scanUser(r scanner) (*User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Email, &u.Login, &u.Name, &u.PasswordHash, &u.GitHubID, &u.GitHubLogin,
		&u.Bio, &u.AvatarURL, &u.IsAdmin, &u.Banned, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

var loginRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$`)

var reservedLogins = map[string]bool{
	"admin": true, "api": true, "login": true, "logout": true, "register": true, "settings": true,
	"dashboard": true, "static": true, "auth": true, "index": true, "packages": true, "users": true,
	"search": true, "about": true, "help": true, "root": true, "c3": true, "c3pkg": true,
	"info": true, "download": true,
}

// ValidLogin reports whether s is an allowed username (lowercase letters, digits, hyphens).
func ValidLogin(s string) bool {
	return len(s) >= 2 && loginRe.MatchString(s) && !reservedLogins[s]
}

func uniqueViolation(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") {
		return msg
	}
	return ""
}

// CreateUser inserts a user. passwordHash may be empty for GitHub-only accounts.
func (s *Store) CreateUser(ctx context.Context, email, login, name, passwordHash string, githubID int64, githubLogin, avatarURL string) (*User, error) {
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO users(email, login, name, password_hash, github_id, github_login, avatar_url)
		 VALUES (?, ?, ?, NULLIF(?,''), NULLIF(?,0), ?, ?)`,
		strings.ToLower(email), login, name, passwordHash, githubID, githubLogin, avatarURL)
	if msg := uniqueViolation(err); msg != "" {
		switch {
		case strings.Contains(msg, "users.email"):
			return nil, ErrEmailTaken
		case strings.Contains(msg, "users.login"):
			return nil, ErrLoginTaken
		}
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(ctx, id)
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, strings.ToLower(email)))
}

func (s *Store) UserByLogin(ctx context.Context, login string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE login = ?`, strings.ToLower(login)))
}

func (s *Store) UserByGitHubID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE github_id = ?`, id))
}

func (s *Store) UpdateProfile(ctx context.Context, id int64, name, bio, githubLogin string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET name = ?, bio = ?, github_login = ? WHERE id = ?`, name, bio, githubLogin, id)
	return err
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// LinkGitHub attaches a GitHub identity to an existing account.
func (s *Store) LinkGitHub(ctx context.Context, id, githubID int64, githubLogin string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET github_id = ?, github_login = ? WHERE id = ?`, githubID, githubLogin, id)
	return err
}
