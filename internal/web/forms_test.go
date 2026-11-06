package web

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"fedigest/internal/models"
)

// postBody is post, returning the response body too.
func (ts *testSite) postBody(c *http.Client, path string, form url.Values) (*http.Response, string) {
	ts.t.Helper()
	req, _ := http.NewRequest("POST", ts.http.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func pickForm(kv ...string) url.Values {
	v := url.Values{"source_type": {"local"}, "action": {"draft"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func TestPickFormKeepsInputOnError(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	cat := fmt.Sprint(ts.catID)

	// New pick, linked type without a URL: everything typed comes back.
	resp, body := ts.postBody(admin, "/admin/picks/new", pickForm("source_type", "fedi", "title", "My typed title",
		"excerpt", "My typed excerpt", "body", "My typed body", "category_id", cat, "alt_text", "An alt"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "needs the original post") {
		t.Fatalf("status %d, want the form with an error", resp.StatusCode)
	}
	for _, want := range []string{"My typed title", "My typed excerpt", "My typed body", "An alt"} {
		if !strings.Contains(body, want) {
			t.Errorf("new pick form lost %q", want)
		}
	}

	// Edit with a missing excerpt: the edits come back, not the saved values.
	resp, body = ts.postBody(admin, fmt.Sprintf("/admin/picks/%d/edit", ts.pickID), pickForm("title", "Edited title",
		"excerpt", "", "body", "Edited body", "category_id", cat))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Edited title") || !strings.Contains(body, "Edited body") {
		t.Errorf("edit form didn't keep the edits (status %d)", resp.StatusCode)
	}

	// An unknown category is a form error, not a 500.
	resp, body = ts.postBody(admin, "/admin/picks/new", pickForm("title", "T", "excerpt", "E", "category_id", "999"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Choose a category") {
		t.Errorf("unknown category: status %d", resp.StatusCode)
	}
	resp, body = ts.postBody(admin, "/admin/picks/new", pickForm("source_type", "fedi", "original_url", "lemmy.example/post/1",
		"source_platform", "friendica", "title", "T", "excerpt", "E", "category_id", cat))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Choose the platform") {
		t.Errorf("unknown platform: status %d", resp.StatusCode)
	}
	// An unknown extra category is ignored rather than failing after the
	// pick was created.
	resp = ts.post(admin, "/admin/picks/new", url.Values{"source_type": {"local"}, "action": {"publish"}, "title": {"Extras"},
		"excerpt": {"E"}, "category_id": {cat}, "extra_category": {"999"}})
	if resp.StatusCode != http.StatusFound {
		t.Errorf("unknown extra category: status %d", resp.StatusCode)
	}
}

func TestPickDuplicatePageKeepsImageAltAndSchedule(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	cat := fmt.Sprint(ts.catID)
	first := pickForm("source_type", "fedi", "source_platform", "lemmy", "original_url", "https://lemmy.example/post/7",
		"title", "First", "excerpt", "E", "category_id", cat)
	if resp := ts.post(admin, "/admin/picks/new", first); resp.StatusCode != http.StatusFound {
		t.Fatalf("first: status %d", resp.StatusCode)
	}
	at := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	second := pickForm("source_type", "fedi", "source_platform", "lemmy", "original_url", "https://lemmy.example/post/7",
		"title", "Second", "excerpt", "E", "category_id", cat, "image_url", "https://img.example/a.png", "alt_text", "Alt kept", "publish_at", at)
	_, body := ts.postBody(admin, "/admin/picks/new", second)
	if !strings.Contains(body, "already been picked") {
		t.Fatal("no duplicate warning")
	}
	for _, want := range []string{"https://img.example/a.png", "Alt kept", at, `name="confirm_duplicate"`} {
		if !strings.Contains(body, want) {
			t.Errorf("duplicate page lost %q", want)
		}
	}
}

func TestPickEditSlugAltAndURLChange(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	cat := fmt.Sprint(ts.catID)
	edit := fmt.Sprintf("/admin/picks/%d/edit", ts.pickID)

	ts.post(admin, edit, pickForm("action", "publish", "title", "A Completely New Title", "excerpt", "E", "category_id", cat, "alt_text", "An alt"))
	p, _ := ts.srv.Picks.GetByID(ts.pickID)
	if p.Slug != "a-completely-new-title" {
		t.Errorf("slug %q after renaming", p.Slug)
	}
	if p.AltText.String != "An alt" {
		t.Fatalf("alt text not saved: %q", p.AltText.String)
	}
	ts.post(admin, edit, pickForm("action", "publish", "title", "A Completely New Title", "excerpt", "E", "category_id", cat, "alt_text", ""))
	if p, _ = ts.srv.Picks.GetByID(ts.pickID); p.AltText.Valid {
		t.Errorf("alt text couldn't be cleared: %q", p.AltText.String)
	}

	// A fedi pick whose URL is changed without fetching again loses the
	// old post's details and cached comments.
	fedi := pickForm("action", "publish", "source_type", "fedi", "source_platform", "lemmy", "original_url", "https://lemmy.example/post/1",
		"original_ap_id", "https://lemmy.example/post/1", "community_post_url", "https://lemmy.example/post/1",
		"original_author", "bob@lemmy.example", "title", "T", "excerpt", "E", "category_id", cat)
	ts.post(admin, edit, fedi)
	if err := ts.srv.Picks.SaveCommentsSuccess(ts.pickID, 3, `[]`); err != nil {
		t.Fatal(err)
	}
	fedi.Set("original_url", "https://lemmy.example/post/2")
	ts.post(admin, edit, fedi)
	p, _ = ts.srv.Picks.GetByID(ts.pickID)
	if p.OriginalApID.Valid || p.CommunityPostURL.Valid || p.OriginalAuthor.Valid {
		t.Errorf("old post's details kept: %q %q %q", p.OriginalApID.String, p.CommunityPostURL.String, p.OriginalAuthor.String)
	}
	if p.CommentCount.Valid || p.CommentsCache.Valid {
		t.Error("old post's comments kept")
	}
}

// multipartForm builds a multipart body with one file field.
func multipartForm(t *testing.T, fields map[string]string, fileField string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if fileField != "" {
		fw, _ := mw.CreateFormFile(fileField, "image.png")
		fw.Write(file)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUploadTooLarge(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("curator")
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxUploadBytes+1)...)
	body, ctype := multipartForm(t, nil, "avatar_upload", big)
	req, _ := http.NewRequest("POST", ts.http.URL+"/account/avatar", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	u, _ := ts.srv.Users.GetByUsername("curator")
	if u.AvatarUploadPath.Valid {
		t.Error("an over-size avatar was saved")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestSearchWhitespaceQuery(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	for _, q := range []string{"%20", "%09", "%C2%A0", "%22", "*", "-", "NEAR(", "a%20OR"} {
		ts.checkPage(c, "/search?q="+q, http.StatusOK)
	}
}

func TestUserFormsAndReset(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")

	// A taken username comes back with the chosen role still selected.
	_, body := ts.postBody(admin, "/admin/users/new", url.Values{"username": {"curator"}, "password": {"x"}, "role": {"admin"}})
	if !strings.Contains(body, `<option value="admin" selected>`) {
		t.Error("role not kept after an error")
	}
	_, body = ts.postBody(admin, "/admin/users/new", url.Values{"username": {"has space"}, "password": {"x"}, "role": {"user"}})
	if !strings.Contains(body, "can&#39;t contain spaces") {
		t.Error("username with a space accepted")
	}

	u, _ := ts.srv.Users.GetByUsername("curator")
	resp := ts.post(admin, fmt.Sprintf("/admin/users/%d/reset-password", u.ID), url.Values{"new_password": {""}})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=empty-password") {
		t.Errorf("empty password: redirect %q", loc)
	}
	resp = ts.post(admin, fmt.Sprintf("/admin/users/%d/reset-password", u.ID), url.Values{"new_password": {"new-secret"}})
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "reset=curator") {
		t.Fatalf("reset: redirect %q", loc)
	}
	if body := ts.checkPage(admin, loc, http.StatusOK); !strings.Contains(body, "Password changed for curator") {
		t.Error("no confirmation after a password reset")
	}
}

func TestDeleteUserUnschedulesPicks(t *testing.T) {
	ts := newTestSite(t)
	curator, _ := ts.srv.Users.GetByUsername("curator")
	admin, _ := ts.srv.Users.GetByUsername("admin")
	id, err := ts.srv.Picks.Create(&models.Pick{Slug: "s", Title: "Scheduled", Excerpt: "E", CategoryID: ts.catID,
		CuratorID: curator.ID, SourceType: models.SourceTypeLocal, Status: models.PickStatusDraft,
		PublishAt: sql.NullString{String: time.Now().Add(time.Minute).UTC().Format(time.RFC3339), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	n, err := ts.srv.Users.Delete(curator.ID, admin.ID)
	if err != nil || n != 1 {
		t.Fatalf("Delete: %d, %v", n, err)
	}
	if p, _ := ts.srv.Picks.GetByID(id); p.PublishAt.Valid {
		t.Error("a deleted user's scheduled pick would still be published")
	}
}

func TestSourceFormKeepsInputOnError(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	_, body := ts.postBody(admin, "/admin/sources/new", url.Values{"platform": {"lemmy"}, "instance_url": {"lemmy.example"},
		"watch_type": {"community"}, "watch_target": {"technology"}, "mode": {"auto-publish"}, "poll_interval_sec": {"1200"}})
	if !strings.Contains(body, "default category is required") {
		t.Fatal("no error for a missing category")
	}
	for _, want := range []string{`<option value="lemmy" selected>`, "https://lemmy.example", `value="technology"`,
		`<option value="auto-publish" selected>`, `value="1200"`} {
		if !strings.Contains(body, want) {
			t.Errorf("source form lost %q", want)
		}
	}
}

func TestCategorySlugAndOrder(t *testing.T) {
	ts := newTestSite(t)
	admin := ts.client("admin")
	ts.post(admin, "/admin/categories/new", url.Values{"name": {"Retro Games"}, "slug": {"Games/Retro Stuff"}})
	c, err := ts.srv.Categories.GetBySlug("games-retro-stuff")
	if err != nil {
		t.Fatal("typed slug wasn't made URL-safe")
	}
	news, _ := ts.srv.Categories.GetByID(ts.catID)
	if c.SortOrder <= news.SortOrder {
		t.Errorf("new category sorts at %d, before the existing one at %d", c.SortOrder, news.SortOrder)
	}
	// Saving with the slug unchanged keeps it, even one that wouldn't be
	// generated today.
	if _, err := ts.srv.DB.Exec(`UPDATE categories SET slug = 'Old_Slug' WHERE id = ?`, c.ID); err != nil {
		t.Fatal(err)
	}
	ts.post(admin, fmt.Sprintf("/admin/categories/%d/edit", c.ID), url.Values{"name": {"Renamed"}, "slug": {"Old_Slug"}, "sort_order": {"3"}})
	if c, _ = ts.srv.Categories.GetByID(c.ID); c.Slug != "Old_Slug" || c.Name != "Renamed" {
		t.Errorf("after edit: slug %q, name %q", c.Slug, c.Name)
	}
}

func TestEditLinksOnPublicPages(t *testing.T) {
	ts := newTestSite(t)
	curator, _ := ts.srv.Users.GetByUsername("curator")
	ownID, err := ts.srv.Picks.Create(&models.Pick{Slug: "mine", Title: "Mine", Excerpt: "E", CategoryID: ts.catID,
		CuratorID: curator.ID, SourceType: models.SourceTypeLocal, Status: models.PickStatusPublished,
		PublishedAt: sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	editOwn := fmt.Sprintf(`href="/admin/picks/%d/edit"`, ownID)
	editAdmins := fmt.Sprintf(`href="/admin/picks/%d/edit"`, ts.pickID)

	for _, tc := range []struct {
		who            string
		own, adminPick bool
	}{{"", false, false}, {"curator", true, false}, {"admin", true, true}} {
		c := ts.client(tc.who)
		for _, path := range []string{"/", "/c/news", "/search?q=mine", "/search?q=hello"} {
			body := ts.checkPage(c, path, http.StatusOK)
			if strings.Contains(body, editOwn) && !tc.own {
				t.Errorf("%q sees an edit link for the curator's pick on %s", tc.who, path)
			}
			if strings.Contains(body, editAdmins) && !tc.adminPick {
				t.Errorf("%q sees an edit link for the admin's pick on %s", tc.who, path)
			}
		}
		if body := ts.checkPage(c, "/", http.StatusOK); tc.own != strings.Contains(body, editOwn) || tc.adminPick != strings.Contains(body, editAdmins) {
			t.Errorf("%q: frontpage edit links wrong", tc.who)
		}
		if body := ts.checkPage(c, fmt.Sprintf("/p/%d-mine", ownID), http.StatusOK); tc.own != strings.Contains(body, editOwn) {
			t.Errorf("%q: post page edit link wrong", tc.who)
		}
	}

	// The links lead somewhere the viewer is allowed to go.
	c := ts.client("curator")
	ts.checkPage(c, fmt.Sprintf("/admin/picks/%d/edit", ownID), http.StatusOK)
	ts.checkPage(c, fmt.Sprintf("/admin/picks/%d/edit", ts.pickID), http.StatusForbidden)
}

func TestAutoExcerptOnPages(t *testing.T) {
	ts := newTestSite(t)
	admin, _ := ts.srv.Users.GetByUsername("admin")
	original := "Opening line.\n\nSecond paragraph.\n\nThird paragraph is only on the post page."
	id, err := ts.srv.Picks.Create(&models.Pick{Slug: "auto", Title: "Auto", Excerpt: "Opening line.\n\nSecond paragraph.",
		ExcerptAuto: true, UseOriginalAsBody: true, OriginalBodyCache: sql.NullString{String: original, Valid: true},
		CategoryID: ts.catID, CuratorID: admin.ID, SourceType: models.SourceTypeFedi, AutoIngested: true,
		SourcePlatform: sql.NullString{String: "lemmy", Valid: true},
		OriginalURL:    sql.NullString{String: "https://lemmy.example/post/9", Valid: true},
		Status:         models.PickStatusPublished,
		PublishedAt:    sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	c := ts.client("")
	front := ts.checkPage(c, "/", http.StatusOK)
	if !strings.Contains(front, "Second paragraph.") || strings.Contains(front, "Third paragraph") {
		t.Error("card doesn't show just the opening paragraphs")
	}
	// Both cards have more on their post page (the test pick has a body).
	if n := strings.Count(front, `class="pick-read-more"`); n != 2 {
		t.Errorf("%d Read more links, want 2", n)
	}
	if err := ts.srv.Picks.Update(&models.Pick{ID: ts.pickID, Slug: "hello-world", Title: "Hello world", Excerpt: "An excerpt",
		CategoryID: ts.catID, CuratorID: admin.ID, SourceType: models.SourceTypeLocal, Status: models.PickStatusPublished,
		PublishedAt: sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(ts.checkPage(c, "/", http.StatusOK), `class="pick-read-more"`); n != 1 {
		t.Errorf("%d Read more links after removing the body, want 1", n)
	}
	post := ts.checkPage(c, fmt.Sprintf("/p/%d-auto", id), http.StatusOK)
	main := post[strings.Index(post, "<main"):]
	if strings.Count(main, "Opening line.") != 1 || !strings.Contains(main, "Third paragraph is only on the post page.") || strings.Contains(main, "<hr>") {
		t.Error("post page should show the original once, in full")
	}
}

func TestRefreshStoresAutoExcerpt(t *testing.T) {
	ts := newTestSite(t)
	admin, _ := ts.srv.Users.GetByUsername("admin")
	mk := func(auto bool) int64 {
		id, err := ts.srv.Picks.Create(&models.Pick{Slug: "x", Title: "Old title stub", Excerpt: "Old title stub", ExcerptAuto: auto,
			UseOriginalAsBody: true, OriginalBodyCache: sql.NullString{String: "Opening.\n\nSecond.\n\nThird.", Valid: true},
			CategoryID: ts.catID, CuratorID: admin.ID, SourceType: models.SourceTypeFedi, Status: models.PickStatusPublished,
			PublishedAt: sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	auto, typed := mk(true), mk(false)
	if !ts.srv.syncAutoExcerpt(auto) || ts.srv.syncAutoExcerpt(auto) {
		t.Error("first sync should change the excerpt, the second nothing")
	}
	if ts.srv.syncAutoExcerpt(typed) {
		t.Error("a hand-written excerpt was changed")
	}
	if p, _ := ts.srv.Picks.GetByID(auto); p.Excerpt != "Opening.\n\nSecond." {
		t.Errorf("stored excerpt %q", p.Excerpt)
	}
	if p, _ := ts.srv.Picks.GetByID(typed); p.Excerpt != "Old title stub" {
		t.Errorf("hand-written excerpt now %q", p.Excerpt)
	}
	// Search now finds the opening paragraph.
	if hits, err := ts.srv.Picks.Search("Opening", 1); err != nil || len(hits) != 1 {
		t.Errorf("search: %d hits, %v", len(hits), err)
	}
}

func TestFooterLinksToSource(t *testing.T) {
	ts := newTestSite(t)
	body := ts.checkPage(ts.client(""), "/", http.StatusOK)
	if !strings.Contains(body, `<a href="`+ts.srv.Cfg.SourceURL+`">Source</a>`) || ts.srv.Cfg.SourceURL == "" {
		t.Errorf("footer has no Source link to %q", ts.srv.Cfg.SourceURL)
	}
}

func TestPostPageBylineLinks(t *testing.T) {
	ts := newTestSite(t)
	c := ts.client("")
	body := ts.checkPage(c, ts.pick, http.StatusOK)
	if !strings.Contains(body, `By <a href="/author/admin">`) {
		t.Error("post page byline doesn't link the curator to their author page")
	}
	if !strings.Contains(body, ` in <a href="/c/news">News</a>`) {
		t.Error("post page byline doesn't link the category")
	}
	if body := ts.checkPage(c, "/", http.StatusOK); strings.Contains(body, `href="/author/`) || strings.Contains(body, ` in <a href="/c/`) {
		t.Error("card bylines must stay plain text (the card is already a link)")
	}
	ts.checkPage(c, "/author/admin", http.StatusOK)

	// Auto posts show the label, without a link.
	admin, _ := ts.srv.Users.GetByUsername("admin")
	id, err := ts.srv.Picks.Create(&models.Pick{Slug: "auto", Title: "Auto", Excerpt: "E", AutoIngested: true,
		CategoryID: ts.catID, CuratorID: admin.ID, SourceType: models.SourceTypeLocal, Status: models.PickStatusPublished,
		PublishedAt: sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	body = ts.checkPage(c, fmt.Sprintf("/p/%d-auto", id), http.StatusOK)
	if strings.Contains(body, `By <a href="/author/`) {
		t.Error("auto post byline links to the admin's author page")
	}
	if !strings.Contains(body, ` in <a href="/c/news">News</a>`) {
		t.Error("auto post byline doesn't link the category")
	}
}
