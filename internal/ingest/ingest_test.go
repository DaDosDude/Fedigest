package ingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"fedigest/internal/adapters"
	fedigestdb "fedigest/internal/db"
	"fedigest/internal/models"
)

// fakePollAdapter returns a fixed poll result.
type fakePollAdapter struct {
	items []adapters.Metadata
	err   error
	calls int
}

func (f *fakePollAdapter) FetchMetadata(ctx context.Context, postURL string) (adapters.Metadata, error) {
	return adapters.Metadata{}, nil
}
func (f *fakePollAdapter) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	return adapters.CommentTree{}, nil
}
func (f *fakePollAdapter) FetchCommentCount(ctx context.Context, postURL string) (int, error) {
	return 0, nil
}
func (f *fakePollAdapter) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := fedigestdb.Open(dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		os.RemoveAll(dir)
	})
	return conn
}

// setupSource creates the user/category a source needs, plus the source
// itself, and returns everything a test typically wants.
func setupSource(t *testing.T, conn *sql.DB, mode models.SourceMode) (*models.SourceStore, *models.PickStore, *models.Source) {
	t.Helper()
	users := &models.UserStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}
	sources := &models.SourceStore{DB: conn}
	picks := &models.PickStore{DB: conn}

	userID, err := users.Create(&models.User{Username: "curator", PasswordHash: "x", Role: models.RoleAdmin})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	catID, err := cats.Create("Tech", "tech", 0, true)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	srcID, err := sources.Create(&models.Source{
		Platform:          models.PlatformLemmy,
		InstanceURL:       "https://lemmy.example",
		WatchTarget:       sql.NullString{String: "technology", Valid: true},
		Mode:              mode,
		PollIntervalSec:   900,
		DefaultCategoryID: sql.NullInt64{Int64: catID, Valid: true},
		CreatedByUserID:   sql.NullInt64{Int64: userID, Valid: true},
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	src, err := sources.GetByID(srcID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	return sources, picks, src
}

func TestPollOneCreatesDraftForAutoReview(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)

	fake := &fakePollAdapter{items: []adapters.Metadata{
		{Title: "New post", Body: "Short opening.\n\nSecond paragraph.\n\nThird paragraph.", CommentURL: "https://lemmy.example/post/1"},
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)

	all, err := picks.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 pick created, got %d", len(all))
	}
	if all[0].Status != models.PickStatusDraft {
		t.Errorf("auto-review pick should be a draft, got %s", all[0].Status)
	}
	if !all[0].UseOriginalAsBody {
		t.Error("expected use_original_as_body=true")
	}
	if all[0].Excerpt != "Short opening.\n\nSecond paragraph." || !all[0].ExcerptAuto {
		t.Errorf("excerpt = %q (auto %v), want the opening paragraphs", all[0].Excerpt, all[0].ExcerptAuto)
	}
	if all[0].OriginalURL.String != "https://lemmy.example/post/1" {
		t.Errorf("original_url = %q", all[0].OriginalURL.String)
	}

	refreshed, err := sources.GetByID(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.LastPolledAt.Valid || refreshed.ConsecutiveFailures != 0 {
		t.Errorf("expected poll success recorded, got last_polled_at=%v failures=%d", refreshed.LastPolledAt, refreshed.ConsecutiveFailures)
	}
}

func TestPollOnePublishesForAutoPublish(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoPublish)

	fake := &fakePollAdapter{items: []adapters.Metadata{
		{Title: "New post", Body: "body text", CommentURL: "https://lemmy.example/post/2"},
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)

	all, err := picks.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 pick created, got %d", len(all))
	}
	if all[0].Status != models.PickStatusPublished {
		t.Errorf("auto-publish pick should be published, got %s", all[0].Status)
	}
	if !all[0].PublishedAt.Valid {
		t.Error("expected published_at to be set")
	}
}

func TestPollOneDedupsAgainstExistingPick(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)

	item := adapters.Metadata{Title: "Seen already", CommentURL: "https://lemmy.example/post/3"}
	fake := &fakePollAdapter{items: []adapters.Metadata{item}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)
	poller.pollOne(context.Background(), src) // second poll sees the same item again

	all, err := picks.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected dedup to prevent a second pick, got %d picks", len(all))
	}
}

// TestPollOneSkipsItemsOlderThanSource checks that items predating the
// source aren't backfilled.
func TestPollOneSkipsItemsOlderThanSource(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)
	src.CreatedAt = "2026-06-15 12:00:00" // source set up on this date

	older := adapters.Metadata{Title: "Old post from before this source existed", CommentURL: "https://lemmy.example/post/old", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	newer := adapters.Metadata{Title: "New post after the source was added", CommentURL: "https://lemmy.example/post/new", CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}
	unknown := adapters.Metadata{Title: "Post with unparseable/zero timestamp", CommentURL: "https://lemmy.example/post/unknown"} // CreatedAt left zero

	fake := &fakePollAdapter{items: []adapters.Metadata{older, newer, unknown}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)

	all, err := picks.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 picks (newer + unknown-timestamp, old one skipped), got %d", len(all))
	}
	urls := map[string]bool{}
	for _, p := range all {
		urls[p.OriginalURL.String] = true
	}
	if urls["https://lemmy.example/post/old"] {
		t.Error("a post older than the source's own created_at should have been skipped as pre-existing history")
	}
	if !urls["https://lemmy.example/post/new"] {
		t.Error("expected the post newer than the source to be ingested")
	}
	if !urls["https://lemmy.example/post/unknown"] {
		t.Error("a post with an unparseable/zero CreatedAt should fail open and still be ingested")
	}
}

