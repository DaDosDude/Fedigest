package web

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

func queuePick(id int64, original, community string) *models.Pick {
	return &models.Pick{ID: id, SourcePlatform: sql.NullString{String: "lemmy", Valid: true},
		OriginalURL:      sql.NullString{String: original, Valid: true},
		CommunityPostURL: sql.NullString{String: community, Valid: community != ""}}
}

func TestRefreshQueue(t *testing.T) {
	a1 := queuePick(1, "https://a.example/post/1", "")
	a2 := queuePick(2, "https://a.example/post/2", "")
	b1 := queuePick(3, "https://b.example/post/1", "")
	q := newRefreshQueue([]*models.Pick{a1, a2, b1})
	t0 := time.Unix(0, 0)

	p, _ := q.next(t0)
	if p != a1 {
		t.Fatalf("first: %v", p.ID)
	}
	q.asked("a.example", t0)
	// a.example was just asked, so b.example goes first.
	if p, _ = q.next(t0); p != b1 {
		t.Fatalf("second: %v", p.ID)
	}
	q.asked("b.example", t0)
	if p, wait := q.next(t0); p != nil || wait != refreshPickDelay {
		t.Fatalf("expected a wait of %v, got %v / %v", refreshPickDelay, p, wait)
	}

	// a.example asks to slow down: a2 waits a minute, then 2, then 5.
	t1 := t0.Add(refreshPickDelay)
	if p, _ = q.next(t1); p != a2 {
		t.Fatalf("third: %v", p)
	}
	q.limitedHost("a.example", t1, 0)
	if !q.retry(a2) {
		t.Fatal("a2 wasn't put back")
	}
	if host, wait := q.longestPause(t1); host != "a.example" || wait != time.Minute {
		t.Errorf("longestPause = %q %v", host, wait)
	}
	if p, wait := q.next(t1); p != nil || wait != time.Minute {
		t.Fatalf("expected a minute's wait, got %v / %v", p, wait)
	}
	t2 := t1.Add(time.Minute)
	if p, _ = q.next(t2); p != a2 {
		t.Fatal("a2 not retried after the pause")
	}
	if host, _ := q.longestPause(t2); host != "" {
		t.Errorf("pause still reported: %q", host)
	}
	q.asked("a.example", t2)
	if q.ready["a.example"] != t2.Add(2*refreshPickDelay) {
		t.Errorf("spacing after a limit: %v", q.ready["a.example"].Sub(t2))
	}
	q.limitedHost("a.example", t2, 0)
	q.retry(a2)
	if _, wait := q.next(t2); wait != 2*time.Minute {
		t.Errorf("second pause %v", wait)
	}
	q.next(t2.Add(time.Hour))
	q.limitedHost("a.example", t2, 0)
	q.retry(a2)
	if _, wait := q.next(t2); wait != 5*time.Minute {
		t.Errorf("third pause %v", wait)
	}
	// Retries run out.
	q.next(t2.Add(time.Hour))
	if q.retry(a2) || q.len() != 0 {
		t.Error("a2 put back after its last retry")
	}
}

func TestRefreshQueueUsesOtherCopies(t *testing.T) {
	// a.example is paused, but the post also lives on c.example.
	p := queuePick(1, "https://a.example/post/1", "https://c.example/post/9")
	q := newRefreshQueue([]*models.Pick{p})
	t0 := time.Unix(0, 0)
	q.limitedHost("a.example", t0, 0)
	if got, _ := q.next(t0); got != p {
		t.Fatal("pick held back although another copy can be asked")
	}
}

// fakeMeta answers FetchMetadata per URL.
type fakeMeta struct {
	answers map[string]error
	calls   []string
}

func (f *fakeMeta) FetchMetadata(_ context.Context, u string) (adapters.Metadata, error) {
	f.calls = append(f.calls, u)
	if err, ok := f.answers[u]; ok && err != nil {
		return adapters.Metadata{}, err
	}
	return adapters.Metadata{Title: "found at " + u}, nil
}
func (f *fakeMeta) FetchComments(context.Context, string) (adapters.CommentTree, error) {
	return adapters.CommentTree{}, nil
}
func (f *fakeMeta) FetchCommentCount(context.Context, string) (int, error) { return 0, nil }
func (f *fakeMeta) Poll(context.Context, models.Source) ([]adapters.Metadata, error) {
	return nil, nil
}

func TestFetchPickMetadataFallsBack(t *testing.T) {
	limited := &adapters.StatusError{Code: 400, Message: "rate_limit_error"}
	lemmy := &fakeMeta{answers: map[string]error{"https://a.example/post/1": limited}}
	piefed := &fakeMeta{answers: map[string]error{"https://c.example/post/9": adapters.ErrNotFound}}
	reg := adapters.NewRegistry()
	reg.Register(models.PlatformLemmy, lemmy)
	reg.Register(models.PlatformPieFed, piefed)
	reg.Register(models.PlatformMbin, &fakeMeta{answers: map[string]error{"https://c.example/post/9": adapters.ErrNotFound}})
	s := &Server{Adapters: reg}

	p := queuePick(1, "https://a.example/post/1", "https://c.example/post/9")
	known := map[string]models.Platform{}
	res := s.fetchPickMetadata(p, metadataSources(p), func(string) bool { return true }, known)
	if !res.fetched || res.meta.Title != "found at https://c.example/post/9" {
		t.Fatalf("not fetched from the community copy: %+v", res)
	}
	if len(res.limited) != 1 || res.limited[0] != "a.example" || len(res.asked) != 2 {
		t.Errorf("limited %v, asked %v", res.limited, res.asked)
	}
	if known["c.example"] != models.PlatformLemmy {
		t.Errorf("platform not remembered: %v", known)
	}

	// With a.example paused, it isn't asked at all.
	lemmy.calls = nil
	res = s.fetchPickMetadata(p, metadataSources(p), func(h string) bool { return h != "a.example" }, known)
	if !res.fetched || !res.skipped || len(lemmy.calls) != 1 || lemmy.calls[0] != "https://c.example/post/9" {
		t.Errorf("paused host asked: %v (skipped %v)", lemmy.calls, res.skipped)
	}

	// Nothing answers: the reasons are kept.
	lemmy.answers["https://c.example/post/9"] = adapters.ErrNotFound
	res = s.fetchPickMetadata(p, metadataSources(p), func(string) bool { return true }, known)
	if res.fetched || res.err == nil {
		t.Errorf("expected a failure, got %+v", res)
	}
}

func TestWaitText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Second: "1 second", 40 * time.Second: "40 seconds", 59 * time.Second: "59 seconds",
		59500 * time.Millisecond: "1 minute", time.Minute: "1 minute", 89 * time.Second: "1 minute", 5 * time.Minute: "5 minutes",
	} {
		if got := waitText(d); got != want {
			t.Errorf("waitText(%v) = %q, want %q", d, got, want)
		}
	}
}
