package web

import (
	"database/sql"
	"net/http"
	"strings"
)

// HandleAdminSettings shows the site-settings form.
func (s *Server) HandleAdminSettings(w http.ResponseWriter, r *http.Request) {
	ss, err := s.SiteSettings.Get()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "admin_settings.html", map[string]any{
		"Settings":       ss,
		"Themes":         s.allThemeNames(),
		"VisitorThemes":  s.visitorThemeSet(),
		"PrivacyContact": s.privacyContact(),
	})
}

// visitorThemeSet is the visitor_themes setting as a set of theme names.
func (s *Server) visitorThemeSet() map[string]bool {
	set := map[string]bool{}
	for _, n := range s.visitorThemes() {
		set[n] = true
	}
	return set
}

func (s *Server) HandleAdminSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if !parseUploadForm(w, r, maxPickFormBytes) {
		return
	}

	siteName := strings.TrimSpace(r.FormValue("site_name"))
	theme := strings.TrimSpace(r.FormValue("theme"))
	autoPostLabel := strings.TrimSpace(r.FormValue("auto_post_label"))

	formErr := func(msg string) {
		ss, _ := s.SiteSettings.Get()
		s.render(w, "admin_settings.html", map[string]any{
			"Settings":       ss,
			"Themes":         s.allThemeNames(),
			"VisitorThemes":  s.visitorThemeSet(),
			"PrivacyContact": r.FormValue("privacy_contact"),
			"Error":          msg,
		})
	}

	if siteName == "" {
		formErr("Site name is required.")
		return
	}
	if !s.themeExists(theme) {
		formErr("Choose a valid theme.")
		return
	}
	if autoPostLabel == "" {
		formErr("Auto post label is required.")
		return
	}

	if err := s.SiteSettings.UpdateSiteName(siteName); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.SiteSettings.UpdateTheme(theme); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.SiteSettings.UpdateAutoPostLabel(autoPostLabel); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.SiteSettings.UpdateMultiCategory(r.FormValue("multi_category") == "on"); err != nil {
		s.serverError(w, err)
		return
	}
	// Every theme ticked is stored as "*" so themes added later are
	// offered too.
	var picked []string
	for _, n := range r.Form["visitor_theme"] {
		if s.themeExists(n) {
			picked = append(picked, n)
		}
	}
	visitor := strings.Join(picked, ",")
	if len(picked) == len(s.allThemeNames()) {
		visitor = "*"
	}
	if err := s.SiteSettings.UpdateVisitorThemes(visitor); err != nil {
		s.serverError(w, err)
		return
	}

	if err := s.SiteSettings.UpdatePrivacyContact(strings.TrimSpace(r.FormValue("privacy_contact"))); err != nil {
		s.serverError(w, err)
		return
	}

	var oldIcon string
	if ss, err := s.SiteSettings.Get(); err == nil && ss.IconPath.Valid {
		oldIcon = ss.IconPath.String
	}
	if file, _, err := r.FormFile("icon_upload"); err == nil {
		defer file.Close()
		savedPath, err := saveUploadedImage(s.Cfg.UploadsDir, "site", file)
		if err != nil {
			formErr("Could not save uploaded icon: " + err.Error())
			return
		}
		if err := s.SiteSettings.UpdateIconPath(sql.NullString{String: savedPath, Valid: true}); err != nil {
			s.serverError(w, err)
			return
		}
		removeUpload(s.Cfg.UploadsDir, oldIcon)
	} else if r.FormValue("remove_icon") == "on" {
		if err := s.SiteSettings.UpdateIconPath(sql.NullString{}); err != nil {
			s.serverError(w, err)
			return
		}
		removeUpload(s.Cfg.UploadsDir, oldIcon)
	}

	http.Redirect(w, r, "/admin/settings", http.StatusFound)
}

// privacyContact is the stored contact line for the privacy page, or "".
func (s *Server) privacyContact() string {
	if ss, err := s.SiteSettings.Get(); err == nil && ss.PrivacyContact.Valid {
		return ss.PrivacyContact.String
	}
	return ""
}
