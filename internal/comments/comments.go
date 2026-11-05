// Package comments fetches read-only comment threads live with a short TTL
// cache, falling back to the last cached thread on failure.
package comments

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/url"
	"sync"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

// Result is a freshly fetched or cached comment thread.
type Result struct {
	Comments    []adapters.Comment
	Total       int
	Stale       bool      // true if this came from cache after a live-fetch failure
	UpdatedAt   time.Time // when this data was actually fetched (not necessarily now)
	Unavailable bool      // true once the pick has crossed the failure threshold
}

type Service struct {
	Picks    *models.PickStore
	Adapters *adapters.Registry
	TTL      time.Duration

	// hostPlatform remembers which platform last answered for a community
	// instance host, so it's tried first next time.
	hostPlatform sync.Map

	mu sync.Mutex
	// inflight holds the live fetch running for each pick, so visitors
	// arriving at the same time share one fetch.
	inflight map[int64]*fetchCall
	// lastFailed is when each pick's last live fetch failed; it isn't
	// retried (or counted again) until the TTL has passed.
	lastFailed map[int64]time.Time

	// countTried is when the background refresh last asked about each
	// pick's count, successful or not (only that goroutine uses it).
	countTried map[int64]time.Time
}

type fetchCall struct {
	done   chan struct{}
	result Result
}

// fetchTimeout bounds one live fetch across all of a pick's sources.
const fetchTimeout = 20 * time.Second

// commentSource is one place a pick's comments can be fetched from.
type commentSource struct {
	platform models.Platform
	url      string
	host     string // set for the community-instance source, to remember what worked
}

// sources lists where to fetch a pick's comments, best first. The
// community instance comes first since every comment gathers there; its
// software is unknown, so both Lemmy and PieFed are tried. The pick's own
// URL and platform are the fallback.
func (s *Service) sources(pick *models.Pick) []commentSource {
	var out []commentSource
	if pick.CommunityPostURL.Valid && pick.CommunityPostURL.String != "" {
		if u, err := url.Parse(pick.CommunityPostURL.String); err == nil && u.Host != "" {
			order := []models.Platform{models.PlatformLemmy, models.PlatformPieFed}
			if known, ok := s.hostPlatform.Load(u.Host); ok && known.(models.Platform) == models.PlatformPieFed {
				order = []models.Platform{models.PlatformPieFed, models.PlatformLemmy}
			}
			for _, pl := range order {
				out = append(out, commentSource{platform: pl, url: pick.CommunityPostURL.String, host: u.Host})
			}
		}
	}
	return append(out, commentSource{platform: platformOf(pick), url: pick.OriginalURL.String})
}

// trySources runs fetch against each source in order, returning the first
// success (and remembering which platform a community instance runs). A
// host that can't be reached at all isn't tried again with the other
// platform.
func (s *Service) trySources(pick *models.Pick, fetch func(adapters.Adapter, string) error) error {
	var errs []error
	unreachable := map[string]bool{}
	for _, src := range s.sources(pick) {
		if src.host != "" && unreachable[src.host] {
			continue
		}
		adapter, err := s.Adapters.For(src.platform)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := fetch(adapter, src.url); err != nil {
			errs = append(errs, err)
			// A host that can't be reached, or says it's getting too many
			// requests, isn't asked again with the other platform.
			var netErr net.Error
			if limited, _ := adapters.RateLimit(err); src.host != "" && (errors.As(err, &netErr) || limited) {
				unreachable[src.host] = true
			}
			continue
		}
		if src.host != "" {
			s.hostPlatform.Store(src.host, src.platform)
		}
		return nil
	}
	return errors.Join(errs...)
}

func New(picks *models.PickStore, registry *adapters.Registry, ttl time.Duration) *Service {
	return &Service{Picks: picks, Adapters: registry, TTL: ttl}
}

// GetComments returns the comment thread for a fedi pick, fetching live if
// the cache is stale (or empty) and falling back to the cache on failure.
// Callers should only call this for source_type == "fedi" picks.
//
// The fetch doesn't stop when the visitor leaves (so a slow instance isn't
// counted as failing), visitors arriving together share one fetch, and
// after a failure the cache is served without retrying until the TTL has
// passed.
func (s *Service) GetComments(ctx context.Context, pick *models.Pick) Result {
	if s.isFresh(pick) {
		return s.cachedResult(pick, false)
	}
	if s.failedRecently(pick.ID) {
		return s.cachedResult(pick, true)
	}

	s.mu.Lock()
	if s.inflight == nil {
		s.inflight = map[int64]*fetchCall{}
	}
	if c, ok := s.inflight[pick.ID]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.result
		case <-ctx.Done():
			return s.cachedResult(pick, true)
		}
	}
	c := &fetchCall{done: make(chan struct{})}
	s.inflight[pick.ID] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, pick.ID)
		s.mu.Unlock()
		close(c.done)
	}()

	c.result = s.fetchAndStore(context.WithoutCancel(ctx), pick)
	return c.result
}

