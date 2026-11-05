package comments

import (
	"context"
	"log"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

// countRefreshWindow is how far back the background refresh goes: older
// picks rarely get new comments, and their counts are still refreshed
// whenever someone opens the post. Pinned picks are always included.
const countRefreshWindow = 30 * 24 * time.Hour

// countInterval is how often the background refresh updates a pick's
// comment count: every run (10 minutes) for pinned picks and picks
// published in the last day, hourly for the rest of the week, and every 6
// hours after that. Conversations slow down as a post ages, and each run
// otherwise asks the source instances about every recent pick.
func countInterval(p *models.Pick, now time.Time) time.Duration {
	if p.PinnedAt.Valid || !p.PublishedAt.Valid {
		return 0
	}
	published, err := time.Parse(time.RFC3339, normalizeSQLiteTime(p.PublishedAt.String))
	if err != nil {
		return 0
	}
	switch age := now.Sub(published); {
	case age < 24*time.Hour:
		return 0
	case age < 7*24*time.Hour:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

// countSlack lets a pick through a little early, so one that's due a few
// seconds after a run starts isn't left for a whole extra run.
const countSlack = 2 * time.Minute

// countDue reports whether the background refresh should ask about p's
// comment count now: its interval has passed since the count was last
// updated (by this refresh or a post-page view) and since the last try.
func countDue(p *models.Pick, now, tried time.Time) bool {
	interval := countInterval(p, now)
	if interval == 0 {
		return true
	}
	last := tried
	if p.CommentCountUpdatedAt.Valid {
		if t, err := time.Parse(time.RFC3339, normalizeSQLiteTime(p.CommentCountUpdatedAt.String)); err == nil && t.After(last) {
			last = t
		}
	}
	return now.Sub(last) >= interval-countSlack
}

// RunBackgroundCountRefresh periodically refreshes the comment count of
// recent and pinned published fedi picks. It blocks until ctx is cancelled.
func (s *Service) RunBackgroundCountRefresh(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshAllCounts(ctx)
		}
	}
}

func (s *Service) refreshAllCounts(ctx context.Context) {
	picks, err := s.Picks.ListPublishedFedi(time.Now().Add(-countRefreshWindow))
	if err != nil {
		log.Printf("background comment-count refresh: listing picks: %v", err)
		return
	}

	// Background work: requests wait behind visitors and new picks in each
	// instance's queue (adapters/schedule.go), which also keeps them a
	// second apart per instance.
	ctx = adapters.Background(ctx)

	tried := map[int64]time.Time{}
	for _, p := range picks {
		tried[p.ID] = s.countTried[p.ID] // forgets picks no longer listed
	}
	s.countTried = tried

	for _, p := range picks {
		if !countDue(p, time.Now(), tried[p.ID]) {
			continue
		}
		tried[p.ID] = time.Now()
		if err := s.RefreshCommentCount(ctx, p); err != nil && !adapters.NothingSent(err) {
			// (NothingSent: its instances are paused or too busy; it's
			// tried again on a later run.)
			log.Printf("background comment-count refresh: pick %d: %v", p.ID, err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}
