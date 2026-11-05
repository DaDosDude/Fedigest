// Package config loads Fedigest's runtime configuration from the
// environment. The database path is fixed (DataDir/fedigest.db) and the
// session secret is generated on first boot into DataDir/session_secret.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all runtime configuration for the server.
type Config struct {
	DataDir    string // e.g. /app/data — holds the SQLite DB and session secret
	UploadsDir string // e.g. /app/uploads
	DBPath     string // DataDir/fedigest.db

	SiteName        string
	SiteDomain      string
	CommentCacheTTL int // seconds
	ListenAddr      string
	AdminUsername   string // used only for the first-run admin account
	// SourceURL is where visitors can get this site's source code, linked
	// as "Source" in the footer (AGPL-3.0 section 13).
	SourceURL string

	// SMTP is optional. If Host is empty, email-based password reset is
	// disabled and admins must reset passwords manually instead.
	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string

	// SessionSecret is generated on first boot, not read from the environment.
	SessionSecret []byte
}

const sessionSecretFile = "session_secret"

// DefaultSourceURL is the official repository, the footer's "Source" link
// unless SOURCE_URL points elsewhere (a modified version's own code).
const DefaultSourceURL = "https://github.com/DaDosDude/Fedigest"

// Load reads configuration from the environment, creating the data and
// uploads directories and the session secret if needed.
func Load() (*Config, error) {
	dataDir := envOr("FEDIGEST_DATA_DIR", "/app/data") // internal-only override, not documented in .env.example
	uploadsDir := envOr("UPLOADS_DIR", "/app/uploads")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating uploads dir: %w", err)
	}

	secret, err := loadOrCreateSessionSecret(dataDir)
	if err != nil {
		return nil, fmt.Errorf("session secret: %w", err)
	}

	ttl, err := strconv.Atoi(envOr("COMMENT_CACHE_TTL", "60"))
	if err != nil || ttl < 1 {
		return nil, fmt.Errorf("COMMENT_CACHE_TTL must be a whole number of seconds, at least 1 (got %q)", os.Getenv("COMMENT_CACHE_TTL"))
	}

	smtpPort, err := strconv.Atoi(envOr("SMTP_PORT", "587"))
	if err != nil || smtpPort < 1 || smtpPort > 65535 {
		return nil, fmt.Errorf("SMTP_PORT must be a port number (got %q)", os.Getenv("SMTP_PORT"))
	}

	sourceURL := envOr("SOURCE_URL", DefaultSourceURL)
	if u, err := url.Parse(sourceURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("SOURCE_URL must be a web address starting with https:// (got %q)", sourceURL)
	}

	cfg := &Config{
		DataDir:         dataDir,
		UploadsDir:      uploadsDir,
		DBPath:          filepath.Join(dataDir, "fedigest.db"),
		SiteName:        envOr("SITE_NAME", "Fedigest"),
		SiteDomain:      os.Getenv("SITE_DOMAIN"),
		CommentCacheTTL: ttl,
		ListenAddr:      envOr("LISTEN_ADDR", ":8080"),
		AdminUsername:   envOr("ADMIN_USERNAME", "admin"),
		SourceURL:       sourceURL,
		SMTPHost:        os.Getenv("SMTP_HOST"),
		SMTPPort:        smtpPort,
		SMTPUser:        os.Getenv("SMTP_USER"),
		SMTPPass:        os.Getenv("SMTP_PASS"),
		SMTPFrom:        os.Getenv("SMTP_FROM"),
		SessionSecret:   secret,
	}
	return cfg, nil
}

// EmailEnabled reports whether self-service password reset can be offered:
// it needs SMTP, and SITE_DOMAIN so reset links never take their host from
// the request.
func (c *Config) EmailEnabled() bool {
	return c.SMTPHost != "" && c.SiteDomain != ""
}

func loadOrCreateSessionSecret(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, sessionSecretFile)

	if raw, err := os.ReadFile(path); err == nil {
		secret, decodeErr := hex.DecodeString(string(raw))
		if decodeErr != nil {
			return nil, fmt.Errorf("existing session secret file is corrupt: %w", decodeErr)
		}
		return secret, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating session secret: %w", err)
	}

	encoded := hex.EncodeToString(secret)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("writing session secret: %w", err)
	}
	return secret, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
