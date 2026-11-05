package web

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/auth"
	"fedigest/internal/config"
	"fedigest/internal/db"
	"fedigest/internal/models"
)

// testSite is a full Server on a temp database with the real themes, one
// category, one published local pick, an admin and a user-role account.
type testSite struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	catID  int64
	pickID int64
	pick   string // post page path
}

func newTestSite(t *testing.T) *testSite {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cfg := &config.Config{DataDir: dir, UploadsDir: t.TempDir(), SiteName: "Test", CommentCacheTTL: 60, SourceURL: config.DefaultSourceURL}
	srv, err := New(cfg, conn, "../../themes", adapters.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	srv.attempts = newAttemptLimiter(15*time.Minute, 15*time.Minute)
	ts := &testSite{t: t, srv: srv}
	for _, u := range []struct {
		name string
		role models.Role
	}{{"admin", models.RoleAdmin}, {"curator", models.RoleUser}} {
		hash, err := auth.HashPassword("secret-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := srv.Users.Create(&models.User{Username: u.name, PasswordHash: hash, Role: u.role}); err != nil {
			t.Fatal(err)
		}
	}
	if ts.catID, err = srv.Categories.Create("News", "news", 0, true); err != nil {
		t.Fatal(err)
	}
	admin, _ := srv.Users.GetByUsername("admin")
	ts.pickID, err = srv.Picks.Create(&models.Pick{
		Slug: "hello-world", Title: "Hello world", Excerpt: "An excerpt", Body: "The **body**.",
		CategoryID: ts.catID, CuratorID: admin.ID, SourceType: models.SourceTypeLocal,
		Status: models.PickStatusPublished, PublishedAt: sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts.pick = fmt.Sprintf("/p/%d-hello-world", ts.pickID)
	ts.http = httptest.NewTLSServer(srv.Routes())
	t.Cleanup(ts.http.Close)
	return ts
}

// client returns an HTTP client that doesn't follow redirects, logged in
// as username unless it's empty.
func (ts *testSite) client(username string) *http.Client {
	ts.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: ts.http.Client().Transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if username != "" {
		resp := ts.post(c, "/login", url.Values{"username": {username}, "password": {"secret-password"}})
		if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
			ts.t.Fatalf("login as %s: status %d", username, resp.StatusCode)
		}
	}
	return c
}

func (ts *testSite) get(c *http.Client, path string) (*http.Response, string) {
	ts.t.Helper()
	resp, err := c.Get(ts.http.URL + path)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// post sends a same-origin form POST, as a browser would.
func (ts *testSite) post(c *http.Client, path string, form url.Values) *http.Response {
	ts.t.Helper()
	req, _ := http.NewRequest("POST", ts.http.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// checkPage fails on a status other than want or a template error in the body.
func (ts *testSite) checkPage(c *http.Client, path string, want int) string {
	ts.t.Helper()
	resp, body := ts.get(c, path)
	if resp.StatusCode != want {
		ts.t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, want)
	}
	if strings.Contains(body, "template:") || strings.Contains(body, "<no value>") {
		ts.t.Errorf("GET %s: template problem in output", path)
	}
	return body
}

func TestPublicPages(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	for _, p := range []string{"/", "/c/news", ts.pick, "/author/admin", "/categories", "/privacy",
		"/search?q=hello", "/feed.xml", "/c/news/feed.xml", "/sitemap.xml", "/robots.txt", "/login", "/forgot-password",
		"/static/admin.css", "/theme/style.css"} {
		ts.checkPage(c, p, 200)
	}
	if body := ts.checkPage(c, ts.pick, 200); !strings.Contains(body, "<strong>body</strong>") {
		t.Error("post page doesn't render the markdown body")
	}
	for _, p := range []string{"/p/9999-nope", "/c/nope", "/author/nobody", "/no-such-page"} {
		ts.checkPage(c, p, 404)
	}
}

func TestDraftsHidden(t *testing.T) {
	ts := newTestSite(t)
	if _, err := ts.srv.DB.Exec(`UPDATE picks SET status = 'draft' WHERE id = ?`, ts.pickID); err != nil {
		t.Fatal(err)
	}
	ts.checkPage(ts.client(""), ts.pick, 404)
	ts.checkPage(ts.client("curator"), ts.pick, 404)
	ts.checkPage(ts.client("admin"), ts.pick, 200)
}

func adminGetRoutes(ts *testSite) []string {
	return []string{"/admin/picks", "/admin/picks/new", fmt.Sprintf("/admin/picks/%d/edit", ts.pickID),
		"/admin/categories", "/admin/categories/new", fmt.Sprintf("/admin/categories/%d/edit", ts.catID),
		"/admin/users", "/admin/users/new", "/admin/users/1/edit",
		"/admin/sources", "/admin/sources/new", "/admin/themes", "/admin/themes/light/edit",
		"/admin/settings", "/admin/sidebar", "/admin/sidebar/new", "/account"}
}

func TestAdminPagesRender(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("admin")
	for _, p := range adminGetRoutes(ts) {
		ts.checkPage(c, p, 200)
	}
}

func TestAccessControl(t *testing.T) {
	ts := newTestSite(t)
	anon := ts.client("")
	for _, p := range adminGetRoutes(ts) {
		resp, _ := ts.get(anon, p)
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/login" {
			t.Errorf("anonymous GET %s: status %d, location %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	user := ts.client("curator")
	userOK := map[string]bool{"/admin/picks": true, "/admin/picks/new": true, "/account": true}
	for _, p := range adminGetRoutes(ts) {
		resp, _ := ts.get(user, p)
		want := http.StatusForbidden
		if userOK[p] {
			want = 200
		}
		if resp.StatusCode != want {
			t.Errorf("user-role GET %s: status %d, want %d", p, resp.StatusCode, want)
		}
	}
	// Another curator's pick can't be edited or deleted.
	resp, _ := ts.get(user, fmt.Sprintf("/admin/picks/%d/edit", ts.pickID))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("user editing admin's pick: status %d", resp.StatusCode)
	}
	if resp := ts.post(user, fmt.Sprintf("/admin/picks/%d/delete", ts.pickID), nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("user deleting admin's pick: status %d", resp.StatusCode)
	}
	if _, err := ts.srv.Picks.GetByID(ts.pickID); err != nil {
		t.Errorf("pick gone after forbidden delete: %v", err)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("admin")
	req, _ := http.NewRequest("POST", ts.http.URL+fmt.Sprintf("/admin/picks/%d/delete", ts.pickID), nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site delete: status %d", resp.StatusCode)
	}
	if _, err := ts.srv.Picks.GetByID(ts.pickID); err != nil {
		t.Error("pick deleted by a cross-site request")
	}
}

func TestLoginRateLimit(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	var last int
	for i := 0; i < 6; i++ {
		last = ts.post(c, "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}).StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("6th failed login: status %d, want 429", last)
	}
}

func TestThemeFiles(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	for name := range ts.srv.Themes {
		_, body := ts.get(c, "/themes/"+name+"/style.css")
		if !strings.Contains(body, "--color-bg") {
			t.Errorf("theme %s: style.css missing or without variables", name)
		}
	}
	for _, p := range []string{"/themes/light/base.html", "/themes/light/../../go.mod", "/theme/base.html", "/themes/nope/style.css"} {
		if resp, _ := ts.get(c, p); resp.StatusCode != 404 && resp.StatusCode != http.StatusMovedPermanently {
			t.Errorf("GET %s: status %d, want 404", p, resp.StatusCode)
		}
	}
	if _, body := ts.get(c, "/themes/dark/style.css?black=1"); !strings.Contains(body, "--color-bg: #000000;") {
		t.Error("black variant doesn't set a black background")
	}
	if _, body := ts.get(c, "/themes/terminal/fonts/ibm-vga-8x16.woff"); body == "" {
		t.Error("terminal font not served")
	}
}

func TestPrivacyPage(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	body := ts.checkPage(c, "/privacy", 200)
	for _, want := range []string{"fedigest_theme", "fedigest_black", "Contact the people who run this site."} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy page missing %q", want)
		}
	}
	admin := ts.client("admin")
	form := url.Values{"site_name": {"Test"}, "theme": {"light"}, "auto_post_label": {"Auto post"}, "privacy_contact": {"Mail [us](mailto:us@example.org)"}}
	if resp := ts.post(admin, "/admin/settings", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("save settings: status %d", resp.StatusCode)
	}
	if body := ts.checkPage(c, "/privacy", 200); !strings.Contains(body, `<a href="mailto:us@example.org">us</a>`) {
		t.Error("contact line not shown")
	}
}

func TestPickFormFlow(t *testing.T) {
	ts := newTestSite(t)
	user := ts.client("curator")
	form := url.Values{"title": {"My local post"}, "excerpt": {"Short"}, "body": {"Long text"},
		"category_id": {fmt.Sprint(ts.catID)}, "source_type": {"local"}, "action": {"publish"}}
	resp := ts.post(user, "/admin/picks/new", form)
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create pick: status %d", resp.StatusCode)
	}
	if body := ts.checkPage(ts.client(""), "/", 200); !strings.Contains(body, "My local post") {
		t.Error("published pick not on the frontpage")
	}
	var id int64
	ts.srv.DB.QueryRow(`SELECT id FROM picks WHERE title = 'My local post'`).Scan(&id)
	form.Set("title", "Renamed post")
	ts.post(user, fmt.Sprintf("/admin/picks/%d/edit", id), form)
	if p, _ := ts.srv.Picks.GetByID(id); p == nil || p.Title != "Renamed post" {
		t.Error("curator couldn't edit their own pick")
	}
	if body := ts.checkPage(ts.client(""), "/search?q=renamed", 200); !strings.Contains(body, "Renamed post") {
		t.Error("search doesn't find the edited title")
	}
	// Missing category is refused.
	form.Del("category_id")
	form.Set("title", "No category")
	ts.post(user, "/admin/picks/new", form)
	var n int
	ts.srv.DB.QueryRow(`SELECT COUNT(*) FROM picks WHERE title = 'No category'`).Scan(&n)
	if n != 0 {
		t.Error("pick without a category was saved")
	}
}

func TestUploadsAreImagesOnly(t *testing.T) {
	ts := newTestSite(t)
	dir := ts.srv.Cfg.UploadsDir
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	saved, err := saveUploadedImage(dir, "picks", bytes.NewReader(png))
	if err != nil || !strings.HasSuffix(saved, ".png") {
		t.Fatalf("png upload: %q, %v", saved, err)
	}
	for name, data := range map[string]string{
		"html": "<!doctype html><script>alert(1)</script>",
		"svg":  `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"text": "hello", "empty": "",
	} {
		if _, err := saveUploadedImage(dir, "picks", strings.NewReader(data)); err == nil {
			t.Errorf("%s upload accepted", name)
		}
	}
	c := ts.client("")
	if resp, _ := ts.get(c, "/uploads/"+saved); resp.StatusCode != 200 || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("serving upload: status %d, nosniff %q", resp.StatusCode, resp.Header.Get("X-Content-Type-Options"))
	}
	for _, p := range []string{"/uploads/", "/uploads/picks/", "/uploads/picks", "/uploads/../go.mod"} {
		if resp, _ := ts.get(c, p); resp.StatusCode == 200 {
			t.Errorf("GET %s listed or served", p)
		}
	}
}

func TestLocalRedirect(t *testing.T) {
	for in, want := range map[string]string{
		"/c/news?page=2": "/c/news?page=2", "/": "/", "": "/",
		"//evil.example": "/", "/\\evil.example": "/", "/\t/evil.example": "/", "https://evil.example": "/",
		"evil.example": "/", "/%09/evil.example": "/%09/evil.example", "javascript:alert(1)": "/",
	} {
		if got := localRedirect(in); got != want {
			t.Errorf("localRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestSite(t)
	resp, _ := ts.get(ts.client(""), "/")
	for k, v := range map[string]string{"X-Frame-Options": "SAMEORIGIN", "X-Content-Type-Options": "nosniff"} {
		if resp.Header.Get(k) != v {
			t.Errorf("%s: %q", k, resp.Header.Get(k))
		}
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	ts := newTestSite(t)
	other := ts.client("curator")
	me := ts.client("curator")
	ts.post(me, "/account/password", url.Values{"current_password": {"secret-password"}, "new_password": {"another-password"}, "confirm_password": {"another-password"}})
	if resp, _ := ts.get(me, "/account"); resp.StatusCode != 200 {
		t.Errorf("session that changed the password was logged out: %d", resp.StatusCode)
	}
	if resp, _ := ts.get(other, "/account"); resp.StatusCode != http.StatusFound {
		t.Errorf("other session still logged in: %d", resp.StatusCode)
	}
}

func TestLastAdminStaysAdmin(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	u, _ := ts.srv.Users.GetByUsername("admin")
	ts.post(admin, fmt.Sprintf("/admin/users/%d/edit", u.ID), url.Values{"username": {"admin"}, "role": {"user"}})
	if u, _ = ts.srv.Users.GetByUsername("admin"); u.Role != models.RoleAdmin {
		t.Error("the only admin was demoted")
	}
}

func TestResetTokenSingleUse(t *testing.T) {
	ts := newTestSite(t)
	u, _ := ts.srv.Users.GetByUsername("curator")
	token, err := ts.srv.PasswordResets.Create(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := ts.client("")
	form := url.Values{"token": {token}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"}}
	if resp := ts.post(c, "/reset-password", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("reset: status %d", resp.StatusCode)
	}
	form.Set("new_password", "new-password-2")
	form.Set("confirm_password", "new-password-2")
	ts.post(c, "/reset-password", form)
	if _, err := ts.srv.Auth.Login(httptest.NewRecorder(), "curator", "new-password-1", false); err != nil {
		t.Error("second use of the reset link changed the password")
	}
}

func TestMissingSiteThemeFallsBack(t *testing.T) {
	ts := newTestSite(t)
	if _, err := ts.srv.DB.Exec(`UPDATE site_settings SET theme = 'gone'`); err != nil {
		t.Fatal(err)
	}
	if got := ts.srv.currentThemeName(); got != "light" {
		t.Errorf("missing site theme: using %q", got)
	}
	ts.checkPage(ts.client(""), "/", 200)
}

func TestSourceNameAndExclusions(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	form := url.Values{"platform": {"lemmy"}, "instance_url": {"lemmy.example"}, "watch_type": {"instance"},
		"mode": {"manual"}, "default_category_id": {fmt.Sprint(ts.catID)},
		"exclude_communities": {"Memes, news@lemmy.example"}}
	if resp := ts.post(admin, "/admin/sources/new", form); resp.StatusCode != http.StatusFound {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	body := ts.checkPage(admin, "/admin/sources", http.StatusOK)
	if !strings.Contains(body, "lemmy.example <span class=\"hint\">(whole instance, 2 excluded)") {
		t.Errorf("unnamed instance source should show its host:\n%s", body)
	}
	srcs, _ := ts.srv.Sources.List()
	if got := srcs[0].ExcludeCommunities.String; got != "memes\nnews@lemmy.example" {
		t.Errorf("exclusions saved as %q", got)
	}
	form.Set("name", "Example local")
	ts.post(admin, fmt.Sprintf("/admin/sources/%d/edit", srcs[0].ID), form)
	if body := ts.checkPage(admin, "/admin/sources", http.StatusOK); !strings.Contains(body, "Example local <span class=\"hint\">(whole instance: lemmy.example, 2 excluded)") {
		t.Errorf("named source:\n%s", body)
	}
	// A community source has no exclusions.
	form.Set("watch_type", "community")
	form.Set("watch_target", "retro")
	ts.post(admin, fmt.Sprintf("/admin/sources/%d/edit", srcs[0].ID), form)
	if s, _ := ts.srv.Sources.GetByID(srcs[0].ID); s.ExcludeCommunities.Valid {
		t.Error("community source kept an exclude list")
	}
}
