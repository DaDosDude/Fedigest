package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

// metadataRefresh tracks the background "Refresh metadata for all picks"
// run; at most one runs at a time.
type metadataRefresh struct {
	mu       sync.Mutex
	running  bool
	status   string
	finished time.Time // when the last run ended; shown in the reader's time zone
}

func (m *metadataRefresh) get() (bool, string, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running, m.status, m.finished
}

func (m *metadataRefresh) set(running bool, status string) {
	m.mu.Lock()
	m.running = running
	m.status = status
	if !running {
		m.finished = time.Now()
	}
	m.mu.Unlock()
}

// refreshPickDelay is the least time between two requests to the same
// instance: a run often asks one instance about hundreds of posts, and
// Lemmy allows an address only so many requests a minute.
const refreshPickDelay = time.Second

// refreshRetries is how often a pick is put back in the queue after its
// instances said they're getting too many requests (or had to wait).
const refreshRetries = 3

// HandleAdminRefreshAllMetadata re-fetches every fedi pick and fills in
// source-derived fields that are still empty (see
// PickStore.FillSourceMetadata).
func (s *Server) HandleAdminRefreshAllMetadata(w http.ResponseWriter, r *http.Request) {
	s.refresh.mu.Lock()
	if s.refresh.running {
		s.refresh.mu.Unlock()
		http.Redirect(w, r, "/admin/picks", http.StatusSeeOther)
		return
	}
	s.refresh.running = true
	s.refresh.status = "Picks will be updated shortly. Reload this page later to see the result."
	s.refresh.mu.Unlock()

	go s.runMetadataRefresh()
	http.Redirect(w, r, "/admin/picks", http.StatusSeeOther)
}

func (s *Server) runMetadataRefresh() {
	picks, err := s.Picks.ListAll()
	if err != nil {
		log.Printf("metadata refresh: listing picks: %v", err)
		s.refresh.set(false, "failed: couldn't list picks.")
		return
	}
	var fedi []*models.Pick
	for _, p := range picks {
		if p.SourceType == models.SourceTypeFedi && p.OriginalURL.Valid && p.SourcePlatform.Valid {
			fedi = append(fedi, p)
		}
	}

	total := len(fedi)
	q := newRefreshQueue(fedi)
	known := map[string]models.Platform{} // host → platform that answered there
	done, updated, failed := 0, 0, 0
	progress := func() {
		msg := fmt.Sprintf("Updating %d picks in the background (%d done). This can take a few minutes; reload this page later to see the result.", total, done)
		if host, wait := q.longestPause(time.Now()); host != "" {
			msg += fmt.Sprintf(" %s asked to slow down, so its picks wait about %s (or come from other instances) while the others go on.", host, waitText(wait))
		}
		s.refresh.set(true, msg)
	}
	progress()
	for q.len() > 0 {
		now := time.Now()
		p, wait := q.next(now)
		if p == nil {
			time.Sleep(wait) // every pick left is on instances that have to wait
			continue
		}
		res := s.fetchPickMetadata(p, q.sources[p.ID], func(host string) bool { return q.isReady(host, now) }, known)
		now = time.Now()
		for _, h := range res.asked {
			q.asked(h, now)
		}
		for _, h := range res.limited {
			q.limitedHost(h, now, res.retryAfter)
		}
		if res.err != nil {
			log.Printf("metadata refresh: pick %d (%s): %s", p.ID, p.OriginalURL.String, res.err)
		}
		if !res.fetched && (len(res.limited) > 0 || res.skipped) && q.retry(p) {
			progress()
			continue // tried again once one of its instances is ready
		}
		changed := false
		if res.fetched {
			changed = s.saveRefreshedMetadata(p, res.meta)
		} else {
			failed++
		}
		done++
		if s.syncAutoExcerpt(p.ID) {
			changed = true
		}
		if changed {
			updated++
		}
		progress()
	}

	status := fmt.Sprintf("%d picks checked, %d updated", total, updated)
	if failed > 0 {
		status += fmt.Sprintf(", %d couldn't be fetched (see server log)", failed)
	}
	s.refresh.set(false, status+".")
	log.Printf("metadata refresh: %s", status)
}

// metadataSource is one copy of a pick's post to fetch it from.
type metadataSource struct {
	url       string
	host      string
	platforms []models.Platform // tried in this order
}

