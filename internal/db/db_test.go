package db_test

import (
	"path/filepath"
	"testing"

	"github.com/tonis/c3pkg/internal/db"
	"github.com/tonis/c3pkg/migrations"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	for i := 0; i < 2; i++ { // second open must be a no-op
		d, err := db.Open(path, migrations.FS)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("migrations applied = %d, err %v", n, err)
		}
		if _, err := d.Exec(`INSERT INTO packages_fts(name, description, tags) VALUES ('vk','vulkan bindings','gpu')`); err != nil {
			t.Fatalf("fts5 unavailable: %v", err)
		}
		d.Close()
	}
}
