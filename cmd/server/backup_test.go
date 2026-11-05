package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"fedigest/internal/config"
	"fedigest/internal/db"
)

func TestBackupArchive(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, UploadsDir: filepath.Join(dir, "uploads"), DBPath: filepath.Join(dir, "fedigest.db")}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	os.WriteFile(filepath.Join(dir, "session_secret"), []byte("abcd"), 0o600)
	os.MkdirAll(filepath.Join(cfg.UploadsDir, "picks"), 0o755)
	os.WriteFile(filepath.Join(cfg.UploadsDir, "picks", "1.png"), []byte("png"), 0o644)

	dest := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := runBackup(cfg, conn, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %v: it holds the session secret, so only its owner may read it", info.Mode().Perm())
	}
	f, _ := os.Open(dest)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "fedigest.db,session_secret,uploads/picks/1.png" {
		t.Errorf("archive holds %s", got)
	}
	if left, _ := filepath.Glob(dest + ".*"); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
