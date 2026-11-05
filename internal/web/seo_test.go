package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fedigest/internal/models"
)

func TestSitemapEscapesAndSkipsEmptyAuthors(t *testing.T) {
	ts := newTestSite(t)
	if _, err := ts.srv.Categories.Create("Über", "über news", 1, true); err != nil {
		t.Fatal(err)
	}
	body := ts.checkPage(ts.client(""), "/sitemap.xml", http.StatusOK)
	if !strings.Contains(body, "/c/%C3%BCber%20news</loc>") {
		t.Error("category slug not escaped in the sitemap")
	}
	if !strings.Contains(body, "/author/admin</loc>") {
		t.Error("author with published picks missing")
	}
	if strings.Contains(body, "/author/curator</loc>") {
		t.Error("author without published picks listed")
	}
	if !strings.Contains(body, ts.pick+"</loc>") {
		t.Error("published pick missing")
	}
}

func TestPaginationEdges(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	ts.checkPage(c, "/?page=2", http.StatusNotFound)
	ts.checkPage(c, "/c/news?page=5", http.StatusNotFound)
	ts.checkPage(c, "/?page=9223372036854775807", http.StatusNotFound)

	admin, _ := ts.srv.Users.GetByUsername("admin")
	for i := 0; i < models.PicksPerPage; i++ {
		if _, err := ts.srv.Picks.Create(&models.Pick{Slug: "p", Title: fmt.Sprint("Pick ", i), Excerpt: "E", CategoryID: ts.catID,
			CuratorID: admin.ID, SourceType: models.SourceTypeLocal, Status: models.PickStatusPublished,
			PublishedAt: sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	body := ts.checkPage(c, "/?page=2", http.StatusOK)
	if !strings.Contains(body, `rel="canonical" href="`+ts.http.URL+`/?page=2"`) {
		t.Error("page 2 doesn't name itself as canonical")
	}
}

func TestTitlesUseSiteName(t *testing.T) {
	ts := newTestSite(t)
	if err := ts.srv.SiteSettings.UpdateSiteName("My Digest"); err != nil {
		t.Fatal(err)
	}
	c := ts.client("")
	if body := ts.checkPage(c, "/", http.StatusOK); !strings.Contains(body, "<title>My Digest</title>") {
		t.Error("frontpage title isn't just the site name")
	}
	if body := ts.checkPage(c, ts.pick, http.StatusOK); !strings.Contains(body, "<title>Hello world · My Digest</title>") ||
		!strings.Contains(body, `property="og:site_name" content="My Digest"`) {
		t.Error("post page title/og:site_name don't use the site name")
	}
}

func TestMetaDescription(t *testing.T) {
	if got := metaDescription("short  text\n"); got != "short text" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("word ", 100)
	got := metaDescription(long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > metaDescriptionMax+1 || strings.Contains(got, "wor…") {
		t.Errorf("%q", got)
	}
}

func TestBaseURLBehindProxy(t *testing.T) {
	ts := newTestSite(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "news.example"
	r.RemoteAddr = "172.18.0.1:5555"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := ts.srv.baseURL(r); got != "https://news.example" {
		t.Errorf("behind a proxy: %q", got)
	}
	r.RemoteAddr = "203.0.113.9:5555" // a public peer can't claim https
	if got := ts.srv.baseURL(r); got != "http://news.example" {
		t.Errorf("public peer: %q", got)
	}
	ts.srv.Cfg.SiteDomain = "digest.example"
	if got := ts.srv.baseURL(r); got != "https://digest.example" {
		t.Errorf("SITE_DOMAIN: %q", got)
	}
}

func TestStaticCaching(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	resp, _ := ts.get(c, "/static/admin.css?v=abc")
	if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("versioned asset: Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
	if resp, _ := ts.get(c, "/static/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/static/ listing: status %d", resp.StatusCode)
	}
}

func TestLimiterForgets(t *testing.T) {
	l := newAttemptLimiter(15*time.Minute, 15*time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.fail("k", 5)
	now = now.Add(16 * time.Minute)
	l.locked("other")
	if len(l.entries) != 0 {
		t.Errorf("%d entries kept past their window", len(l.entries))
	}
}