// metadataSources lists where a pick's post can be fetched: the link it
// was made from (with the pick's own platform), then its copy on the
// community's instance, then on the poster's (the ActivityPub ID). The
// software running those other instances isn't known, so every platform
// is tried there, the pick's own first; a link of the wrong shape for a
// platform costs no request.
func metadataSources(p *models.Pick) []metadataSource {
	own := models.Platform(p.SourcePlatform.String)
	others := []models.Platform{own}
	for _, pl := range []models.Platform{models.PlatformLemmy, models.PlatformPieFed, models.PlatformMbin} {
		if pl != own {
			others = append(others, pl)
		}
	}
	var out []metadataSource
	seen := map[string]bool{}
	add := func(raw string, platforms []models.Platform) {
		u, err := url.Parse(raw)
		if raw == "" || seen[raw] || err != nil || u.Host == "" {
			return
		}
		seen[raw] = true
		out = append(out, metadataSource{url: raw, host: strings.ToLower(u.Host), platforms: platforms})
	}
	add(p.OriginalURL.String, []models.Platform{own})
	add(p.CommunityPostURL.String, others)
	add(p.OriginalApID.String, others)
	return out
}

// metadataResult is what fetchPickMetadata found out.
type metadataResult struct {
	meta       adapters.Metadata
	fetched    bool
	asked      []string      // hosts that were sent a request
	limited    []string      // hosts that said they're getting too many requests
	retryAfter time.Duration // what the first of those asked for, if anything
	skipped    bool          // a source was left out because its host has to wait
	err        error         // why the sources tried failed
}

// fetchPickMetadata fetches a pick's post from the first of its sources
// that answers, leaving out hosts that may not be asked now. A host that
// can't be reached, or says it's getting too many requests, isn't tried
// again with another platform.
func (s *Server) fetchPickMetadata(p *models.Pick, sources []metadataSource, ready func(string) bool, known map[string]models.Platform) metadataResult {
	var res metadataResult
	var errs []string
	for _, src := range sources {
		if !ready(src.host) {
			res.skipped = true
			continue
		}
		platforms := src.platforms
		if k, ok := known[src.host]; ok && len(platforms) > 1 {
			platforms = append([]models.Platform{k}, platforms...) // a repeat is skipped below
		}
		tried := map[models.Platform]bool{}
		askedHost := false
		for _, pl := range platforms {
			if tried[pl] {
				continue
			}
			tried[pl] = true
			adapter, err := s.Adapters.For(pl)
			if err != nil {
				continue
			}
			if !askedHost {
				res.asked = append(res.asked, src.host)
				askedHost = true
			}
			ctx, cancel := context.WithTimeout(adapters.Background(context.Background()), 12*time.Second)
			meta, err := adapter.FetchMetadata(ctx, src.url)
			cancel()
			if err == nil {
				known[src.host] = pl
				res.meta, res.fetched = meta, true
				return res
			}
			errs = append(errs, fmt.Sprintf("%s as %s: %v", src.url, pl, err))
			var paused *adapters.PausedError
			if errors.As(err, &paused) && paused.Busy {
				res.skipped = true // its queue is full for now: try again later
				break
			}
			if limited, wait := adapters.RateLimit(err); limited {
				res.limited = append(res.limited, src.host)
				if res.retryAfter == 0 {
					res.retryAfter = wait
				}
				break
			}
			var netErr net.Error
			if errors.As(err, &netErr) {
				break
			}
		}
	}
	if len(errs) > 0 {
		res.err = errors.New(strings.Join(errs, "; "))
	}
	return res
}

// saveRefreshedMetadata fills a pick's empty source-derived fields from a
// fetch, and reports whether anything changed.
func (s *Server) saveRefreshedMetadata(p *models.Pick, meta adapters.Metadata) bool {
	published := ""
	if !meta.CreatedAt.IsZero() {
		published = meta.CreatedAt.UTC().Format(time.RFC3339)
	}
	communityURL := ""
	if !p.CommunityPostURL.Valid || p.CommunityPostURL.String == "" {
		rctx, rcancel := context.WithTimeout(adapters.Background(context.Background()), 20*time.Second)
		communityURL = adapters.DefaultResolver.CommunityPostURL(rctx, meta.CommunityHandle, meta.CanonicalID())
		rcancel()
	}
	changed, err := s.Picks.FillSourceMetadata(p.ID, meta.AuthorHandle, meta.CommunityHandle, published, meta.LinkURL, meta.Body, meta.CanonicalID(), communityURL, meta.ImageURL)
	if err != nil {
		log.Printf("metadata refresh: saving pick %d: %v", p.ID, err)
		return false
	}
	return changed
}

// refreshQueue hands out the picks of a refresh run so that each instance
// gets at most one request per refreshPickDelay, and an instance that
// asked us to slow down is left alone until its pause is over, while picks
// on other instances (or other copies of its posts) carry on.
type refreshQueue struct {
	picks   []*models.Pick
	sources map[int64][]metadataSource
	ready   map[string]time.Time // host → when it may be asked again
	paused  map[string]time.Time // host → end of the pause it asked for
	limits  map[string]int       // host → times it asked us to slow down
	tries   map[int64]int        // times a pick was put back
}

