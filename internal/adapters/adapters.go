// Package adapters defines the common interface for talking to Lemmy,
// PieFed, and Mbin, plus a registry that dispatches by platform.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fedigest/internal/models"
)

// ErrNotFound is returned when the remote instance has no such post.
var ErrNotFound = errors.New("post not found on source instance")

// Metadata is what a manual pick or an auto-ingest poll pulls from the
// original post.
type Metadata struct {
	Title      string
	Body       string // the original post's own text, used when a curator ticks "use original as body"
	ImageURL   string
	AuthorName string
	CreatedAt  time.Time
	CommentURL string // canonical URL to store as original_url (normalizes the pasted link)
	// ApID is the post's ActivityPub ID when it differs from CommentURL
	// (Mbin, for federated posts). Lemmy/PieFed leave it empty. Use
	// CanonicalID().
	ApID string
	// LinkURL is the external URL of a link post. Empty for text posts and
	// for image posts (whose url is surfaced as ImageURL).
	LinkURL string

	// AuthorHandle and CommunityHandle are "name@instance", or empty if the
	// home instance couldn't be determined.
	AuthorHandle    string
	CommunityHandle string
}

// Comment is one node in a fetched comment tree.
type Comment struct {
	Author    string
	Body      string
	Score     int
	CreatedAt time.Time
	Children  []Comment

	// Community is set only on top-level comments merged in from a
	// crosspost, to label where they came from.
	Community string
}

type CommentTree struct {
	Comments []Comment
	Total    int
}

// Adapter is implemented once per supported platform. Lemmy and PieFed
// share most of their wire format; Mbin has its own implementation.
type Adapter interface {
	// FetchMetadata retrieves title/body/image/author for a single post URL.
	FetchMetadata(ctx context.Context, postURL string) (Metadata, error)

	// FetchComments retrieves the full comment tree; caching is the
	// caller's job.
	FetchComments(ctx context.Context, postURL string) (CommentTree, error)

	// FetchCommentCount returns the comment count without the tree.
	FetchCommentCount(ctx context.Context, postURL string) (int, error)

	// Poll lists recent posts from a watched community/account. The caller
	// handles dedup and pick creation.
	Poll(ctx context.Context, source models.Source) ([]Metadata, error)
}

// Registry resolves the right Adapter for a platform.
type Registry struct {
	adapters map[models.Platform]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[models.Platform]Adapter)}
}

func (r *Registry) Register(platform models.Platform, a Adapter) {
	r.adapters[platform] = a
}

func (r *Registry) For(platform models.Platform) (Adapter, error) {
	a, ok := r.adapters[platform]
	if !ok {
		return nil, errors.New("no adapter registered for platform: " + string(platform))
	}
	return a, nil
}

// CanonicalID returns the post's ActivityPub ID.
func (m Metadata) CanonicalID() string {
	if m.ApID != "" {
		return m.ApID
	}
	return m.CommentURL
}

// PollLimit is how many of the newest posts a poll asks for: Lemmy's
// largest page. A busy feed can get more new posts than this between two
// polls; those in between are missed.
const PollLimit = 50

// maxResponseBytes caps how much of an instance's answer is read.
const maxResponseBytes = 16 << 20

// StatusError is an unexpected HTTP status from an instance's API.
type StatusError struct {
	Code     int
	Endpoint string
	// Message is the instance's own short explanation, if it gave one
	// (Lemmy and PieFed send {"error": "…"}).
	Message string
	// RetryAfter is how long the instance asked us to wait, if it said.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("unexpected status %d from %s", e.Code, e.Endpoint)
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// RateLimited reports whether the instance refused because of too many
// requests: HTTP 429, or Lemmy's "rate_limit_error", which comes with 400.
func (e *StatusError) RateLimited() bool {
	return e.Code == http.StatusTooManyRequests || strings.Contains(e.Message, "rate_limit")
}

// NewStatusError describes an unexpected response, reading the instance's
// error message (JSON "error"/"message", or a short plain-text body) and
// any Retry-After header.
func NewStatusError(resp *http.Response, endpoint string) *StatusError {
	e := &StatusError{Code: resp.StatusCode, Endpoint: endpoint}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var j struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &j) == nil {
		e.Message = j.Error
		if e.Message == "" {
			e.Message = j.Message
		}
	} else if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		e.Message = string(body)
	}
	e.Message = strings.Join(strings.Fields(e.Message), " ")
	if r := []rune(e.Message); len(r) > 200 {
		e.Message = string(r[:200]) + "…"
	}
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil && n > 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil && time.Until(t) > 0 {
			e.RetryAfter = time.Until(t)
		}
	}
	if e.RateLimited() && resp.Request != nil && resp.Request.URL != nil {
		pauseHost(resp.Request.URL.Host, e.RetryAfter) // every request to it waits (pause.go)
	}
	return e
}

// rateLimitPauses are a background job's waits after an instance says
// it's getting too many requests: the first time, the second, and from the
// third on.
var rateLimitPauses = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}

// RateLimitPause is how long a background job waits after an instance
// says it's getting too many requests for the nth time in a run (1, 2,
// …): a minute, then 2, then 5, or longer if the instance asked for more
// (at most 15 minutes).
func RateLimitPause(asked time.Duration, n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := rateLimitPauses[min(n, len(rateLimitPauses))-1]
	if asked > d {
		d = asked
	}
	return min(d, 15*time.Minute)
}

// RateLimit reports whether err (or any error joined into it) is an
// instance saying "too many requests", or a request held back because it
// recently did (PausedError), and how long to wait (0 if unknown).
func RateLimit(err error) (bool, time.Duration) {
	var se *StatusError
	if errors.As(err, &se) && se.RateLimited() {
		return true, se.RetryAfter
	}
	var pe *PausedError
	if errors.As(err, &pe) {
		return true, pe.Left
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if limited, wait := RateLimit(e); limited {
				return true, wait
			}
		}
	}
	return false, 0
}

// DecodeJSON decodes an API response body into out, reading at most
// maxResponseBytes, and explains a non-JSON answer (such as an HTML error
// or challenge page) instead of reporting a JSON syntax error.
func DecodeJSON(resp *http.Response, out any) error {
	err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out)
	if err == nil {
		return nil
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return fmt.Errorf("%s didn't answer with JSON (got %s); is this the right platform and instance?", resp.Request.URL.Host, ct)
	}
	return fmt.Errorf("reading the answer from %s: %w", resp.Request.URL.Host, err)
}

// ParseTime reads an API timestamp. Older Lemmy versions send UTC times
// without a zone ("2023-06-21T18:36:57.528929").
func ParseTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02T15:04:05.999999999", s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// DeletedComment is the text shown in place of a deleted or removed
// comment, which is kept so its replies stay in place.
func DeletedComment(removed bool) (author, body string) {
	if removed {
		return "", "*Removed by a moderator.*"
	}
	return "", "*Deleted.*"
}
