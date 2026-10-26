package db_test

import (
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/tonis/c3pkg/internal/db"
	"github.com/tonis/c3pkg/migrations"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	files, _ := fs.Glob(migrations.FS, "*.sql")
	for i := 0; i < 2; i++ { // second open must be a no-op
		d, err := db.Open(path, migrations.FS)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(files) {
			t.Fatalf("migrations applied = %d, err %v", n, err)
		}
		if _, err := d.Exec(`INSERT INTO packages_fts(name, description, tags) VALUES ('vk','vulkan bindings','gpu')`); err != nil {
			t.Fatalf("fts5 unavailable: %v", err)
		}
		d.Close()
	}
}

// Packages that already have a README when readme_source is added were uploaded by hand.
func TestReadmeSourceMigrationKeepsExistingReadmes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	first, _ := fs.ReadFile(migrations.FS, "0001_init.sql")
	d, err := db.Open(path, fstest.MapFS{"0001_init.sql": {Data: first}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO users(id, email, login) VALUES (1, 'a@example.com', 'a')`); err != nil {
		t.Fatal(err)
	}
	for name, md := range map[string]string{"with-readme": "# hi", "without": ""} {
		if _, err := d.Exec(`INSERT INTO packages(name, name_key, owner_id, readme_md) VALUES (?, ?, 1, ?)`, name, name, md); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	d, err = db.Open(path, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for name, want := range map[string]string{"with-readme": "manual", "without": "zip"} {
		var got string
		if err := d.QueryRow(`SELECT readme_source FROM packages WHERE name = ?`, name).Scan(&got); err != nil || got != want {
			t.Errorf("%s: readme_source = %q (err %v), want %q", name, got, err, want)
		}
	}
}
