package adapters

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// An instance that says it's getting too many requests (HTTP 429, or
// Lemmy's rate_limit_error) is paused for every kind of request at once:
// post-page comments, the comment-count job, auto-ingest polls, the
// metadata refresh and Fetch metadata. Until the pause is over, requests
// to it fail at once with a PausedError, without being sent, and each
// caller falls back in its own way (saved comments, other picks, other
// copies of the post, the next poll). The pause grows with refusals in a
// row: a minute, then 2, then 5 (RateLimitPause), or what the instance
// asked for if longer; after pauseResetAfter without a refusal it starts
// at a minute again.

// pauseResetAfter is how long an instance has to go without refusing
// before its next pause starts from a minute again.
const pauseResetAfter = 30 * time.Minute

// PausedError is returned, without sending anything, for a request to an
// instance whose pause isn't over, or (Busy) whose turn in the instance's
// queue (schedule.go) would come too late.
type PausedError struct {
	Host string
	Left time.Duration
	Busy bool
}

func (e *PausedError) Error() string {
	if e.Busy {
		return fmt.Sprintf("too many requests to %s are queued; its turn would come in %s", e.Host, e.Left.Round(time.Second))
	}
	return fmt.Sprintf("%s asked us to slow down; not asking it again for %s", e.Host, e.Left.Round(time.Second))
}

type hostPause struct {
	until   time.Time // no requests before this
	refused int       // refusals in a row
	last    time.Time // when the last refusal came
}

var pauses = struct {
	sync.Mutex
	m   map[string]*hostPause
	now func() time.Time
}{m: map[string]*hostPause{}, now: time.Now}

func hostKey(host string) string { return strings.ToLower(host) }

// pauseHost records a refusal from host (asked: its Retry-After, if any)
// and returns the pause that starts now.
func pauseHost(host string, asked time.Duration) time.Duration {
	pauses.Lock()
	defer pauses.Unlock()
	now := pauses.now()
	for h, p := range pauses.m { // forget instances that have been quiet a while
		if now.Sub(p.last) > pauseResetAfter && now.After(p.until) {
			delete(pauses.m, h)
		}
	}
	p := pauses.m[hostKey(host)]
	if p == nil {
		p = &hostPause{}
		pauses.m[hostKey(host)] = p
	}
	p.refused++
	p.last = now
	d := RateLimitPause(asked, p.refused)
	if until := now.Add(d); until.After(p.until) {
		p.until = until
	}
	return p.until.Sub(now)
}

// PausedUntil is when host may be asked again (the zero time if it isn't
// paused).
func PausedUntil(host string) time.Time {
	pauses.Lock()
	defer pauses.Unlock()
	if p := pauses.m[hostKey(host)]; p != nil && p.until.After(pauses.now()) {
		return p.until
	}
	return time.Time{}
}

// PauseLeft is how long host's pause still runs (0 if it isn't paused).
func PauseLeft(host string) time.Duration {
	if until := PausedUntil(host); !until.IsZero() {
		pauses.Lock()
		defer pauses.Unlock()
		return until.Sub(pauses.now())
	}
	return 0
}

// ResetPauses forgets every pause (for tests).
func ResetPauses() {
	pauses.Lock()
	pauses.m = map[string]*hostPause{}
	pauses.Unlock()
}

// checkPaused is the transport's gate: a PausedError for a paused host.
func checkPaused(req *http.Request) error {
	if left := PauseLeft(req.URL.Host); left > 0 {
		return &PausedError{Host: hostKey(req.URL.Host), Left: left}
	}
	return nil
}

// NothingSent reports whether err only says that instances are paused,
// i.e. no request went out.
func NothingSent(err error) bool {
	if err == nil {
		return false
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if !NothingSent(e) {
				return false
			}
		}
		return len(j.Unwrap()) > 0
	}
	var pe *PausedError
	return errors.As(err, &pe)
}