func TestPollOneRecordsFailure(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)

	fake := &fakePollAdapter{err: errors.New("instance unreachable")}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)

	refreshed, err := sources.GetByID(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ConsecutiveFailures != 1 {
		t.Errorf("consecutive_failures = %d, want 1", refreshed.ConsecutiveFailures)
	}

	all, err := picks.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected no picks created on a failed poll, got %d", len(all))
	}
}

func TestSourceFlaggedUnavailableAfterThreshold(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)

	fake := &fakePollAdapter{err: errors.New("always fails")}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0
	for i := 0; i < models.SourceFailureThreshold; i++ {
		refetched, err := sources.GetByID(src.ID)
		if err != nil {
			t.Fatal(err)
		}
		poller.pollOne(context.Background(), refetched)
	}

	final, err := sources.GetByID(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.SourceUnavailable {
		t.Error("expected source_unavailable=1 after crossing the failure threshold")
	}
}

func TestDueRespectsIntervalAndBackoff(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, _ := setupSource(t, conn, models.SourceModeAutoReview)
	registry := adapters.NewRegistry()
	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil // no network lookups in tests
	poller.SettleDelay = 0

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	poller.Now = func() time.Time { return now }

	// Never polled -> always due.
	fresh := &models.Source{PollIntervalSec: 900}
	if !poller.due(fresh) {
		t.Error("a source with no LastPolledAt should be due immediately")
	}

	// Polled 10 minutes ago with a 15-minute interval and no failures -> not due yet.
	recent := &models.Source{
		PollIntervalSec: 900,
		LastPolledAt:    sql.NullString{String: now.Add(-10 * time.Minute).Format("2006-01-02 15:04:05"), Valid: true},
	}
	if poller.due(recent) {
		t.Error("expected not due — within the plain interval")
	}

	// Polled 10 minutes ago but with 2 consecutive failures -> effective
	// interval is 900*2^2=3600s (1h), so still not due.
	backedOff := &models.Source{
		PollIntervalSec:     900,
		ConsecutiveFailures: 2,
		LastPolledAt:        sql.NullString{String: now.Add(-10 * time.Minute).Format("2006-01-02 15:04:05"), Valid: true},
	}
	if poller.due(backedOff) {
		t.Error("expected not due — backoff should have stretched the interval")
	}

	// Polled over an hour ago -> due again once the interval (or its
	// capped backoff) has fully elapsed.
	overdue := &models.Source{
		PollIntervalSec: 900,
		LastPolledAt:    sql.NullString{String: now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05"), Valid: true},
	}
	if !poller.due(overdue) {
		t.Error("expected due — well past the interval")
	}
}

func TestPollOneWaitsForNewPostsToSettle(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoPublish)

	now := time.Now()
	fake := &fakePollAdapter{items: []adapters.Metadata{
		{Title: "Brand new", CommentURL: "https://lemmy.example/post/new", CreatedAt: now.Add(-1 * time.Minute)},
		{Title: "Settled", CommentURL: "https://lemmy.example/post/old", CreatedAt: now.Add(-11 * time.Minute)},
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil
	// The source was just created, so both items would otherwise count as
	// pre-existing history; move its baseline back.
	if _, err := conn.Exec(`UPDATE sources SET created_at = datetime('now', '-1 hour') WHERE id = ?`, src.ID); err != nil {
		t.Fatal(err)
	}
	src, _ = sources.GetByID(src.ID)
	poller.pollOne(context.Background(), src)

	all, _ := picks.ListAll()
	if len(all) != 1 || all[0].Title != "Settled" {
		t.Fatalf("expected only the settled post, got %d picks", len(all))
	}

	// Ten minutes later the new one is picked up (once).
	poller.Now = func() time.Time { return now.Add(10 * time.Minute) }
	poller.pollOne(context.Background(), src)
	all, _ = picks.ListAll()
	if len(all) != 2 {
		t.Fatalf("expected the new post on a later poll, got %d picks", len(all))
	}
}

// TestOverlappingSourcesMergeCategories: a second source finding the same
// post adds its categories to the existing pick instead of skipping it.
func TestOverlappingSourcesMergeCategories(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoPublish)
	cats := &models.CategoryStore{DB: conn}
	retro, _ := cats.Create("Retro", "retro", 1, true)
	local, _ := cats.Create("Local", "local", 2, true)
	sources.SetExtraCategories(src.ID, []int64{retro})

	id2, err := sources.Create(&models.Source{Platform: models.PlatformLemmy, InstanceURL: "https://lemmy.example", WatchType: models.WatchTypeInstance,
		Mode: models.SourceModeAutoPublish, PollIntervalSec: 900, DefaultCategoryID: sql.NullInt64{Int64: local, Valid: true},
		CreatedByUserID: src.CreatedByUserID})
	if err != nil {
		t.Fatal(err)
	}
	src2, _ := sources.GetByID(id2)

	fake := &fakePollAdapter{items: []adapters.Metadata{{Title: "Shared", CommentURL: "https://lemmy.example/post/7"}}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil
	poller.SettleDelay = 0

	poller.pollOne(context.Background(), src)
	poller.pollOne(context.Background(), src2)

	all, _ := picks.ListAll()
	if len(all) != 1 {
		t.Fatalf("expected 1 pick, got %d", len(all))
	}
	ex, _ := picks.ExtraCategories(all[0].ID)
	got := map[int64]bool{}
	for _, c := range ex[all[0].ID] {
		got[c] = true
	}
	if !got[retro] || !got[local] || len(got) != 2 {
		t.Errorf("extra categories = %v, want retro (first source's extra) and local (second source)", ex[all[0].ID])
	}

	// With the setting off, the second source just skips it.
	poller.MultiCategory = func() bool { return false }
	other, _ := cats.Create("Other", "other", 3, true)
	sources.Update(&models.Source{ID: id2, Platform: models.PlatformLemmy, InstanceURL: "https://lemmy.example", WatchType: models.WatchTypeInstance,
		Mode: models.SourceModeAutoPublish, PollIntervalSec: 900, DefaultCategoryID: sql.NullInt64{Int64: other, Valid: true}})
	src2, _ = sources.GetByID(id2)
	poller.pollOne(context.Background(), src2)
	if ex, _ := picks.ExtraCategories(all[0].ID); len(ex[all[0].ID]) != 2 {
		t.Errorf("categories changed with the setting off: %v", ex[all[0].ID])
	}
}

// TestPollOneLookBack: ingest_since moves the baseline back, so posts made
// shortly before the source was created are added too.
func TestPollOneLookBack(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)
	src.CreatedAt = "2026-06-15 12:00:00"
	src.IngestSince = sql.NullString{String: "2026-06-15T09:00:00Z", Valid: true} // 3 hours back

	within := adapters.Metadata{Title: "Two hours before", CommentURL: "https://lemmy.example/post/a", CreatedAt: time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)}
	before := adapters.Metadata{Title: "Five hours before", CommentURL: "https://lemmy.example/post/b", CreatedAt: time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)}
	fake := &fakePollAdapter{items: []adapters.Metadata{within, before}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)

	all, _ := picks.ListAll()
	if len(all) != 1 || all[0].Title != "Two hours before" {
		t.Fatalf("got %d picks, want only the post inside the look-back window", len(all))
	}
}

