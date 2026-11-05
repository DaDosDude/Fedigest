package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fedigest/internal/models"
)

// themeCookie remembers a visitor's own theme choice. It stores only the
// theme name and exists only after the visitor picks one.
const themeCookie = "fedigest_theme"

type themeOption struct {
	Name, Label string
	Selected    bool
	// Group is the <optgroup> label of a theme with a light/dark partner;
	// GroupStart/GroupEnd mark its first and last option.
	Group                string
	GroupStart, GroupEnd bool
}

// themeLabel turns "terminal-green" into "Terminal green".
func themeLabel(name string) string {
	l := strings.ReplaceAll(name, "-", " ")
	if l == "" {
		return l
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

// visitorThemes returns the installed themes visitors may pick, sorted;
// the site theme is always included.
func (s *Server) visitorThemes() []string {
	setting := "*"
	if s.SiteSettings != nil {
		if ss, err := s.SiteSettings.Get(); err == nil {
			setting = ss.VisitorThemes
		}
	}
	allowed := map[string]bool{s.currentThemeName(): true}
	if setting == "*" {
		for _, name := range s.allThemeNames() {
			allowed[name] = true
		}
	}
	for _, name := range strings.Split(setting, ",") {
		if name = strings.TrimSpace(name); s.themeExists(name) {
			allowed[name] = true
		}
	}
	out := make([]string, 0, len(allowed))
	for name := range allowed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// forcedSuffix on a theme choice means "always this theme", ignoring its
// light/dark partner.
const forcedSuffix = "!"

// chosenTheme is the visitor's own allowed theme choice ("" for none), and
// whether they chose to always use it rather than follow the device.
func (s *Server) chosenTheme(r *http.Request) (string, bool) {
	c, err := r.Cookie(themeCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	return s.validThemeChoice(c.Value)
}

// validThemeChoice parses a menu value ("dark" or "dark!") against the
// themes visitors may pick.
func (s *Server) validThemeChoice(value string) (string, bool) {
	forced := strings.HasSuffix(value, forcedSuffix)
	value = strings.TrimSuffix(value, forcedSuffix)
	for _, name := range s.visitorThemes() {
		if name == value {
			return name, forced
		}
	}
	return "", false
}

// themeSheet is one stylesheet link; Media is empty for an unpaired theme.
type themeSheet struct {
	Href, Media string
}

// themeModes is a snapshot of every theme's light/dark mode and the
// installed theme names, read once per page rather than once per theme.
type themeModes struct {
	stored    map[string]models.ThemeMode
	installed map[string]bool
}

func (s *Server) loadThemeModes() themeModes {
	t := themeModes{installed: map[string]bool{}}
	if s.ThemeModes != nil {
		t.stored, _ = s.ThemeModes.All()
	}
	for _, n := range s.allThemeNames() {
		t.installed[n] = true
	}
	return t
}

// mode returns a theme's mode, with the partner cleared when it isn't an
// installed theme. Themes without a stored mode are fixed.
func (t themeModes) mode(name string) models.ThemeMode {
	m := models.ThemeMode{Name: name, Mode: "fixed"}
	if stored, ok := t.stored[name]; ok {
		m = stored
	}
	if m.Mode == "fixed" || m.Partner == name || !t.installed[m.Partner] {
		m.Partner = ""
	}
	return m
}

// themeMode returns one theme's mode (see themeModes.mode).
func (s *Server) themeMode(name string) models.ThemeMode {
	return s.loadThemeModes().mode(name)
}

// applyTheme fills the head fields for the stylesheets and theme picker. A
// theme with a partner gets two stylesheets, one per device colour scheme;
// the theme's own sheet also covers "no preference".
func (s *Server) applyTheme(r *http.Request, h *seoHead) {
	site := s.currentThemeName()
	chosen, forced := s.chosenTheme(r)
	theme := site
	if chosen != "" {
		theme = chosen
	}
	h.ThemeBack = r.URL.RequestURI()

	modes := s.loadThemeModes()
	m := modes.mode(theme)
	if forced {
		m.Partner = ""
	}
	h.ThemeHasDark = m.Mode == "dark" || m.Partner != ""
	if h.ThemeHasDark {
		c, err := r.Cookie(blackCookie)
		h.ThemeBlack = err == nil && c.Value == "1"
	}
	sheet := func(name string, dark bool, media string) themeSheet {
		black := dark && h.ThemeBlack
		href := "/themes/" + name + "/style.css?v=" + s.themeCSSVersion(name, black)
		if black {
			href += "&black=1"
		}
		return themeSheet{Href: href, Media: media}
	}
	switch {
	case h.ThemeBlack || (m.Mode == "dark" && m.Partner == "") || (m.Mode == "fixed" && s.themeLooksDark(theme)):
		h.ColorScheme = "dark"
	case m.Partner != "":
		h.ColorScheme = "light dark"
	default:
		h.ColorScheme = "light"
	}
	switch {
	// Pure black always shows the dark theme, whatever the device setting.
	case h.ThemeBlack && m.Mode == "light":
		h.ThemeSheets = []themeSheet{sheet(m.Partner, true, "")}
	case h.ThemeBlack:
		h.ThemeSheets = []themeSheet{sheet(theme, true, "")}
	case m.Partner != "" && m.Mode == "light":
		h.ThemeSheets = []themeSheet{
			sheet(theme, false, "not all and (prefers-color-scheme: dark)"),
			sheet(m.Partner, true, "(prefers-color-scheme: dark)"),
		}
	case m.Partner != "" && m.Mode == "dark":
		h.ThemeSheets = []themeSheet{
			sheet(m.Partner, false, "(prefers-color-scheme: light)"),
			sheet(theme, true, "not all and (prefers-color-scheme: light)"),
		}
	default:
		h.ThemeSheets = []themeSheet{sheet(theme, m.Mode == "dark", "")}
	}

	names := s.visitorThemes()
	if len(names) < 2 {
		return
	}
	chosenValue := chosen
	if forced {
		chosenValue += forcedSuffix
	}
	h.ThemeOptions = append(h.ThemeOptions, themeOption{Name: "", Label: "Site default (" + modes.menuLabel(site) + ")", Selected: chosen == ""})
	allowed := map[string]bool{}
	for _, n := range names {
		allowed[n] = true
	}
	add := func(group, value, label string, also ...string) {
		selected := value == chosenValue
		for _, v := range also {
			selected = selected || v == chosenValue
		}
		h.ThemeOptions = append(h.ThemeOptions, themeOption{Name: value, Label: label, Selected: selected, Group: group})
	}
	for _, n := range names {
		m := modes.mode(n)
		mutual := m.Partner != "" && modes.mode(m.Partner).Partner == n
		// A pair is listed once, under its light theme: following the
		// device, then each theme on its own.
		if mutual && m.Mode == "dark" && allowed[m.Partner] {
			continue
		}
		if m.Partner == "" {
			add("", n, themeLabel(n))
			continue
		}
		group := strings.TrimSuffix(modes.menuLabel(n), " (auto)")
		first := len(h.ThemeOptions)
		if mutual {
			add(group, n, group+" (auto)", m.Partner)
		} else {
			add(group, n, group+" (auto, "+themeLabel(m.Partner)+" in "+map[string]string{"light": "dark", "dark": "light"}[m.Mode]+" mode)")
		}
		add(group, n+forcedSuffix, themeLabel(n))
		if mutual && allowed[m.Partner] {
			add(group, m.Partner+forcedSuffix, themeLabel(m.Partner))
		}
		h.ThemeOptions[first].GroupStart = true
		h.ThemeOptions[len(h.ThemeOptions)-1].GroupEnd = true
	}
}

var bgColorRe = regexp.MustCompile(`--color-bg\s*:\s*#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`)

// themeLooksDark reports whether a theme's background colour is dark, for
// fixed themes that don't say which kind they are.
func (s *Server) themeLooksDark(name string) bool {
	css, ok := s.themeCSS(name)
	if !ok {
		return false
	}
	root := rootBlockRe.Find(css)
	m := bgColorRe.FindSubmatch(root)
	if m == nil {
		return false
	}
	hex := string(m[1])
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return false
	}
	r, g, b := float64(v>>16&0xff), float64(v>>8&0xff), float64(v&0xff)
	return 0.299*r+0.587*g+0.114*b < 128
}

// themeMenuLabel names a theme in the visitors' menu (see menuLabel).
func (s *Server) themeMenuLabel(name string) string {
	return s.loadThemeModes().menuLabel(name)
}

// menuLabel names a theme in the visitors' menu: "Light / Dark" for a
// pair, "Dim (auto)" for a theme that switches to a partner of its own.
func (t themeModes) menuLabel(name string) string {
	m := t.mode(name)
	if m.Partner == "" {
		return themeLabel(name)
	}
	if t.mode(m.Partner).Partner == name {
		if m.Mode == "dark" {
			return themeLabel(m.Partner) + " / " + themeLabel(name)
		}
		return themeLabel(name) + " / " + themeLabel(m.Partner)
	}
	return themeLabel(name) + " (auto)"
}

func (s *Server) themeCSSVersion(theme string, black bool) string {
	data, ok := s.themeCSSFor(theme, black)
	if !ok {
		return "0"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// allThemeNames lists built-in (folder) and custom (admin-made) themes.
func (s *Server) allThemeNames() []string {
	names := make([]string, 0, len(s.Themes))
	for name := range s.Themes {
		names = append(names, name)
	}
	if s.CustomThemes != nil {
		if custom, err := s.CustomThemes.Names(); err == nil {
			for _, n := range custom {
				if _, builtIn := s.Themes[n]; !builtIn {
					names = append(names, n)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

func (s *Server) themeExists(name string) bool {
	if _, ok := s.Themes[name]; ok {
		return true
	}
	return s.customTheme(name) != nil
}

func (s *Server) customTheme(name string) *models.CustomTheme {
	if s.CustomThemes == nil || name == "" {
		return nil
	}
	t, err := s.CustomThemes.Get(name)
	if err != nil {
		return nil
	}
	return t
}

// themeCSS returns a theme's stylesheet.
func (s *Server) themeCSS(name string) ([]byte, bool) {
	if dir, ok := s.ThemeDirs[name]; ok {
		data, err := os.ReadFile(filepath.Join(dir, "style.css"))
		return data, err == nil
	}
	if t := s.customTheme(name); t != nil {
		return []byte(t.CSS), true
	}
	return nil, false
}

// blackCookie holds a visitor's "pure black in dark mode" choice.
const blackCookie = "fedigest_black"

// blackRoot sets the background and box colours in a stylesheet's first
// :root block to pure black (the visitor's AMOLED choice).
func blackRoot(css []byte) []byte {
	text := string(css)
	loc := rootBlockRe.FindStringIndex(text)
	if loc == nil {
		return css
	}
	root := text[loc[0]:loc[1]]
	for _, bv := range blackVars {
		v, re := bv.name, bv.re
		if re.MatchString(root) {
			root = re.ReplaceAllString(root, "${1}#000000;")
		} else {
			root = strings.TrimSuffix(root, "}") + "\t" + v + ": #000000;\n}"
		}
	}
	return []byte(text[:loc[0]] + root + text[loc[1]:])
}

var rootBlockRe = regexp.MustCompile(`:root\s*\{[^}]*\}`)

// blackVars are the variables pure black sets, with their patterns.
var blackVars = []struct {
	name string
	re   *regexp.Regexp
}{
	{"--color-bg", regexp.MustCompile(`(--color-bg\s*:\s*)[^;]*;`)},
	{"--color-surface", regexp.MustCompile(`(--color-surface\s*:\s*)[^;]*;`)},
}

// themeCSSFor is the stylesheet as served; ?black=1 is the pure black variant.
func (s *Server) themeCSSFor(name string, black bool) ([]byte, bool) {
	css, ok := s.themeCSS(name)
	if ok && black {
		css = blackRoot(css)
	}
	return css, ok
}

// serveThemeFile serves a file of the named theme; a custom theme only has
// its stylesheet.
func (s *Server) serveThemeFile(w http.ResponseWriter, r *http.Request, name, file string) {
	cacheHeaders(w, r)
	if _, builtIn := s.ThemeDirs[name]; !builtIn {
		if t := s.customTheme(name); t != nil && path.Clean("/"+file) == "/style.css" {
			css, _ := s.themeCSSFor(name, r.URL.Query().Get("black") == "1")
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Write(css)
			return
		}
		http.NotFound(w, r)
		return
	}
	if path.Clean("/"+file) == "/style.css" && r.URL.Query().Get("black") == "1" {
		css, _ := s.themeCSSFor(name, true)
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(css)
		return
	}
	serveThemeAsset(w, r, s.ThemeDirs[name], file)
}

// HandleThemeChoice saves (or, for "", clears) the visitor's theme and
// returns to the page they were on.
func (s *Server) HandleThemeChoice(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("theme")
	theme, _ := s.validThemeChoice(name)
	if name == "" || theme == "" {
		http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)})
	} else {
		http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: name, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, HttpOnly: true, Secure: isHTTPS(r)})
	}
	// The black checkbox is only on the form while a dark theme is in use;
	// has_black says it was there, so other theme changes keep the choice.
	if r.URL.Query().Get("has_black") == "1" {
		secure := isHTTPS(r)
		if r.URL.Query().Get("black") == "1" {
			http.SetCookie(w, &http.Cookie{Name: blackCookie, Value: "1", Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, HttpOnly: true, Secure: secure})
		} else {
			http.SetCookie(w, &http.Cookie{Name: blackCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), SameSite: http.SameSiteLaxMode, Secure: secure})
		}
	}
	http.Redirect(w, r, localRedirect(r.URL.Query().Get("back")), http.StatusSeeOther)
}

// themeAssetExts are the only files served from theme directories, so a
// theme's templates aren't readable over HTTP.
var themeAssetExts = map[string]bool{".css": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true, ".ico": true}

// serveThemeAsset serves file (a path inside the theme dir) if it's an asset.
func serveThemeAsset(w http.ResponseWriter, r *http.Request, dir, file string) {
	clean := path.Clean("/" + file)
	if dir == "" || !themeAssetExts[strings.ToLower(path.Ext(clean))] {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(dir, filepath.FromSlash(clean)))
}
