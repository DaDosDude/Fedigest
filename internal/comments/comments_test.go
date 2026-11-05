package comments

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fedigest/internal/adapters"
	fedigestdb "fedigest/internal/db"
	"fedigest/internal/models"
)

// fakeAdapter lets tests control exactly what a "live fetch" returns,
// without any real network access.
type fakeAdapter struct {
	comments adapters.CommentTree
	err      error
	calls    int
}

func (f *fakeAdapter) FetchMetadata(ctx context.Context, postURL string) (adapters.Metadata, error) {
	return adapters.Metadata{}, nil
}
func (f *fakeAdapter) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	f.calls++
	if f.err != nil {
		return adapters.CommentTree{}, f.err
	}
	return f.comments, nil
}
func (f *fakeAdapter) FetchCommentCount(ctx context.Context, postURL string) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.comments.Total, nil
}
func (f *fakeAdapter) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	return nil, nil
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

func makeFediPick(t *testing.T, conn *sql.DB) *models.Pick {
	t.Helper()
	users := &models.UserStore{DB: conn}
	cats := &models.CategoryStore{DB: conn}
	picks := &models.PickStore{DB: conn}

	userID, err := users.Create(&models.User{Username: "tester", PasswordHash: "x", Role: models.RoleAdmin})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	catID, err := cats.Create("Tech", "tech", 0, true)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	p := &models.Pick{
		Slug:           "test-pick",
		Title:          "Test pick",
		Excerpt:        "e",
		Body:           "b",
		CategoryID:     catID,
		CuratorID:      userID,
		SourceType:     models.SourceTypeFedi,
		SourcePlatform: sql.NullString{String: "lemmy", Valid: true},
		OriginalURL:    sql.NullString{String: "https://example.com/post/1", Valid: true},
		Status:         models.PickStatusPublished,
	}
	id, err := picks.Create(p)
	if err != nil {
		t.Fatalf("create pick: %v", err)
	}
	got, err := picks.GetByID(id)
	if err != nil {
		t.Fatalf("get pick: %v", err)
	}
	return got
}

func TestGetCommentsLiveFetchOnEmptyCache(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)

	fake := &fakeAdapter{comments: adapters.CommentTree{
		Comments: []adapters.Comment{{Author: "alice", Body: "hi"}},
		Total:    1,
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	svc := New(picks, registry, time.Minute)
	result := svc.GetComments(context.Background(), pick)

	if result.Total != 1 {
		t.Errorf("Total = %d, want 1", result.Total)
	}
	if result.Stale {
		t.Error("expected fresh result, got Stale=true")
	}
	if fake.calls != 1 {
		t.Errorf("expected 1 adapter call, got %d", fake.calls)
	}
}

func TestGetCommentsUsesCacheWithinTTL(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)

	fake := &fakeAdapter{comments: adapters.CommentTree{
		Comments: []adapters.Comment{{Author: "alice", Body: "hi"}},
		Total:    1,
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	svc := New(picks, registry, time.Hour) // long TTL — second call should hit cache

	svc.GetComments(context.Background(), pick)
	refetched, err := picks.GetByID(pick.ID)
	if err != nil {
		t.Fatal(err)
	}
	result2 := svc.GetComments(context.Background(), refetched)

	if fake.calls != 1 {
		t.Errorf("expected only 1 live call (second should be cached), got %d", fake.calls)
	}
	if result2.Total != 1 {
		t.Errorf("Total = %d, want 1 from cache", result2.Total)
	}
}

func TestGetCommentsFallsBackToCacheOnFailure(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)

	// First call succeeds and populates the cache.
	fake := &fakeAdapter{comments: adapters.CommentTree{
		Comments: []adapters.Comment{{Author: "alice", Body: "hi"}},
		Total:    1,
	}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	svc := New(picks, registry, 0) // zero TTL — always attempts a live fetch
	svc.GetComments(context.Background(), pick)

	// Now make the adapter fail and confirm we fall back to the cache
	// instead of returning empty/erroring out.
	fake.err = errors.New("instance unreachable")
	refetched, err := picks.GetByID(pick.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := svc.GetComments(context.Background(), refetched)

	if !result.Stale {
		t.Error("expected Stale=true on fallback")
	}
	if result.Total != 1 {
		t.Errorf("Total = %d, want 1 (from cache)", result.Total)
	}
	if len(result.Comments) != 1 {
		t.Errorf("expected cached comments to survive the failure, got %d", len(result.Comments))
	}
}

func TestSourceUnavailableAfterThreshold(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)

	fake := &fakeAdapter{err: errors.New("always fails")}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)

	svc := New(picks, registry, 0)

	// No cache exists yet, so every failed attempt returns an empty,
	// Unavailable-flagged result until the threshold trips.
	var last Result
	for i := 0; i < models.CommentFailureThreshold; i++ {
		refetched, err := picks.GetByID(pick.ID)
		if err != nil {
			t.Fatal(err)
		}
		last = svc.GetComments(context.Background(), refetched)
	}

	if !last.Unavailable {
		t.Error("expected Unavailable=true after crossing the failure threshold")
	}

	// And the DB itself should agree, independent of the in-flight result.
	final, err := picks.GetByID(pick.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.SourceUnavailable {
		t.Error("expected source_unavailable=1 in the database after the threshold")
	}
}

func TestRateLimitIsNotAFailure(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)

	fake := &fakeAdapter{err: &adapters.StatusError{Code: 400, Message: "rate_limit_error"}}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	svc := New(picks, registry, 0)

	for i := 0; i < models.CommentFailureThreshold+1; i++ {
		p, _ := picks.GetByID(pick.ID)
		svc.GetComments(context.Background(), p)
		if err := svc.RefreshCommentCount(context.Background(), p); err == nil {
			t.Fatal("expected the rate limit error back")
		}
	}
	final, _ := picks.GetByID(pick.ID)
	if final.ConsecutiveFailures != 0 || final.SourceUnavailable {
		t.Errorf("rate limits counted as failures: %d, unavailable %v", final.ConsecutiveFailures, final.SourceUnavailable)
	}
}