// CachedComments returns the cached thread without fetching, for requests
// that shouldn't cause one (HEAD requests, crawlers, link previews).
func (s *Service) CachedComments(pick *models.Pick) Result {
	return s.cachedResult(pick, !s.isFresh(pick))
}

func (s *Service) fetchAndStore(ctx context.Context, pick *models.Pick) Result {
	live, err := s.fetchLive(ctx, pick)
	if err != nil {
		log.Printf("comment fetch failed for pick %d: %v", pick.ID, err)
		s.mu.Lock()
		if s.lastFailed == nil {
			s.lastFailed = map[int64]time.Time{}
		}
		s.lastFailed[pick.ID] = time.Now()
		s.mu.Unlock()
		result := s.cachedResult(pick, true)
		result.Unavailable = pick.SourceUnavailable
		// Being told to slow down says nothing about the post being gone.
		if limited, _ := adapters.RateLimit(err); !limited {
			if recErr := s.Picks.RecordCommentFetchFailure(pick.ID); recErr != nil {
				log.Printf("recording comment fetch failure for pick %d: %v", pick.ID, recErr)
			}
			// pick predates the failure just recorded, so recompute the threshold.
			result.Unavailable = pick.SourceUnavailable || pick.ConsecutiveFailures+1 >= models.CommentFailureThreshold
		}
		return result
	}

	s.mu.Lock()
	delete(s.lastFailed, pick.ID)
	s.mu.Unlock()

	cacheJSON, err := json.Marshal(live.Comments)
	if err != nil {
		log.Printf("marshalling comment cache for pick %d: %v", pick.ID, err)
	} else if err := s.Picks.SaveCommentsSuccess(pick.ID, live.Total, string(cacheJSON)); err != nil {
		log.Printf("saving comment cache for pick %d: %v", pick.ID, err)
	}

	return Result{Comments: live.Comments, Total: live.Total, UpdatedAt: time.Now()}
}

// failedRecently reports whether the pick's last live fetch failed less
// than a TTL ago.
func (s *Service) failedRecently(pickID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.lastFailed[pickID]
	return ok && time.Since(t) < s.TTL
}

// RefreshCommentCount updates a pick's comment count without fetching the tree.
func (s *Service) RefreshCommentCount(ctx context.Context, pick *models.Pick) error {
	var count int
	err := s.trySources(pick, func(a adapters.Adapter, u string) error {
		c, err := a.FetchCommentCount(ctx, u)
		count = c
		return err
	})
	if err != nil {
		if limited, _ := adapters.RateLimit(err); limited {
			return err // not a sign the post is gone
		}
		if recErr := s.Picks.RecordCommentFetchFailure(pick.ID); recErr != nil {
			return recErr
		}
		return err
	}
	return s.Picks.SaveCommentCountSuccess(pick.ID, count)
}

func (s *Service) isFresh(pick *models.Pick) bool {
	if !pick.CommentsCacheUpdatedAt.Valid {
		return false
	}
	t, err := time.Parse(time.RFC3339, normalizeSQLiteTime(pick.CommentsCacheUpdatedAt.String))
	if err != nil {
		return false
	}
	return time.Since(t) < s.TTL
}

func (s *Service) cachedResult(pick *models.Pick, stale bool) Result {
	var list []adapters.Comment
	if pick.CommentsCache.Valid {
		if err := json.Unmarshal([]byte(pick.CommentsCache.String), &list); err != nil {
			log.Printf("unmarshalling comment cache for pick %d: %v", pick.ID, err)
		}
	}
	var updatedAt time.Time
	if pick.CommentsCacheUpdatedAt.Valid {
		updatedAt, _ = time.Parse(time.RFC3339, normalizeSQLiteTime(pick.CommentsCacheUpdatedAt.String))
	}
	total := 0
	if pick.CommentCount.Valid {
		total = int(pick.CommentCount.Int64)
	}
	return Result{
		Comments:    list,
		Total:       total,
		Stale:       stale && pick.CommentsCache.Valid, // nothing cached: nothing to call stale
		UpdatedAt:   updatedAt,
		Unavailable: pick.SourceUnavailable,
	}
}

func (s *Service) fetchLive(ctx context.Context, pick *models.Pick) (adapters.CommentTree, error) {
	ctx, cancelAll := context.WithTimeout(ctx, fetchTimeout)
	defer cancelAll()
	var tree adapters.CommentTree
	err := s.trySources(pick, func(a adapters.Adapter, u string) error {
		fetchCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		t, err := a.FetchComments(fetchCtx, u)
		tree = t
		return err
	})
	return tree, err
}

func platformOf(pick *models.Pick) models.Platform {
	if pick.SourcePlatform.Valid {
		return models.Platform(pick.SourcePlatform.String)
	}
	return ""
}

// normalizeSQLiteTime converts SQLite's datetime('now') format
// ("2026-09-23 10:00:00") to RFC3339 so time.Parse accepts it.
func normalizeSQLiteTime(s string) string {
	if len(s) == 19 && s[10] == ' ' {
		return s[:10] + "T" + s[11:] + "Z"
	}
	return s
}
