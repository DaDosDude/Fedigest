package web

import (
	"crypto/sha256"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"fedigest/internal/auth"
	"fedigest/internal/models"
)

// botUA matches crawlers, link-preview fetchers (including fediverse
// software fetching link cards) and command-line clients.
var botUA = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|preview|fetch|curl|wget|python|go-http|java/|okhttp|headless|lighthouse|facebookexternalhit|discord|telegram|whatsapp|slack|mastodon|lemmy|piefed|mbin|pleroma|akkoma|misskey|friendica|fedigest`)

// isBot reports whether a request comes from a crawler, link-preview
// fetcher or script (or sends no user agent at all).
func isBot(r *http.Request) bool {
	ua := r.UserAgent()
	return ua == "" || botUA.MatchString(ua)
}

// viewCounter decides whether a post-page request counts as a view: at
// most once per visitor per pick per day. Visitors are remembered only as
// a hash of IP, user agent and pick, in memory, for the current day.
type viewCounter struct {
	mu   sync.Mutex
	day  string
	seen map[[32]byte]struct{}
}

func (c *viewCounter) firstToday(ip, ua string, pickID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if c.day != today || len(c.seen) > 500000 {
		c.day = today
		c.seen = map[[32]byte]struct{}{}
	}
	key := sha256.Sum256([]byte(ip + "\x00" + ua + "\x00" + strconv.FormatInt(pickID, 10)))
	if _, ok := c.seen[key]; ok {
		return false
	}
	c.seen[key] = struct{}{}
	return true
}

var (
	viewsOnce sync.Once
	views     *viewCounter
)

// countView records a view of a published pick by an anonymous, non-bot
// visitor. Logged-in users (curators checking their own posts) don't count.
func (s *Server) countView(r *http.Request, pick *models.Pick) {
	if r.Method != http.MethodGet || pick.Status != models.PickStatusPublished {
		return
	}
	if auth.UserFromContext(r.Context()) != nil {
		return
	}
	if isBot(r) {
		return
	}
	ua := r.UserAgent()
	viewsOnce.Do(func() { views = &viewCounter{} })
	if !views.firstToday(clientIP(r), ua, pick.ID) {
		return
	}
	s.Picks.AddView(pick.ID)
}
