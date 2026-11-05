package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// headFor runs applyTheme for a request carrying the given cookies.
func headFor(s *Server, cookies map[string]string) seoHead {
	r := httptest.NewRequest("GET", "/", nil)
	for k, v := range cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	var h seoHead
	s.applyTheme(r, &h)
	return h
}

func sheets(h seoHead) string {
	var out []string
	for _, sh := range h.ThemeSheets {
		name := strings.TrimPrefix(sh.Href, "/themes/")
		name = name[:strings.Index(name, "/")]
		if strings.Contains(sh.Href, "black=1") {
			name += "+black"
		}
		if sh.Media != "" {
			name += " [" + sh.Media + "]"
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func TestApplyTheme(t *testing.T) {
	s := newTestSite(t).srv
	cases := []struct {
		name     string
		cookies  map[string]string
		want     string
		hasBlack bool
	}{
		{"site default follows device", nil,
			"light [not all and (prefers-color-scheme: dark)], dark [(prefers-color-scheme: dark)]", true},
		{"dark theme, light partner", map[string]string{"fedigest_theme": "sepia-dark"},
			"sepia [(prefers-color-scheme: light)], sepia-dark [not all and (prefers-color-scheme: light)]", true},
		{"always light", map[string]string{"fedigest_theme": "light!"}, "light", false},
		{"always dark", map[string]string{"fedigest_theme": "dark!"}, "dark", true},
		{"pure black forces the dark partner", map[string]string{"fedigest_black": "1"}, "dark+black", true},
		{"pure black on an always-dark theme", map[string]string{"fedigest_theme": "nord!", "fedigest_black": "1"}, "nord+black", true},
		{"pure black ignored when always light", map[string]string{"fedigest_theme": "sepia!", "fedigest_black": "1"}, "sepia", false},
		{"fixed theme", map[string]string{"fedigest_theme": "high-contrast", "fedigest_black": "1"}, "high-contrast", false},
		{"unknown theme falls back", map[string]string{"fedigest_theme": "nope"},
			"light [not all and (prefers-color-scheme: dark)], dark [(prefers-color-scheme: dark)]", true},
	}
	for _, c := range cases {
		h := headFor(s, c.cookies)
		if got := sheets(h); got != c.want {
			t.Errorf("%s: sheets %q, want %q", c.name, got, c.want)
		}
		if h.ThemeHasDark != c.hasBlack {
			t.Errorf("%s: pure black checkbox shown = %v", c.name, h.ThemeHasDark)
		}
	}
}

func TestThemeMenu(t *testing.T) {
	s := newTestSite(t).srv
	h := headFor(s, map[string]string{"fedigest_theme": "dark"})
	var groups []string
	values := map[string]string{}
	selected := ""
	for _, o := range h.ThemeOptions {
		if o.GroupStart {
			groups = append(groups, o.Group)
		}
		values[o.Name] = o.Label
		if o.Selected {
			selected = o.Name
		}
	}
	if !strings.Contains(strings.Join(groups, "|"), "Light / Dark|") {
		t.Errorf("groups %v: want a Light / Dark group", groups)
	}
	for v, label := range map[string]string{"light": "Light / Dark (auto)", "light!": "Light", "dark!": "Dark",
		"dim": "Dim (auto, Light in light mode)", "high-contrast": "High contrast"} {
		if values[v] != label {
			t.Errorf("option %q: label %q, want %q", v, values[v], label)
		}
	}
	if _, ok := values["dark"]; ok {
		t.Error("the dark half of a pair shouldn't have its own auto entry")
	}
	if selected != "light" {
		t.Errorf("choosing dark (auto) should select the pair's entry, got %q", selected)
	}
}

func TestVisitorThemeRestriction(t *testing.T) {
	s := newTestSite(t).srv
	if err := s.SiteSettings.UpdateVisitorThemes("sepia"); err != nil {
		t.Fatal(err)
	}
	if name, _ := s.validThemeChoice("nord!"); name != "" {
		t.Error("a theme not offered to visitors was accepted")
	}
	if name, forced := s.validThemeChoice("sepia!"); name != "sepia" || !forced {
		t.Errorf("sepia!: got %q forced=%v", name, forced)
	}
	if name, _ := s.validThemeChoice("light"); name != "light" {
		t.Error("the site theme is always allowed")
	}
}

func TestThemeChoiceHandler(t *testing.T) {
	s := newTestSite(t).srv
	do := func(q url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.HandleThemeChoice(w, httptest.NewRequest("GET", "/theme-choice?"+q.Encode(), nil))
		return w
	}
	cookie := func(w *httptest.ResponseRecorder, name string) *http.Cookie {
		for _, c := range w.Result().Cookies() {
			if c.Name == name {
				return c
			}
		}
		return nil
	}
	w := do(url.Values{"theme": {"sepia!"}, "back": {"/c/news?page=2"}, "has_black": {"1"}, "black": {"1"}})
	if c := cookie(w, themeCookie); c == nil || c.Value != "sepia!" || !c.HttpOnly {
		t.Errorf("theme cookie: %+v", c)
	}
	if c := cookie(w, blackCookie); c == nil || c.Value != "1" {
		t.Errorf("black cookie: %+v", c)
	}
	if loc := w.Header().Get("Location"); loc != "/c/news?page=2" {
		t.Errorf("redirect to %q", loc)
	}
	// Without has_black the black choice is left alone.
	if c := cookie(do(url.Values{"theme": {"light"}}), blackCookie); c != nil {
		t.Error("black cookie changed by a form without the checkbox")
	}
	// Unticked: cleared.
	if c := cookie(do(url.Values{"theme": {"light"}, "has_black": {"1"}}), blackCookie); c == nil || c.MaxAge >= 0 {
		t.Error("unticking should delete the black cookie")
	}
	for _, back := range []string{"//evil.example", "https://evil.example/", "/\\evil.example"} {
		if loc := do(url.Values{"theme": {"light"}, "back": {back}}).Header().Get("Location"); loc != "/" {
			t.Errorf("back=%q redirected to %q", back, loc)
		}
	}
}

func TestBlackRoot(t *testing.T) {
	in := ":root {\n\t--color-bg: #fff;\n\t--color-text: #000;\n}\n.x { color: red; }\n:root { --color-surface: #eee; }"
	out := string(blackRoot([]byte(in)))
	if !strings.Contains(out, "--color-bg: #000000;") || !strings.Contains(out, "--color-surface: #000000;") {
		t.Errorf("first :root not blacked out:\n%s", out)
	}
	if !strings.Contains(out, ":root { --color-surface: #eee; }") {
		t.Error("only the first :root block should change")
	}
	if got := string(blackRoot([]byte(".x{}"))); got != ".x{}" {
		t.Error("css without :root should be unchanged")
	}
}

func TestCustomThemeLifecycle(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	if resp := ts.post(admin, "/admin/themes/new", url.Values{"name": {"mine"}, "from": {"terminal-light"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	th, err := ts.srv.CustomThemes.Get("mine")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(th.CSS, "/themes/terminal-light/fonts/") {
		t.Error("copied font URL not made absolute")
	}
	if m := ts.srv.themeMode("mine"); m.Mode != "light" || m.Partner != "terminal" {
		t.Errorf("copy should start with its source's pairing, got %+v", m)
	}
	// Pair another theme with it, make it the site theme, then rename it.
	if err := ts.srv.ThemeModes.Set("terminal", "dark", "mine"); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.SiteSettings.UpdateTheme("mine"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"name": {"mine2"}, "css": {th.CSS}, "theme_mode": {"light"}, "partner_dark": {"dark"}}
	if resp := ts.post(admin, "/admin/themes/mine/edit", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("rename: status %d", resp.StatusCode)
	}
	if ts.srv.currentThemeName() != "mine2" {
		t.Error("site theme not renamed")
	}
	if m := ts.srv.themeMode("terminal"); m.Partner != "mine2" {
		t.Errorf("partner reference not renamed: %+v", m)
	}
	if m := ts.srv.themeMode("mine2"); m.Partner != "dark" {
		t.Errorf("pairing not saved: %+v", m)
	}
	// Can't delete the site theme; after switching away, delete clears references.
	ts.post(admin, "/admin/themes/mine2/delete", nil)
	if ts.srv.customTheme("mine2") == nil {
		t.Fatal("site theme was deleted")
	}
	ts.srv.SiteSettings.UpdateTheme("light")
	ts.post(admin, "/admin/themes/mine2/delete", nil)
	if ts.srv.customTheme("mine2") != nil {
		t.Fatal("theme not deleted")
	}
	if m := ts.srv.themeMode("terminal"); m.Partner != "" {
		t.Errorf("partner reference to a deleted theme kept: %+v", m)
	}
	// Built-in names and bad names are refused.
	for _, name := range []string{"dark", "Bad Name", "../x"} {
		ts.post(admin, "/admin/themes/new", url.Values{"name": {name}, "from": {"light"}})
		if th, _ := ts.srv.CustomThemes.Get(name); th != nil {
			t.Errorf("custom theme %q was created", name)
		}
	}
}

func TestBuiltInThemePairingEdit(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	ts.post(admin, "/admin/themes/sepia/edit", url.Values{"theme_mode": {"fixed"}})
	if m := ts.srv.themeMode("sepia"); m.Mode != "fixed" || m.Partner != "" {
		t.Errorf("sepia: %+v", m)
	}
	ts.post(admin, "/admin/themes/sepia/edit", url.Values{"theme_mode": {"light"}, "partner_dark": {"nope"}})
	if m := ts.srv.themeMode("sepia"); m.Mode != "light" || m.Partner != "" {
		t.Errorf("unknown partner should be dropped: %+v", m)
	}
}

func TestColorSchemeMeta(t *testing.T) {
	s := newTestSite(t).srv
	for cookie, want := range map[string]string{"": "light dark", "light!": "light", "dark!": "dark",
		"sepia": "light dark", "high-contrast": "dark"} {
		c := map[string]string{}
		if cookie != "" {
			c["fedigest_theme"] = cookie
		}
		if got := headFor(s, c).ColorScheme; got != want {
			t.Errorf("theme %q: color-scheme %q, want %q", cookie, got, want)
		}
	}
	if got := headFor(s, map[string]string{"fedigest_black": "1"}).ColorScheme; got != "dark" {
		t.Errorf("pure black: %q", got)
	}
}
