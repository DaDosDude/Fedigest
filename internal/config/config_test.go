package config

import "testing"

func TestLoadValidates(t *testing.T) {
	t.Setenv("FEDIGEST_DATA_DIR", t.TempDir())
	t.Setenv("UPLOADS_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTPPort != 587 || cfg.CommentCacheTTL != 60 || cfg.SourceURL != DefaultSourceURL {
		t.Errorf("defaults: SMTP port %d, TTL %d, source %q", cfg.SMTPPort, cfg.CommentCacheTTL, cfg.SourceURL)
	}
	t.Setenv("SOURCE_URL", "https://git.example/me/fedigest-fork")
	if cfg, err := Load(); err != nil || cfg.SourceURL != "https://git.example/me/fedigest-fork" {
		t.Errorf("SOURCE_URL: %v, %v", cfg, err)
	}
	for _, bad := range []struct{ key, val string }{{"COMMENT_CACHE_TTL", "0"}, {"COMMENT_CACHE_TTL", "x"}, {"SMTP_PORT", "70000"},
		{"SOURCE_URL", "javascript:alert(1)"}, {"SOURCE_URL", "github.com/x"}} {
		t.Run(bad.key+"="+bad.val, func(t *testing.T) {
			t.Setenv(bad.key, bad.val)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s accepted", bad.key, bad.val)
			}
		})
	}
}
