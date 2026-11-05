package comments

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

// slowAdapter blocks every comment fetch until released, counting calls.
type slowAdapter struct {
	fakeAdapter
	release chan struct{}
	n       atomic.Int32
}

func (s *slowAdapter) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	s.n.Add(1)
	select {
	case <-s.release:
	case <-ctx.Done():
		return adapters.CommentTree{}, ctx.Err()
	}
	return adapters.CommentTree{Comments: []adapters.Comment{{Author: "a", Body: "b"}}, Total: 1}, nil
}

func TestConcurrentVisitorsShareOneFetch(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)
	slow := &slowAdapter{release: make(chan struct{})}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, slow)
	svc := New(picks, registry, time.Minute)

	var wg sync.WaitGroup
	results := make([]Result, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = svc.GetComments(context.Background(), pick)
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(slow.release)
	wg.Wait()
	if n := slow.n.Load(); n != 1 {
		t.Errorf("%d fetches for 5 simultaneous visitors, want 1", n)
	}
	for i, r := range results {
		if r.Total != 1 {
			t.Errorf("visitor %d got %d comments", i, r.Total)
		}
	}
}

func TestVisitorLeavingDoesntCountAsFailure(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)
	slow := &slowAdapter{release: make(chan struct{})}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, slow)
	svc := New(picks, registry, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.GetComments(ctx, pick); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel() // the visitor gives up
	time.Sleep(50 * time.Millisecond)
	close(slow.release)
	<-done
	got, _ := picks.GetByID(pick.ID)
	if got.ConsecutiveFailures != 0 || !got.CommentsCache.Valid {
		t.Errorf("after a cancelled visit: failures %d, cached %v", got.ConsecutiveFailures, got.CommentsCache.Valid)
	}
}

func TestFailureIsNotRetriedWithinTTL(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)
	fake := &fakeAdapter{err: errors.New("down")}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	svc := New(picks, registry, time.Minute)

	for i := 0; i < 5; i++ {
		p, _ := picks.GetByID(pick.ID)
		svc.GetComments(context.Background(), p)
	}
	if fake.calls != 1 {
		t.Errorf("%d fetch attempts within one TTL, want 1", fake.calls)
	}
	got, _ := picks.GetByID(pick.ID)
	if got.ConsecutiveFailures != 1 || got.SourceUnavailable {
		t.Errorf("failures %d, unavailable %v: five views in a minute shouldn't flag the pick", got.ConsecutiveFailures, got.SourceUnavailable)
	}
}

func TestCachedCommentsNeverFetches(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)
	if err := picks.SaveCommentCountSuccess(pick.ID, 7); err != nil {
		t.Fatal(err)
	}
	pick, _ = picks.GetByID(pick.ID)
	fake := &fakeAdapter{}
	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, fake)
	svc := New(picks, registry, time.Minute)
	r := svc.CachedComments(pick)
	if fake.calls != 0 {
		t.Error("CachedComments fetched")
	}
	if r.Total != 7 || r.Stale {
		t.Errorf("got total %d, stale %v; want the stored count, not stale (nothing cached)", r.Total, r.Stale)
	}
}