func TestPollOneSkipsExcludedCommunities(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)
	src.WatchType = models.WatchTypeInstance
	src.ExcludeCommunities = sql.NullString{String: models.NormalizeCommunityList("Memes\nnews@lemmy.example"), Valid: true}
	if err := sources.Update(src); err != nil {
		t.Fatal(err)
	}
	src, _ = sources.GetByID(src.ID)

	fake := &fakePollAdapter{items: []adapters.Metadata{
		{Title: "a", CommentURL: "https://lemmy.example/post/10", CommunityHandle: "memes@lemmy.example"},
		{Title: "b", CommentURL: "https://lemmy.example/post/11", CommunityHandle: "news@lemmy.example"},
		{Title: "c", CommentURL: "https://lemmy.example/post/12", CommunityHandle: "news@other.example"},
		{Title: "d", CommentURL: "https://lemmy.example/post/13", CommunityHandle: "retro@lemmy.example"},
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil
	poller.SettleDelay = 0
	poller.pollOne(context.Background(), src)

	all, err := picks.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range all {
		titles = append(titles, p.Title)
	}
	sort.Strings(titles)
	if strings.Join(titles, ",") != "c,d" {
		t.Errorf("picks created: %v, want c,d", titles)
	}
}

func TestRateLimitedPollIsNotAFailure(t *testing.T) {
	conn := setupTestDB(t)
	sources, picks, src := setupSource(t, conn, models.SourceModeAutoReview)
	fake := &fakePollAdapter{err: &adapters.StatusError{Code: 400, Message: "rate_limit_error"}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	poller := NewPoller(sources, picks, registry)
	poller.Resolver = nil
	for i := 0; i < models.SourceFailureThreshold+1; i++ {
		poller.pollOne(context.Background(), src)
	}
	got, err := sources.GetByID(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 0 || got.SourceUnavailable {
		t.Errorf("rate limits counted as failures: %d, unavailable %v", got.ConsecutiveFailures, got.SourceUnavailable)
	}
}
