package models_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	fedigestdb "fedigest/internal/db"
	"fedigest/internal/models"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := fedigestdb.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestDeleteUserUnpublishesAndReassigns(t *testing.T) {
	conn := openDB(t)
	users := &models.UserStore{DB: conn}
	picks := &models.PickStore{DB: conn}
	sources := &models.SourceStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}

	admin, _ := users.Create(&models.User{Username: "admin", PasswordHash: "x", Role: models.RoleAdmin})
	abuser, _ := users.Create(&models.User{Username: "abuser", PasswordHash: "x", Role: models.RoleUser})
	cat, _ := cats.Create("General", "general", 0, true)
	srcID, err := sources.Create(&models.Source{Platform: models.PlatformLemmy, InstanceURL: "https://l.example", Mode: models.SourceModeAutoPublish, PollIntervalSec: 900,
		CreatedByUserID: sql.NullInt64{Int64: abuser, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(title string, auto bool) int64 {
		p := &models.Pick{Slug: title, Title: title, Excerpt: "e", CategoryID: cat, CuratorID: abuser, SourceType: models.SourceTypeLocal,
			Status: models.PickStatusPublished, PublishedAt: sql.NullString{String: "2026-09-27T00:00:00Z", Valid: true}, AutoIngested: auto}
		if auto {
			p.SourceID = sql.NullInt64{Int64: srcID, Valid: true}
		}
		id, err := picks.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	own, auto := mk("own", false), mk("auto", true)
	if _, err := conn.Exec(`INSERT INTO sessions (id, user_id, expires_at) VALUES ('s1', ?, '2099-01-01')`, abuser); err != nil {
		t.Fatal(err)
	}

	n, err := users.Delete(abuser, admin)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("unpublished = %d, want 1", n)
	}
	p, _ := picks.GetByID(own)
	if p.Status != models.PickStatusDraft || p.CuratorID != admin {
		t.Errorf("own pick: status %s curator %d, want draft/%d", p.Status, p.CuratorID, admin)
	}
	a, _ := picks.GetByID(auto)
	if a.Status != models.PickStatusPublished || a.CuratorID != admin {
		t.Errorf("auto pick should stay published, moved to admin: %s/%d", a.Status, a.CuratorID)
	}
	src, _ := sources.GetByID(srcID)
	if src.CreatedByUserID.Int64 != admin {
		t.Errorf("source owner = %d", src.CreatedByUserID.Int64)
	}
	if _, err := users.GetByID(abuser); err == nil {
		t.Error("user still exists")
	}
	var sessions int
	conn.QueryRow(`SELECT COUNT(1) FROM sessions WHERE user_id = ?`, abuser).Scan(&sessions)
	if sessions != 0 {
		t.Error("sessions not removed")
	}

	// Deleting the source keeps its picks and their auto byline marker.
	if err := sources.Delete(srcID); err != nil {
		t.Fatal(err)
	}
	a, err = picks.GetByID(auto)
	if err != nil || a.SourceID.Valid || !a.AutoIngested {
		t.Errorf("after source delete: err=%v source_id=%v auto=%v", err, a.SourceID, a.AutoIngested)
	}
}

func TestDeleteCategoryWithSources(t *testing.T) {
	conn := openDB(t)
	sources := &models.SourceStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}

	cat, _ := cats.Create("News", "news", 0, true)
	other, _ := cats.Create("Other", "other", 0, true)
	mk := func(c int64) int64 {
		id, err := sources.Create(&models.Source{Platform: models.PlatformLemmy, InstanceURL: "https://l.example", Mode: models.SourceModeAutoPublish, PollIntervalSec: 900,
			DefaultCategoryID: sql.NullInt64{Int64: c, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	inCat, keep := mk(cat), mk(other)

	if err := cats.Delete(cat, false); err != models.ErrCategoryHasSources {
		t.Fatalf("Delete without deleteSources = %v, want ErrCategoryHasSources", err)
	}
	if list, _ := sources.ListByCategory(cat); len(list) != 1 || list[0].ID != inCat {
		t.Fatalf("ListByCategory = %v", list)
	}
	if err := cats.Delete(cat, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sources.GetByID(inCat); err != models.ErrNotFound {
		t.Errorf("source in deleted category still exists: %v", err)
	}
	if _, err := sources.GetByID(keep); err != nil {
		t.Errorf("unrelated source removed: %v", err)
	}
	if _, err := cats.GetByID(cat); err != models.ErrNotFound {
		t.Errorf("category still exists: %v", err)
	}
}

func TestPinnedPicks(t *testing.T) {
	conn := openDB(t)
	users := &models.UserStore{DB: conn}
	picks := &models.PickStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}

	admin, _ := users.Create(&models.User{Username: "admin", PasswordHash: "x", Role: models.RoleAdmin})
	shown, _ := cats.Create("Shown", "shown", 0, true)
	hidden, _ := cats.Create("Hidden", "hidden", 1, false)
	mk := func(title string, cat int64, status models.PickStatus) int64 {
		id, err := picks.Create(&models.Pick{Slug: title, Title: title, Excerpt: "e", CategoryID: cat, CuratorID: admin, SourceType: models.SourceTypeLocal,
			Status: status, PublishedAt: sql.NullString{String: "2026-10-01T00:00:00Z", Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	normal := mk("normal", shown, models.PickStatusPublished)
	pinHidden := mk("pinned-hidden-cat", hidden, models.PickStatusPublished)
	draft := mk("draft", shown, models.PickStatusDraft)
	for _, id := range []int64{pinHidden, draft} {
		if err := picks.SetPinned(id, true); err != nil {
			t.Fatal(err)
		}
	}

	pinned, _ := picks.ListPinned()
	if len(pinned) != 1 || pinned[0].ID != pinHidden {
		t.Fatalf("ListPinned = %v, want only the published pinned pick", pinned)
	}
	front, _ := picks.ListFrontpageUnpinned(1)
	if len(front) != 1 || front[0].ID != normal {
		t.Errorf("ListFrontpageUnpinned = %v, want only the normal pick", front)
	}
	if inCat, _ := picks.ListCategoryUnpinned(hidden, 1); len(inCat) != 0 {
		t.Errorf("pinned pick also listed in its category: %v", inCat)
	}
	if feed, _ := picks.ListPublished(&hidden, nil, 1); len(feed) != 1 {
		t.Errorf("category feed should still include the pinned pick, got %d", len(feed))
	}

	if err := picks.SetPinned(pinHidden, false); err != nil {
		t.Fatal(err)
	}
	if pinned, _ := picks.ListPinned(); len(pinned) != 0 {
		t.Errorf("still pinned after unpin: %v", pinned)
	}
}

func TestDuplicatesScheduleViews(t *testing.T) {
	conn := openDB(t)
	users := &models.UserStore{DB: conn}
	picks := &models.PickStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}
	admin, _ := users.Create(&models.User{Username: "admin", PasswordHash: "x", Role: models.RoleAdmin})
	cat, _ := cats.Create("General", "general", 0, true)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

	orig, err := picks.Create(&models.Pick{Slug: "a", Title: "a", Excerpt: "e", CategoryID: cat, CuratorID: admin, SourceType: models.SourceTypeFedi,
		Status: models.PickStatusPublished, OriginalURL: ns("https://piefed.example/c/x@lemmy.example/p/5"), OriginalApID: ns("https://lemmy.example/post/9"),
		LinkURL: ns("https://news.example/story")})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][3]string{
		"same url":          {"https://piefed.example/c/x@lemmy.example/p/5", "", ""},
		"same post via ap":  {"https://other.example/post/77", "https://lemmy.example/post/9", ""},
		"ap pasted as url":  {"https://lemmy.example/post/9", "", ""},
		"same linked story": {"https://other.example/post/1", "https://other.example/post/1", "https://news.example/story"},
	} {
		if d, _ := picks.FindDuplicates(0, c[0], c[1], c[2]); len(d) != 1 || d[0].ID != orig {
			t.Errorf("%s: got %d duplicates", name, len(d))
		}
	}
	if d, _ := picks.FindDuplicates(0, "https://other.example/post/2", "", "https://news.example/other"); len(d) != 0 {
		t.Errorf("unrelated post reported as duplicate")
	}
	if d, _ := picks.FindDuplicates(orig, "https://lemmy.example/post/9", "", ""); len(d) != 0 {
		t.Errorf("a pick is its own duplicate")
	}

	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	due, _ := picks.Create(&models.Pick{Slug: "d", Title: "due", Excerpt: "e", CategoryID: cat, CuratorID: admin, SourceType: models.SourceTypeLocal,
		Status: models.PickStatusDraft, PublishAt: ns("2026-10-05T11:59:00Z")})
	later, _ := picks.Create(&models.Pick{Slug: "l", Title: "later", Excerpt: "e", CategoryID: cat, CuratorID: admin, SourceType: models.SourceTypeLocal,
		Status: models.PickStatusDraft, PublishAt: ns("2026-10-05T13:00:00Z")})
	if n, err := picks.PublishDue(now); err != nil || n != 1 {
		t.Fatalf("PublishDue = %d, %v; want 1", n, err)
	}
	if p, _ := picks.GetByID(due); p.Status != models.PickStatusPublished || p.PublishedAt.String != "2026-10-05T11:59:00Z" || p.PublishAt.Valid {
		t.Errorf("due pick: status=%s published_at=%q publish_at=%v", p.Status, p.PublishedAt.String, p.PublishAt)
	}
	if p, _ := picks.GetByID(later); p.Status != models.PickStatusDraft {
		t.Errorf("future pick published early")
	}

	picks.AddView(orig)
	picks.AddView(orig)
	if v, _ := picks.ViewCounts(); v[orig] != 2 {
		t.Errorf("views = %d, want 2", v[orig])
	}
	if err := picks.Delete(orig); err != nil {
		t.Errorf("deleting a viewed pick: %v", err)
	}
}

func TestExtraCategories(t *testing.T) {
	conn := openDB(t)
	users := &models.UserStore{DB: conn}
	picks := &models.PickStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}
	sources := &models.SourceStore{DB: conn}
	admin, _ := users.Create(&models.User{Username: "admin", PasswordHash: "x", Role: models.RoleAdmin})
	news, _ := cats.Create("News", "news", 0, true)
	retro, _ := cats.Create("Retro", "retro", 1, true)
	hidden, _ := cats.Create("Hidden", "hidden", 2, false)

	id, _ := picks.Create(&models.Pick{Slug: "p", Title: "p", Excerpt: "e", CategoryID: hidden, CuratorID: admin, SourceType: models.SourceTypeLocal,
		Status: models.PickStatusPublished, PublishedAt: sql.NullString{String: "2026-10-01T00:00:00Z", Valid: true}})
	if front, _ := picks.ListFrontpage(1); len(front) != 0 {
		t.Fatal("pick in a hidden category on the frontpage")
	}
	if err := picks.SetExtraCategories(id, []int64{retro, hidden}); err != nil {
		t.Fatal(err)
	}
	if ex, _ := picks.ExtraCategories(id); len(ex[id]) != 1 || ex[id][0] != retro {
		t.Fatalf("extras = %v, want only retro (main category isn't stored as extra)", ex[id])
	}
	if front, _ := picks.ListFrontpage(1); len(front) != 1 {
		t.Error("pick with a shown extra category missing from the frontpage")
	}
	if inRetro, _ := picks.ListCategoryUnpinned(retro, 1); len(inRetro) != 1 {
		t.Error("pick missing from its extra category's page")
	}
	if inNews, _ := picks.ListPublished(&news, nil, 1); len(inNews) != 0 {
		t.Error("pick shows in a category it doesn't have")
	}
	picks.AddCategories(id, []int64{news, retro})
	if ex, _ := picks.ExtraCategories(id); len(ex[id]) != 2 {
		t.Errorf("after AddCategories extras = %v, want retro + news", ex[id])
	}
	if err := cats.Delete(news, false); err != nil {
		t.Errorf("deleting a category used only as an extra: %v", err)
	}
	if ex, _ := picks.ExtraCategories(id); len(ex[id]) != 1 {
		t.Errorf("extra link not removed with its category: %v", ex[id])
	}

	srcID, _ := sources.Create(&models.Source{Platform: models.PlatformLemmy, InstanceURL: "https://l.example", Mode: models.SourceModeManual, PollIntervalSec: 900})
	sources.SetExtraCategories(srcID, []int64{retro, hidden})
	if ex, _ := sources.ExtraCategories(srcID); len(ex) != 2 {
		t.Errorf("source extras = %v", ex)
	}
	if err := sources.Delete(srcID); err != nil {
		t.Errorf("deleting a source with extra categories: %v", err)
	}
}
