// Package mbin is the Mbin platform adapter. Mbin distinguishes "entries"
// (/m/<magazine>/t/<id>/<slug>) from microblog "posts" (/m/<magazine>/p/<id>);
// the two are numbered separately, so the URL decides which API is asked.
package mbin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

const requestTimeout = 15 * time.Second

type Adapter struct {
	HTTP *http.Client
}

func New() *Adapter {
	return &Adapter{HTTP: adapters.NewHTTPClient(requestTimeout)}
}

var idPattern = regexp.MustCompile(`/(t|p)/(\d+)`)

// itemKind is what a URL points at: an entry (thread) or a microblog post.
type itemKind int

const (
	kindEntry itemKind = iota
	kindPost
)

type entryResponse struct {
	EntryID      int64        `json:"entryId"`
	ApID         *string      `json:"apId"` // null for an entry local to this instance
	Title        string       `json:"title"`
	Body         *string      `json:"body"`
	URL          *string      `json:"url"`
	Image        *mbinImage   `json:"image"`
	User         mbinUser     `json:"user"`
	Magazine     mbinMagazine `json:"magazine"`
	CreatedAt    string       `json:"createdAt"`
	CommentCount int          `json:"numComments"`
}

type postResponse struct {
	Body         *string      `json:"body"`
	URL          *string      `json:"url"`
	Image        *mbinImage   `json:"image"`
	User         mbinUser     `json:"user"`
	Magazine     mbinMagazine `json:"magazine"`
	CreatedAt    string       `json:"createdAt"`
	CommentCount int          `json:"comments"`
}

type mbinImage struct {
	StorageURL string `json:"storageUrl"`
	SourceURL  string `json:"sourceUrl"` // set for remote images that weren't stored locally
}

// url returns the image's address, preferring this instance's stored copy.
func (i *mbinImage) url() string {
	if i.StorageURL != "" {
		return i.StorageURL
	}
	return i.SourceURL
}

// mbinUser's Username is "@name@instance" for a federated account and bare
// "name" for a local one; there is no actor-URL field.
type mbinUser struct {
	Username string `json:"username"`
}

// name is the username without Mbin's leading "@" on remote accounts.
func (u mbinUser) name() string {
	return strings.TrimPrefix(u.Username, "@")
}

// handle returns the username as "name@instance", qualifying a local one
// with instanceHost.
func (u mbinUser) handle(instanceHost string) string {
	n := u.name()
	if n == "" || strings.Contains(n, "@") || instanceHost == "" {
		return n
	}
	return n + "@" + instanceHost
}

// mbinMagazine's Name is "name@instance" for a remote magazine and bare
// "name" for a local one.
type mbinMagazine struct {
	Name string `json:"name"`
}

func (m mbinMagazine) handle(instanceHost string) string {
	n := strings.TrimPrefix(m.Name, "!")
	if n == "" || strings.Contains(n, "@") || instanceHost == "" {
		return n
	}
	return n + "@" + instanceHost
}

type commentsListResponse struct {
	Items []mbinComment `json:"items"`
}

// entryListResponse is Mbin's paginated entries listing, used by Poll.
type entryListResponse struct {
	Items []entryListItem `json:"items"`
}

type entryListItem struct {
	EntryID      int64        `json:"entryId"`
	ApID         *string      `json:"apId"`
	Title        string       `json:"title"`
	Slug         string       `json:"slug"`
	Body         *string      `json:"body"`
	URL          *string      `json:"url"`
	Image        *mbinImage   `json:"image"`
	User         mbinUser     `json:"user"`
	Magazine     mbinMagazine `json:"magazine"`
	CreatedAt    string       `json:"createdAt"`
	CommentCount int          `json:"numComments"`
}

type magazineResponse struct {
	MagazineID int64 `json:"magazineId"`
}

type mbinComment struct {
	Body      string        `json:"body"`
	User      mbinUser      `json:"user"`
	CreatedAt string        `json:"createdAt"`
	Score     int           `json:"favourites"`
	Children  []mbinComment `json:"children"`
}

func (a *Adapter) FetchMetadata(ctx context.Context, postURL string) (adapters.Metadata, error) {
	instance, kind, id, err := parsePostURL(postURL)
	if err != nil {
		return adapters.Metadata{}, err
	}
	if kind == kindPost {
		post, err := a.getPost(ctx, instance, id)
		if err != nil {
			return adapters.Metadata{}, err
		}
		return metadataFromPost(post, postURL), nil
	}
	entry, err := a.getEntry(ctx, instance, id)
	if err != nil {
		return adapters.Metadata{}, err
	}
	return metadataFromEntry(entry, postURL), nil
}

func (a *Adapter) FetchCommentCount(ctx context.Context, postURL string) (int, error) {
	instance, kind, id, err := parsePostURL(postURL)
	if err != nil {
		return 0, err
	}
	if kind == kindPost {
		post, err := a.getPost(ctx, instance, id)
		return post.CommentCount, err
	}
	entry, err := a.getEntry(ctx, instance, id)
	return entry.CommentCount, err
}

