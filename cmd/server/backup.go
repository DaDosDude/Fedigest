package main

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"fedigest/internal/config"
)

// runBackup writes a .tar.gz with everything Fedigest needs to be restored:
// a consistent snapshot of the database (SQLite VACUUM INTO, safe while
// the server is running), the session secret, and the uploads directory.
//
//	docker exec fedigest ./fedigest backup /app/data/fedigest-backup.tar.gz
func runBackup(cfg *config.Config, conn *sql.DB, dest string) error {
	snapshot := dest + ".db.tmp"
	os.Remove(snapshot)
	if _, err := conn.Exec(`VACUUM INTO ?`, snapshot); err != nil {
		return fmt.Errorf("database snapshot: %w", err)
	}
	defer os.Remove(snapshot)
	// The database and session secret let anyone take over logged-in
	// sessions, so only the owner may read them.
	if err := os.Chmod(snapshot, 0o600); err != nil {
		return err
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(tmp)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, snapshot, "fedigest.db"); err != nil {
		return err
	}
	secret := filepath.Join(filepath.Dir(cfg.DBPath), "session_secret")
	if _, err := os.Stat(secret); err == nil {
		if err := addFile(tw, secret, "session_secret"); err != nil {
			return err
		}
	}
	err = filepath.WalkDir(cfg.UploadsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(cfg.UploadsDir, path)
		if err != nil {
			return err
		}
		return addFile(tw, path, filepath.ToSlash(filepath.Join("uploads", rel)))
	})
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("uploads: %w", err)
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func addFile(tw *tar.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, src)
	return err
}
