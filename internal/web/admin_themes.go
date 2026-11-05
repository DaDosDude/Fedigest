package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"fedigest/internal/models"
)

type themeRow struct {
	Name    string
	Custom  bool
	BasedOn string
	Site    bool
	Mode    string
	Partner string
}

// HandleAdminThemes lists built-in and custom themes, with a form to
// create a custom theme from any existing one.
func (s *Server) HandleAdminThemes(w http.ResponseWriter, r *http.Request) {
	s.renderThemes(w, "", "")
}

func (s *Server) renderThemes(w http.ResponseWriter, errMsg, name string) {
	site := s.currentThemeName()
	var rows []themeRow
	for _, n := range s.allThemeNames() {
		m := s.themeMode(n)
		row := themeRow{Name: n, Site: n == site, Mode: m.Mode, Partner: m.Partner}
		if t := s.customTheme(n); t != nil {
			if _, builtIn := s.Themes[n]; !builtIn {
				row.Custom = true
				row.BasedOn = t.BasedOn.String
			}
		}
		rows = append(rows, row)
	}
	s.render(w, "admin_themes.html", map[string]any{
		"Themes": rows,
		"Error":  errMsg,
		"Name":   name,
	})
}

// HandleAdminThemeCreate copies an existing theme's stylesheet into a new
// custom theme and opens it for editing.
func (s *Server) HandleAdminThemeCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	from := r.FormValue("from")
	if !models.ThemeNamePattern.MatchString(name) {
		s.renderThemes(w, "Use 1–40 lowercase letters, digits and dashes for the name, starting with a letter or digit.", name)
		return
	}
	if s.themeExists(name) {
		s.renderThemes(w, "A theme called "+name+" already exists.", name)
		return
	}
	css, ok := s.themeCSS(from)
	if !ok {
		s.renderThemes(w, "Choose a theme to start from.", name)
		return
	}
	// The copy lives at another URL, so point relative font/image paths
	// back at the original theme's files.
	text := string(css)
	if _, builtIn := s.ThemeDirs[from]; builtIn {
		for _, q := range []string{`url("`, `url('`, `url(`} {
			text = strings.ReplaceAll(text, q+"fonts/", q+"/themes/"+from+"/fonts/")
		}
	}
	if err := s.CustomThemes.Create(name, text, from); err != nil {
		if errors.Is(err, models.ErrThemeExists) {
			s.renderThemes(w, "A theme called "+name+" already exists.", name)
			return
		}
		s.serverError(w, err)
		return
	}
	// The copy starts as the same kind of theme, with the same partner.
	if m := s.themeMode(from); s.ThemeModes != nil {
		if err := s.ThemeModes.Set(name, m.Mode, m.Partner); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/admin/themes/"+name+"/edit", http.StatusFound)
}

// themeFormData is what the edit page needs besides the theme itself.
func (s *Server) themeFormData(name string, m models.ThemeMode) map[string]any {
	var lights, darks []string
	for _, n := range s.allThemeNames() {
		if n == name {
			continue
		}
		switch s.themeMode(n).Mode {
		case "light":
			lights = append(lights, n)
		case "dark":
			darks = append(darks, n)
		}
	}
	data := map[string]any{"Mode": m, "LightThemes": lights, "DarkThemes": darks}
	// The live preview offers the newest post as one of its pages.
	if picks, err := s.Picks.ListFrontpage(1); err == nil && len(picks) > 0 {
		data["PreviewPost"] = fmt.Sprintf("/p/%d-%s", picks[0].ID, picks[0].Slug)
	}
	return data
}

// HandleAdminThemeEditForm edits a custom theme (name, look, light/dark
// pairing) or a built-in one (pairing only).
func (s *Server) HandleAdminThemeEditForm(w http.ResponseWriter, r *http.Request, name string) {
	_, builtIn := s.Themes[name]
	t := s.customTheme(name)
	if !builtIn && t == nil {
		http.NotFound(w, r)
		return
	}
	if builtIn {
		t = &models.CustomTheme{Name: name}
	}
	data := s.themeFormData(name, s.themeMode(name))
	data["Theme"] = t
	data["Name"] = name
	data["BuiltIn"] = builtIn
	data["Saved"] = r.URL.Query().Get("saved") == "1"
	s.render(w, "admin_theme_form.html", data)
}

// formThemeMode reads the light/dark/fixed choice and the matching partner.
func (s *Server) formThemeMode(r *http.Request, name string) models.ThemeMode {
	m := models.ThemeMode{Name: name, Mode: r.FormValue("theme_mode")}
	switch m.Mode {
	case "light":
		m.Partner = r.FormValue("partner_dark")
	case "dark":
		m.Partner = r.FormValue("partner_light")
	default:
		m.Mode = "fixed"
	}
	// A partner must exist and be of the other kind.
	if other := map[string]string{"light": "dark", "dark": "light"}[m.Mode]; m.Partner == name || !s.themeExists(m.Partner) || s.themeMode(m.Partner).Mode != other {
		m.Partner = ""
	}
	return m
}

func (s *Server) HandleAdminThemeUpdate(w http.ResponseWriter, r *http.Request, name string) {
	_, builtIn := s.Themes[name]
	t := s.customTheme(name)
	if !builtIn && t == nil {
		http.NotFound(w, r)
		return
	}
	m := s.formThemeMode(r, name)
	if builtIn {
		if err := s.ThemeModes.Set(name, m.Mode, m.Partner); err != nil {
			s.serverError(w, err)
			return
		}
		http.Redirect(w, r, "/admin/themes/"+name+"/edit?saved=1", http.StatusFound)
		return
	}
	css := strings.ReplaceAll(r.FormValue("css"), "\r\n", "\n")
	if strings.Contains(strings.ToLower(css), "</style") {
		http.Error(w, "invalid stylesheet", http.StatusBadRequest)
		return
	}
	newName := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	if newName == "" {
		newName = name
	}
	formErr := func(msg string) {
		t.CSS = css
		data := s.themeFormData(name, m)
		data["Theme"] = t
		data["Name"] = newName
		data["Error"] = msg
		s.render(w, "admin_theme_form.html", data)
	}
	if newName != name {
		if !models.ThemeNamePattern.MatchString(newName) {
			formErr("Use 1–40 lowercase letters, digits and dashes for the name, starting with a letter or digit.")
			return
		}
		if s.themeExists(newName) {
			formErr("A theme called " + newName + " already exists.")
			return
		}
	}
	if err := s.CustomThemes.UpdateCSS(name, css); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.ThemeModes.Set(name, m.Mode, m.Partner); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.CustomThemes.Rename(name, newName); err != nil {
		if errors.Is(err, models.ErrThemeExists) {
			formErr("A theme called " + newName + " already exists.")
			return
		}
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/themes/"+newName+"/edit?saved=1", http.StatusFound)
}

// HandleAdminThemeDelete removes a custom theme; the site theme can't be
// deleted while it's in use.
func (s *Server) HandleAdminThemeDelete(w http.ResponseWriter, r *http.Request, name string) {
	if s.customTheme(name) == nil {
		http.NotFound(w, r)
		return
	}
	if name == s.currentThemeName() {
		s.renderThemes(w, "That's the site theme. Pick another site theme in Site settings first.", "")
		return
	}
	if err := s.CustomThemes.Delete(name); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/themes", http.StatusFound)
}