// commentsPerPage is the most Mbin returns per page of top-level comments.
const commentsPerPage = 100

func (a *Adapter) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	instance, kind, id, err := parsePostURL(postURL)
	if err != nil {
		return adapters.CommentTree{}, err
	}

	endpoint := fmt.Sprintf("%s/api/entry/%d/comments?perPage=%d", instance, id, commentsPerPage)
	if kind == kindPost {
		endpoint = fmt.Sprintf("%s/api/posts/%d/comments?perPage=%d", instance, id, commentsPerPage)
	}
	var resp commentsListResponse
	if err := a.getJSON(ctx, endpoint, &resp); err != nil {
		return adapters.CommentTree{}, err
	}

	total := 0
	comments := make([]adapters.Comment, 0, len(resp.Items))
	for _, c := range resp.Items {
		conv := convertComment(c)
		total += 1 + countChildren(conv)
		comments = append(comments, conv)
	}
	// A very long thread is cut off at one page; the entry's own count is
	// the real total, matching FetchCommentCount.
	if n, err := a.FetchCommentCount(ctx, postURL); err == nil && n > total {
		total = n
	}
	return adapters.CommentTree{Comments: comments, Total: total}, nil
}

// Poll lists recent entries from a watched magazine or person
// (source.WatchType). A magazine name is local to source.InstanceURL; a
// username may carry "@instance". Only entries are polled, not microblog
// posts, which don't fit community-style curation.
func (a *Adapter) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	isInstance := source.WatchType == models.WatchTypeInstance
	if !isInstance && (!source.WatchTarget.Valid || source.WatchTarget.String == "") {
		return nil, errors.New("source has no watch_target configured")
	}
	instance := strings.TrimRight(source.InstanceURL, "/")

	var endpoint string
	if isInstance {
		endpoint = fmt.Sprintf("%s/api/entries?federation=local&sort=newest&perPage=%d", instance, adapters.PollLimit)
	} else if source.WatchType == models.WatchTypePerson {
		// The user entries route takes a numeric ID, not a name.
		userID, err := a.resolveUserID(ctx, instance, source.WatchTarget.String)
		if err != nil {
			return nil, fmt.Errorf("resolving user %q: %w", source.WatchTarget.String, err)
		}
		endpoint = fmt.Sprintf("%s/api/users/%d/entries?sort=newest&perPage=%d", instance, userID, adapters.PollLimit)
	} else {
		magazineName := source.WatchTarget.String
		magazineID, err := a.resolveMagazineID(ctx, instance, magazineName)
		if err != nil {
			return nil, fmt.Errorf("resolving magazine %q: %w", magazineName, err)
		}
		endpoint = fmt.Sprintf("%s/api/magazine/%d/entries?sort=newest&perPage=%d", instance, magazineID, adapters.PollLimit)
	}

	var resp entryListResponse
	if err := a.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	out := make([]adapters.Metadata, 0, len(resp.Items))
	for _, e := range resp.Items {
		// federation=local returns entries created on this instance, which
		// includes local users posting to remote magazines ("name@host").
		if isInstance && strings.Contains(e.Magazine.Name, "@") {
			continue
		}
		out = append(out, metadataFromEntryListItem(e, instance))
	}
	return out, nil
}

func (a *Adapter) getEntry(ctx context.Context, instance string, id int64) (entryResponse, error) {
	var e entryResponse
	err := a.getJSON(ctx, fmt.Sprintf("%s/api/entry/%d", instance, id), &e)
	return e, err
}

func (a *Adapter) getPost(ctx context.Context, instance string, id int64) (postResponse, error) {
	var p postResponse
	err := a.getJSON(ctx, fmt.Sprintf("%s/api/post/%d", instance, id), &p)
	return p, err
}

// resolveUserID looks up a user's numeric ID by name ("name" or
// "name@instance").
func (a *Adapter) resolveUserID(ctx context.Context, instance, name string) (int64, error) {
	var resp struct {
		UserID int64 `json:"userId"`
	}
	if err := a.getJSON(ctx, fmt.Sprintf("%s/api/users/name/%s", instance, url.PathEscape(name)), &resp); err != nil {
		return 0, err
	}
	if resp.UserID == 0 {
		return 0, fmt.Errorf("user %q not found", name)
	}
	return resp.UserID, nil
}

// resolveMagazineID looks up a magazine's ID; the magazine entries route
// takes an ID, not a name.
func (a *Adapter) resolveMagazineID(ctx context.Context, instance, name string) (int64, error) {
	var resp magazineResponse
	endpoint := fmt.Sprintf("%s/api/magazine/name/%s", instance, url.PathEscape(name))
	if err := a.getJSON(ctx, endpoint, &resp); err != nil {
		return 0, err
	}
	if resp.MagazineID == 0 {
		return 0, fmt.Errorf("magazine %q not found", name)
	}
	return resp.MagazineID, nil
}

func (a *Adapter) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return adapters.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return adapters.NewStatusError(resp, endpoint)
	}
	return adapters.DecodeJSON(resp, out)
}

