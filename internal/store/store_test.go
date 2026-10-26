package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tonis/c3pkg/internal/db"
	"github.com/tonis/c3pkg/internal/store"
	"github.com/tonis/c3pkg/migrations"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return &store.Store{DB: d}
}

func TestUsersUniqueness(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if _, err := s.CreateUser(ctx, "A@Example.com", "alice", "Alice", "hash", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "a@example.com", "alice2", "A", "hash", 0, "", ""); !errors.Is(err, store.ErrEmailTaken) {
		t.Errorf("want ErrEmailTaken (case-insensitive), got %v", err)
	}
	if _, err := s.CreateUser(ctx, "b@example.com", "ALICE", "A", "hash", 0, "", ""); !errors.Is(err, store.ErrLoginTaken) {
		t.Errorf("want ErrLoginTaken (case-insensitive), got %v", err)
	}
	u, err := s.UserByEmail(ctx, "A@EXAMPLE.COM")
	if err != nil || u.Login != "alice" {
		t.Errorf("lookup by email: %v %+v", err, u)
	}
	if _, err := s.UserByLogin(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestValidLogin(t *testing.T) {
	for in, want := range map[string]bool{"alice": true, "a-b-c": true, "x": false, "-ab": false, "ab-": false,
		"Alice": false, "a_b": false, "admin": false, "api": false, "ab": true} {
		if got := store.ValidLogin(in); got != want {
			t.Errorf("ValidLogin(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSessionsAndBan(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	u, _ := s.CreateUser(ctx, "a@example.com", "alice", "Alice", "hash", 0, "", "")
	raw, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.UserBySession(ctx, raw); err != nil || got.ID != u.ID {
		t.Fatalf("session lookup: %v", err)
	}
	s.DB.Exec(`UPDATE users SET banned_at = '2026-01-01T00:00:00Z' WHERE id = ?`, u.ID)
	if _, err := s.UserBySession(ctx, raw); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("banned user must lose session, got %v", err)
	}
	s.DB.Exec(`UPDATE users SET banned_at = NULL WHERE id = ?`, u.ID)
	s.DB.Exec(`UPDATE sessions SET expires_at = '2000-01-01T00:00:00Z'`)
	if _, err := s.UserBySession(ctx, raw); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired session accepted: %v", err)
	}
}

func TestAPITokens(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	u, _ := s.CreateUser(ctx, "a@example.com", "alice", "Alice", "hash", 0, "", "")
	raw, err := s.CreateAPIToken(ctx, u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.UserByAPIToken(ctx, raw); err != nil || got.ID != u.ID {
		t.Fatalf("token lookup: %v", err)
	}
	list, _ := s.ListAPITokens(ctx, u.ID)
	if len(list) != 1 || list[0].LastUsedAt == "" {
		t.Fatalf("list = %+v", list)
	}
	s.RevokeAPIToken(ctx, u.ID+1, list[0].ID) // someone else's id must not revoke it
	if _, err := s.UserByAPIToken(ctx, raw); err != nil {
		t.Errorf("revoked by wrong user: %v", err)
	}
	s.RevokeAPIToken(ctx, u.ID, list[0].ID)
	if _, err := s.UserByAPIToken(ctx, raw); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revoked token still valid: %v", err)
	}
}
