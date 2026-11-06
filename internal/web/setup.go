package web

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/auth"
	"fedigest/internal/comments"
	"fedigest/internal/config"
	"fedigest/internal/models"
)

// Version is the release, set at build time from the VERSION file (see the
// Dockerfile); "dev" for other builds.
var Version = "dev"

// New builds a Server on an open, migrated database: its stores, the
// comment service and the parsed themes under themesRoot.
func New(cfg *config.Config, conn *sql.DB, themesRoot string, registry *adapters.Registry) (*Server, error) {
	users := &models.UserStore{DB: conn}
	picks := &models.PickStore{DB: conn}
	s := &Server{
		Cfg:            cfg,
		DB:             conn,
		Auth:           auth.NewService(conn, users),
		Users:          users,
		Categories:     &models.CategoryStore{DB: conn},
		Picks:          picks,
		Sources:        &models.SourceStore{DB: conn},
		SiteSettings:   &models.SiteSettingsStore{DB: conn},
		Sidebar:        &models.SidebarBlockStore{DB: conn},
		PasswordResets: &models.PasswordResetStore{DB: conn},
		CustomThemes:   &models.CustomThemeStore{DB: conn},
		ThemeModes:     &models.ThemeModeStore{DB: conn},
		Adapters:       registry,
		Comments:       comments.New(picks, registry, time.Duration(cfg.CommentCacheTTL)*time.Second),
	}
	themes, dirs, err := loadThemes(themesRoot, s.funcMap())
	if err != nil {
		return nil, fmt.Errorf("loading themes: %w", err)
	}
	s.Templates, s.Themes, s.ThemeDirs = themes[baseTheme], themes, dirs
	return s, nil
}

func (s *Server) funcMap() template.FuncMap {
	return template.FuncMap{
		// authorURL is a curator's public author page.
		"authorURL": func(u *models.User) string { return "/author/" + url.PathEscape(u.Username) },
		// categoryURL is a category's page.
		"categoryURL": func(c *models.Category) string { return "/c/" + url.PathEscape(c.Slug) },
		// sourceURL is where this site's source code is (footer link).
		"sourceURL": func() string { return s.Cfg.SourceURL },
		// version is "v0.1.1" for a release build, "dev" otherwise.
		"version": func() string {
			if Version == "dev" {
				return Version
			}
			return "v" + Version
		},
		// adminHead builds admin_head's argument: page title and whether to
		// show the admin-only nav links.
		"adminHead": func(title string, isAdmin bool) map[string]any {
			return map[string]any{"Title": title, "IsAdmin": isAdmin}
		},
		// siteIcon and userAvatar fall back to the bundled default icon when
		// unset or when the stored path no longer resolves to a file, so a
		// stale path never renders as a broken image.
		"siteIcon": func() string {
			ss, err := s.SiteSettings.Get()
			if err == nil && ss.IconPath.Valid && ss.IconPath.String != "" && uploadExists(s.Cfg.UploadsDir, ss.IconPath.String) {
				return "/uploads/" + ss.IconPath.String
			}
			return "/static/default-icon.png"
		},
		"userAvatar": func(u *models.User) string {
			if u != nil && u.AvatarUploadPath.Valid && u.AvatarUploadPath.String != "" && uploadExists(s.Cfg.UploadsDir, u.AvatarUploadPath.String) {
				return "/uploads/" + u.AvatarUploadPath.String
			}
			return "/static/default-icon.png"
		},
		// safeHTML is for sidebar blocks: admin-authored markdown already
		// rendered to HTML at save time, not visitor input.
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
		// excerptHTML renders a card excerpt (see models.ExcerptHTML, which
		// escapes its output, hence template.HTML).
		"excerptHTML": func(s string) template.HTML {
			h, err := models.ExcerptHTML(s)
			if err != nil {
				return template.HTML(template.HTMLEscapeString(s))
			}
			return template.HTML(h)
		},
		// markdown renders pick bodies and fetched comments at request time.
		// RenderMarkdown escapes raw HTML, which matters because this
		// includes untrusted federated content.
		"markdown": func(s string) template.HTML {
			html, err := models.RenderMarkdown(s)
			if err != nil {
				return template.HTML(template.HTMLEscapeString(s))
			}
			return template.HTML(html)
		},
		// autoPostLabel is the byline text shown on auto-ingested picks in
		// place of a curator.
		"autoPostLabel": s.autoPostLabel,
		// autoPostsURL is the auto-post label's page (every auto post).
		"autoPostsURL":  func() string { return autoPostsPath },
		"multiCategory": s.SiteSettings.MultiCategory,
		// themeLabel turns a theme's name into its display name.
		"themeLabel": themeLabel,
		// inSet reports whether id is in a map[int64]bool; a missing map
		// (page data without it) counts as empty.
		"inSet": func(set any, id int64) bool {
			m, ok := set.(map[int64]bool)
			return ok && m[id]
		},
		// uploadFileExists lets a template skip a preview whose file is gone.
		"uploadFileExists": func(relPath string) bool {
			return uploadExists(s.Cfg.UploadsDir, relPath)
		},
		// add/sub are for pagination links; html/template has no arithmetic.
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// plural formats a count with the right noun: "1 comment", "2 comments".
		"plural": func(n any, one, many string) string {
			v := fmt.Sprint(n)
			if v == "1" {
				return v + " " + one
			}
			return v + " " + many
		},
		// timeAgo is how long ago t was, roughly: "3 minutes ago".
		"timeAgo": timeAgo,
		// isoTime turns a stored SQLite time ("2026-09-23 10:00:00", UTC)
		// into RFC 3339 for <time datetime>.
		"isoTime": func(v string) string {
			if t, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
				return t.Format(time.RFC3339)
			}
			return v
		},
		// staticAssetVersion is a content hash (?v=) for the embedded
		// admin assets' URLs, for cache busting.
		"staticAssetVersion": staticAssetVersion,
	}
}

// timeAgo describes how long ago t was, in the largest whole unit.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	unit := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one + " ago"
		}
		return strconv.Itoa(n) + " " + many + " ago"
	}
	switch {
	case t.IsZero():
		return "a while ago"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return unit(int(d/time.Minute), "minute", "minutes")
	case d < 48*time.Hour:
		return unit(int(d/time.Hour), "hour", "hours")
	default:
		return unit(int(d/(24*time.Hour)), "day", "days")
	}
}

// uploadExists reports whether relPath resolves to a regular file under
// uploadsDir; a DB row can outlive its upload.
func uploadExists(uploadsDir, relPath string) bool {
	info, err := os.Stat(filepath.Join(uploadsDir, relPath))
	return err == nil && !info.IsDir()
}

// contentHash returns a short cache-busting token (not a security hash).
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:10]
}

var (
	staticAssetVersionOnce   sync.Once
	staticAssetVersionCached string
)

// staticAssetVersion hashes the embedded admin.css and theme-editor.js
// once, for their ?v= cache-busting parameter.
func staticAssetVersion() string {
	staticAssetVersionOnce.Do(func() {
		var all []byte
		for _, name := range []string{"admin.css", "theme-editor.js"} {
			data, err := StaticAssetBytes(name)
			if err != nil {
				staticAssetVersionCached = "0"
				return
			}
			all = append(all, data...)
		}
		staticAssetVersionCached = contentHash(all)
	})
	return staticAssetVersionCached
}