func metadataFromEntry(e entryResponse, sourceURL string) adapters.Metadata {
	host := hostOf(sourceURL)
	m := adapters.Metadata{
		Title:           e.Title,
		AuthorName:      e.User.name(),
		CommentURL:      sourceURL,
		ApID:            entryApID(e.ApID, sourceURL, e.Magazine.Name, e.EntryID),
		AuthorHandle:    e.User.handle(host),
		CommunityHandle: e.Magazine.handle(host),
	}
	if e.Body != nil {
		m.Body = *e.Body
	}
	if e.Image != nil {
		m.ImageURL = e.Image.url()
	}
	m.LinkURL = linkURL(e.URL)
	if t, ok := adapters.ParseTime(e.CreatedAt); ok {
		m.CreatedAt = t
	}
	return m
}

// linkURL returns an entry's external link, or "" if it has none or the
// "link" is just the entry's own image.
func linkURL(u *string) string {
	if u == nil || *u == "" {
		return ""
	}
	lower := strings.ToLower(*u)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(lower, ext) {
			return ""
		}
	}
	return *u
}

func metadataFromPost(p postResponse, sourceURL string) adapters.Metadata {
	host := hostOf(sourceURL)
	m := adapters.Metadata{
		AuthorName:      p.User.name(),
		CommentURL:      sourceURL,
		AuthorHandle:    p.User.handle(host),
		CommunityHandle: p.Magazine.handle(host),
	}
	if p.Body != nil {
		m.Body = *p.Body
		// Microblog posts have no separate title — use the first line.
		if line, _, ok := strings.Cut(*p.Body, "\n"); ok {
			m.Title = truncate(line, 120)
		} else {
			m.Title = truncate(*p.Body, 120)
		}
	}
	if p.Image != nil {
		m.ImageURL = p.Image.url()
	}
	if t, ok := adapters.ParseTime(p.CreatedAt); ok {
		m.CreatedAt = t
	}
	return m
}

// metadataFromEntryListItem builds Metadata from a listing item. The list
// has no post URL, so one is reconstructed in the shape parsePostURL expects.
func metadataFromEntryListItem(e entryListItem, instance string) adapters.Metadata {
	host := hostOf(instance)
	m := adapters.Metadata{
		Title:           e.Title,
		AuthorName:      e.User.name(),
		CommentURL:      fmt.Sprintf("%s/m/%s/t/%d/%s", instance, e.Magazine.Name, e.EntryID, e.Slug),
		ApID:            entryApID(e.ApID, instance, e.Magazine.Name, e.EntryID),
		AuthorHandle:    e.User.handle(host),
		CommunityHandle: e.Magazine.handle(host),
		LinkURL:         linkURL(e.URL),
	}
	if e.Body != nil {
		m.Body = *e.Body
	}
	if e.Image != nil {
		m.ImageURL = e.Image.url()
	}
	if t, ok := adapters.ParseTime(e.CreatedAt); ok {
		m.CreatedAt = t
	}
	return m
}

func convertComment(c mbinComment) adapters.Comment {
	out := adapters.Comment{
		Author: c.User.name(),
		Body:   c.Body,
		Score:  c.Score,
	}
	if t, ok := adapters.ParseTime(c.CreatedAt); ok {
		out.CreatedAt = t
	}
	for _, child := range c.Children {
		out.Children = append(out.Children, convertComment(child))
	}
	return out
}

func countChildren(c adapters.Comment) int {
	n := 0
	for _, child := range c.Children {
		n += 1 + countChildren(child)
	}
	return n
}

// truncate shortens s to at most max bytes without splitting a character.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "…"
}

// entryApID is an entry's ActivityPub ID: Mbin's apId for an entry from
// another instance, else (apId is null for local entries) Mbin's own
// /m/<magazine>/t/<id> URL.
func entryApID(apID *string, instanceURL, magazine string, entryID int64) string {
	if apID != nil && *apID != "" {
		return *apID
	}
	if entryID == 0 || magazine == "" {
		return ""
	}
	u, err := url.Parse(instanceURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s/m/%s/t/%d", u.Scheme, u.Host, magazine, entryID)
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// parsePostURL extracts the instance base URL, the kind of item and its
// numeric ID from an Mbin entry (/t/<id>) or microblog post (/p/<id>) URL.
// A URL of another shape wraps adapters.ErrNotFound, so callers can try
// another platform.
func parsePostURL(postURL string) (instance string, kind itemKind, id int64, err error) {
	u, err := url.Parse(postURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", 0, 0, fmt.Errorf("invalid post URL %q: %w", postURL, adapters.ErrNotFound)
	}
	instance = u.Scheme + "://" + u.Host

	match := idPattern.FindStringSubmatch(u.Path)
	if match == nil {
		return "", 0, 0, fmt.Errorf("not an Mbin thread or post URL (no /t/<id> or /p/<id>): %w", adapters.ErrNotFound)
	}
	id, err = strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return "", 0, 0, err
	}
	if match[1] == "p" {
		kind = kindPost
	}
	return instance, kind, id, nil
}
