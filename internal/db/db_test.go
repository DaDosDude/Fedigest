package db

import (
	"io/fs"
	"path/filepath"
	"testing"
)

func TestFreshDatabase(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var theme string
	if err := conn.QueryRow(`SELECT theme FROM site_settings WHERE id = 1`).Scan(&theme); err != nil || theme != "light" {
		t.Errorf("site_settings: theme %q, err %v", theme, err)
	}
	var modes int
	conn.QueryRow(`SELECT COUNT(*) FROM theme_modes`).Scan(&modes)
	if modes == 0 {
		t.Error("theme_modes not seeded")
	}
	// FTS stays in sync with picks.
	conn.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('a', 'x', 'admin')`)
	conn.Exec(`INSERT INTO categories (name, slug) VALUES ('C', 'c')`)
	if _, err := conn.Exec(`INSERT INTO picks (slug, title, excerpt, body, category_id, curator_id, source_type) VALUES ('s', 'Zebra crossing', 'e', 'b', 1, 1, 'local')`); err != nil {
		t.Fatal(err)
	}
	var hits int
	conn.QueryRow(`SELECT COUNT(*) FROM picks_fts WHERE picks_fts MATCH 'zebra'`).Scan(&hits)
	if hits != 1 {
		t.Errorf("full-text search hits: %d", hits)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	for i := 0; i < 2; i++ {
		conn, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		conn.Close()
	}
}

func TestSquashedMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A database that ran every pre-release migration.
	for _, name := range squashedMigrations {
		conn.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name)
	}
	conn.Close()
	conn, err = Open(path)
	if err != nil {
		t.Fatalf("upgraded database refused: %v", err)
	}
	var n int
	conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	conn.Close()
	if want := len(mustMigrations(t)); n != want {
		t.Errorf("schema_migrations rows after cleanup: %d, want %d", n, want)
	}

	// One that stopped partway is refused.
	path = filepath.Join(t.TempDir(), "p.db")
	conn, _ = Open(path)
	conn.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, squashedMigrations[3])
	conn.Close()
	if _, err := Open(path); err == nil {
		t.Error("partly migrated database accepted")
	}
}

func mustMigrations(t *testing.T) []fs.DirEntry {
	entries, err := fs.ReadDir(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
