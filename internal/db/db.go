// Package db opens the SQLite database and applies migrations on boot.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Open opens (creating if needed) the SQLite database at path and applies
// any migrations that haven't been recorded yet.
func Open(path string) (*sql.DB, error) {
	// _txlock=immediate: transactions take the write lock up front, so one
	// that reads before writing can't fail with "database is locked" when
	// another write lands in between (busy_timeout only covers waiting).
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	if err := migrate(conn); err != nil {
		return nil, fmt.Errorf("applying migrations: %w", err)
	}
	return conn, nil
}

// noFKMarker on a migration's first line runs it with foreign key
// enforcement off (needed to rebuild a table that other tables reference),
// then checks that no references were broken before committing.
const noFKMarker = "-- fedigest:foreign-keys-off"

func migrate(db *sql.DB) error {
	ctx := context.Background()
	// PRAGMA foreign_keys is per connection, so pin one.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return err
	}

	if err := checkSquash(ctx, conn); err != nil {
		return err
	}

	entries, err := fs.ReadDir(embeddedMigrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}

		sqlBytes, err := embeddedMigrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, conn, name, string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, name, body string) error {
	noFK := strings.HasPrefix(body, noFKMarker)
	if noFK {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	if noFK {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			return err
		}
		broken := rows.Next()
		rows.Close()
		if broken {
			return fmt.Errorf("migration %s: leaves broken foreign key references", name)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (filename) VALUES (?)`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// squashedMigrations were combined into 0001_init.sql before the first
// release. A database that already ran 0001 under the old scheme has the
// combined schema only if it ran all of them.
var squashedMigrations = []string{
	"0002_pick_comment_health.sql",
	"0003_sources_ingest_fields.sql",
	"0004_pick_original_author_community.sql",
	"0005_source_watch_type.sql",
	"0006_site_settings_auto_post_label.sql",
	"0007_dark_default_theme.sql",
	"0008_rename_default_theme_light.sql",
	"0009_pick_link_url.sql",
	"0010_pick_original_published_at.sql",
	"0011_category_show_on_frontpage.sql",
	"0012_pick_original_ap_id.sql",
	"0013_pick_community_post_url.sql",
	"0014_pick_excerpt_auto.sql",
	"0015_pick_auto_ingested.sql",
	"0016_pick_pinned.sql",
	"0017_source_watch_instance.sql",
	"0018_pick_publish_at.sql",
	"0019_pick_views.sql",
	"0020_multi_category.sql",
	"0021_source_ingest_since.sql",
	"0022_theme_terminal.sql",
	"0023_visitor_themes.sql",
	"0024_custom_themes_privacy.sql",
	"0025_amoled_builtin.sql",
	"0026_auto_amoled.sql",
	"0027_drop_auto_amoled.sql",
	"0028_theme_modes.sql",
}

// checkSquash forgets the old migration names on a database that ran them
// all, and refuses one that stopped partway (its schema is incomplete).
func checkSquash(ctx context.Context, conn *sql.Conn) error {
	ran := 0
	for _, name := range squashedMigrations {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&n); err != nil {
			return err
		}
		ran += n
	}
	switch {
	case ran == 0:
		return nil
	case ran < len(squashedMigrations):
		return fmt.Errorf("this database is from an older Fedigest test version that is missing schema updates; start a fresh database")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range squashedMigrations {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE filename = ?`, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