func newRefreshQueue(picks []*models.Pick) *refreshQueue {
	q := &refreshQueue{picks: append([]*models.Pick(nil), picks...), sources: map[int64][]metadataSource{},
		ready: map[string]time.Time{}, paused: map[string]time.Time{}, limits: map[string]int{}, tries: map[int64]int{}}
	for _, p := range picks {
		q.sources[p.ID] = metadataSources(p)
	}
	return q
}

func (q *refreshQueue) len() int { return len(q.picks) }

func (q *refreshQueue) isReady(host string, now time.Time) bool { return !q.readyAt(host).After(now) }

// readyAt is when an instance may be asked again: after this run's spacing
// and after any pause it asked for (adapters.PausedUntil, shared with the
// rest of the app).
func (q *refreshQueue) readyAt(host string) time.Time {
	t := q.ready[host]
	if until := adapters.PausedUntil(host); until.After(t) {
		t = until
	}
	return t
}

// next takes the first pick with a source on an instance that may be
// asked now; when there's none, it returns nil and how long until one of
// the remaining picks' instances may be asked.
func (q *refreshQueue) next(now time.Time) (*models.Pick, time.Duration) {
	var soonest time.Time
	for i, p := range q.picks {
		srcs := q.sources[p.ID]
		if len(srcs) == 0 {
			q.picks = append(q.picks[:i], q.picks[i+1:]...)
			return p, 0 // nothing to fetch; it's counted as failed
		}
		for _, src := range srcs {
			t := q.readyAt(src.host)
			if !t.After(now) {
				q.picks = append(q.picks[:i], q.picks[i+1:]...)
				return p, 0
			}
			if soonest.IsZero() || t.Before(soonest) {
				soonest = t
			}
		}
	}
	return nil, soonest.Sub(now)
}

// asked records a request to an instance.
func (q *refreshQueue) asked(host string, now time.Time) {
	if t := now.Add(q.gap(host)); t.After(q.ready[host]) {
		q.ready[host] = t
	}
}

// gap is the time between requests to an instance: refreshPickDelay,
// doubled each time it asked us to slow down (at most 4 times as long).
func (q *refreshQueue) gap(host string) time.Duration {
	return refreshPickDelay << min(q.limits[host], 2)
}

// limitedHost records that an instance asked us to wait (askedFor: its
// Retry-After, if any). The shared pause (adapters.PausedUntil) already
// holds it, a minute the first time, then 2, then 5; this run also spaces
// its requests to it further apart from now on.
func (q *refreshQueue) limitedHost(host string, now time.Time, askedFor time.Duration) {
	q.limits[host]++
	until := adapters.PausedUntil(host)
	if until.IsZero() { // not recorded centrally (an adapter that didn't say)
		until = now.Add(adapters.RateLimitPause(askedFor, q.limits[host]))
	}
	if until.After(q.ready[host]) {
		q.ready[host] = until
	}
	q.paused[host] = q.ready[host]
}

// retry puts p back in the queue, unless it has been put back
// refreshRetries times already.
func (q *refreshQueue) retry(p *models.Pick) bool {
	q.tries[p.ID]++
	if q.tries[p.ID] > refreshRetries {
		return false
	}
	q.picks = append(q.picks, p)
	return true
}

// longestPause names the paused instance with the longest wait left.
func (q *refreshQueue) longestPause(now time.Time) (string, time.Duration) {
	host, longest := "", time.Duration(0)
	for h, until := range q.paused {
		if d := until.Sub(now); d > longest || (d == longest && d > 0 && h < host) {
			host, longest = h, d
		}
	}
	return host, longest
}

// waitText says a wait in words: "40 seconds", "1 minute", "3 minutes".
func waitText(d time.Duration) string {
	if n := int(math.Ceil(d.Seconds())); n < 60 {
		if n == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", n)
	}
	n := int(math.Round(d.Minutes()))
	if n == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", n)
}

// hostOf returns a URL's host, or the URL itself if it has none.
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// syncAutoExcerpt stores a pick's automatic excerpt as cards now show it
// (Pick.DisplayExcerpt), so search and the edit form match. Picks with a
// hand-written excerpt are left alone.
func (s *Server) syncAutoExcerpt(id int64) bool {
	p, err := s.Picks.GetByID(id)
	if err != nil || !p.ExcerptAuto {
		return false
	}
	changed, err := s.Picks.SetAutoExcerpt(id, p.DisplayExcerpt())
	if err != nil {
		log.Printf("metadata refresh: excerpt of pick %d: %v", id, err)
	}
	return changed
}
