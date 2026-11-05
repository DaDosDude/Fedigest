// Package piefed is the PieFed platform adapter. PieFed's API differs from
// Lemmy's enough to need its own implementation:
//
//   - It lives under /api/alpha, not /api/v3.
//   - Posts are fetched by the instance-local ID in the URL, via
//     /api/alpha/post?id=<id> (as lemmyapi does).
//   - Field names differ: post "title" (Lemmy "name"), comment "body"
//     (Lemmy "content"), person "user_name"/"title" (Lemmy
//     "name"/"display_name").
package piefed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

const requestTimeout = 15 * time.Second

type Client struct {
	HTTP *http.Client
}

func New() *Client {
	return &Client{HTTP: adapters.NewHTTPClient(requestTimeout)}
}

type postResponse struct {
	PostView postView `json:"post_view"`
	// CrossPosts lists other copies of this post in other communities.
	CrossPosts []postView `json:"cross_posts"`
}

type postView struct {
	Post      postFields      `json:"post"`
	Creator   personView      `json:"creator"`
	Counts    postCounts      `json:"counts"`
	Community communityFields `json:"community"`
}

type communityFields struct {
	Name     string `json:"name"`
	ApDomain string `json:"ap_domain"`
}

// label formats "community@instance", or "" if the name is unknown.
func (c communityFields) label() string {
	if c.Name == "" {
		return ""
	}
	if c.ApDomain == "" {
		return c.Name
	}
	return c.Name + "@" + c.ApDomain
}

type postFields struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Body         *string `json:"body"`
	URL          *string `json:"url"`
	ThumbnailURL *string `json:"thumbnail_url"`
	Published    string  `json:"published"`
	APID         string  `json:"ap_id"`
}

type personView struct {
	UserName string  `json:"user_name"`
	Title    *string `json:"title"` // display name, when set — otherwise mirrors user_name
	// ActorID is the person's actor URL. PieFed uses "actor_id" here, not
	// "ap_id" as on its post object.
	ActorID string `json:"actor_id"`
}

func (p personView) displayName() string {
	if p.Title != nil && *p.Title != "" {
		return *p.Title
	}
	return p.UserName
}

// handle formats "user_name@instance", or "" if user_name is empty.
func (p personView) handle() string {
	if p.UserName == "" {
		return ""
	}
	if u, err := url.Parse(p.ActorID); err == nil && u.Host != "" {
		return p.UserName + "@" + u.Host
	}
	return p.UserName
}

type postCounts struct {
	Comments int `json:"comments"`
}

type commentsResponse struct {
	Comments []commentView `json:"comments"`
}

type commentView struct {
	Comment commentFields `json:"comment"`
	Creator personView    `json:"creator"`
	Counts  commentCounts `json:"counts"`
}

type commentFields struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Published string `json:"published"`
	Path      string `json:"path"` // dot-separated ancestor chain, e.g. "0.5.12"
	// Deleted and removed comments are still listed (so replies keep
	// their place); their text mustn't be shown.
	Deleted bool `json:"deleted"`
	Removed bool `json:"removed"`
}

type commentCounts struct {
	Score int `json:"score"`
}

type postListResponse struct {
	Posts []postView `json:"posts"`
}

func (c *Client) FetchMetadata(ctx context.Context, postURL string) (adapters.Metadata, error) {
	pv, err := c.fetchPost(ctx, postURL)
	if err != nil {
		return adapters.Metadata{}, err
	}
	return metadataFromPostView(pv), nil
}

// FetchCommentCount sums the post's and its crossposts' comment counts,
// matching what FetchComments returns.
func (c *Client) FetchCommentCount(ctx context.Context, postURL string) (int, error) {
	resp, err := c.fetchPostFull(ctx, postURL)
	if err != nil {
		return 0, err
	}
	total := resp.PostView.Counts.Comments
	for _, cp := range resp.CrossPosts {
		total += cp.Counts.Comments
	}
	return total, nil
}

// FetchComments returns the post's comment thread plus, as PieFed's UI
// does, the comments of its crossposts, with their top-level comments
// tagged with the source community.
func (c *Client) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	resp, err := c.fetchPostFull(ctx, postURL)
	if err != nil {
		return adapters.CommentTree{}, err
	}

	instance, err := instanceBase(postURL)
	if err != nil {
		return adapters.CommentTree{}, err
	}

	roots, err := c.fetchCommentTree(ctx, instance, resp.PostView.Post.ID, "")
	if err != nil {
		return adapters.CommentTree{}, err
	}
	total := resp.PostView.Counts.Comments

	for _, cp := range resp.CrossPosts {
		if cp.Post.ID == resp.PostView.Post.ID {
			continue
		}
		cpRoots, err := c.fetchCommentTree(ctx, instance, cp.Post.ID, cp.Community.label())
		if err != nil {
			continue // one failed crosspost shouldn't fail the page
		}
		roots = append(roots, cpRoots...)
		total += cp.Counts.Comments
	}

	return adapters.CommentTree{Comments: roots, Total: total}, nil
}

// fetchCommentTree builds the tree for one post ID, tagging top-level
// comments with community (empty for the original thread).
func (c *Client) fetchCommentTree(ctx context.Context, instance string, postID int64, community string) ([]adapters.Comment, error) {
	endpoint := instance + "/api/alpha/comment/list?post_id=" +
		fmt.Sprint(postID) + "&sort=Hot&max_depth=8&limit=200"

	var resp commentsResponse
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	tree := buildCommentTree(resp.Comments)
	if community != "" {
		for i := range tree {
			tree[i].Community = community
		}
	}
	return tree, nil
}

// Poll lists recent posts from a watched community or person
// (source.WatchType); WatchTarget is as for lemmyapi.Poll.
func (c *Client) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	isInstance := source.WatchType == models.WatchTypeInstance
	if !isInstance && (!source.WatchTarget.Valid || source.WatchTarget.String == "") {
		return nil, errors.New("source has no watch_target configured")
	}
	instance := strings.TrimRight(source.InstanceURL, "/")

	var endpoint string
	if isInstance {
		endpoint = instance + "/api/alpha/post/list?type_=Local&sort=New"
	} else if source.WatchType == models.WatchTypePerson {
		endpoint = instance + "/api/alpha/user?username=" +
			url.QueryEscape(source.WatchTarget.String) + "&sort=New"
	} else {
		endpoint = instance + "/api/alpha/post/list?community_name=" +
			url.QueryEscape(source.WatchTarget.String) + "&sort=New"
	}

	var resp postListResponse
	err := c.getJSON(ctx, endpoint+"&limit="+fmt.Sprint(adapters.PollLimit), &resp)
	var status *adapters.StatusError
	if errors.As(err, &status) && status.Code == http.StatusBadRequest && !status.RateLimited() {
		// In case an instance refuses the larger page.
		err = c.getJSON(ctx, endpoint+"&limit=20", &resp)
	}
	if err != nil {
		return nil, err
	}

	var host string
	if u, err := url.Parse(instance); err == nil {
		host = strings.ToLower(u.Host)
	}
	out := make([]adapters.Metadata, 0, len(resp.Posts))
	for _, pv := range resp.Posts {
		// Local feed: only communities hosted here, in case type_ is ignored.
		if isInstance && pv.Community.ApDomain != "" && !strings.EqualFold(pv.Community.ApDomain, host) {
			continue
		}
		out = append(out, metadataFromPostView(pv))
	}
	return out, nil
}

// postIDPattern extracts the instance-local post ID from "/post/<id>" or
// "/c/<community>@<remote>/p/<id>/<slug>".
var postIDPattern = regexp.MustCompile(`/(?:post|p)/(\d+)`)

// postIDFromURL returns the post ID in a post URL. A URL of another shape
// wraps adapters.ErrNotFound, so callers can try another platform.
func postIDFromURL(postURL string) (string, error) {
	m := postIDPattern.FindStringSubmatch(postURL)
	if m == nil {
		return "", fmt.Errorf("no /post/<id> in %q: %w", postURL, adapters.ErrNotFound)
	}
	return m[1], nil
}

func (c *Client) fetchPost(ctx context.Context, postURL string) (postView, error) {
	resp, err := c.fetchPostFull(ctx, postURL)
	if err != nil {
		return postView{}, err
	}
	return resp.PostView, nil
}

// fetchPostFull is like fetchPost but also returns the crosspost list.
func (c *Client) fetchPostFull(ctx context.Context, postURL string) (postResponse, error) {
	instance, err := instanceBase(postURL)
	if err != nil {
		return postResponse{}, err
	}
	id, err := postIDFromURL(postURL)
	if err != nil {
		return postResponse{}, err
	}

	endpoint := instance + "/api/alpha/post?id=" + id
	var resp postResponse
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return postResponse{}, err
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
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

func metadataFromPostView(pv postView) adapters.Metadata {
	m := adapters.Metadata{
		Title:           pv.Post.Title,
		AuthorName:      pv.Creator.displayName(),
		CommentURL:      pv.Post.APID,
		AuthorHandle:    pv.Creator.handle(),
		CommunityHandle: pv.Community.label(),
	}
	if pv.Post.Body != nil {
		m.Body = *pv.Post.Body
	}
	if pv.Post.ThumbnailURL != nil {
		m.ImageURL = *pv.Post.ThumbnailURL
	} else if pv.Post.URL != nil && looksLikeImage(*pv.Post.URL) {
		m.ImageURL = *pv.Post.URL
	}
	if pv.Post.URL != nil && *pv.Post.URL != "" && !looksLikeImage(*pv.Post.URL) {
		m.LinkURL = *pv.Post.URL
	}
	if t, ok := adapters.ParseTime(pv.Post.Published); ok {
		m.CreatedAt = t
	}
	return m
}

func looksLikeImage(u string) bool {
	lower := strings.ToLower(u)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// instanceBase extracts "scheme://host" from a post URL.
func instanceBase(postURL string) (string, error) {
	u, err := url.Parse(postURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid post URL %q: %w", postURL, adapters.ErrNotFound)
	}
	return u.Scheme + "://" + u.Host, nil
}

// buildCommentTree turns PieFed's flat comment list (each with a dotted
// "path" of ancestor IDs) into a nested tree for rendering.
func buildCommentTree(flat []commentView) []adapters.Comment {
	type node struct {
		comment  adapters.Comment
		id       int64
		parentID int64 // 0 = top-level
	}

	nodes := make(map[int64]*node, len(flat))
	var order []int64

	for _, cv := range flat {
		parts := strings.Split(cv.Comment.Path, ".")
		var parentID int64
		if len(parts) >= 2 {
			fmt.Sscanf(parts[len(parts)-2], "%d", &parentID)
		}
		createdAt, _ := adapters.ParseTime(cv.Comment.Published)
		author, body := cv.Creator.displayName(), cv.Comment.Body
		if cv.Comment.Deleted || cv.Comment.Removed {
			author, body = adapters.DeletedComment(cv.Comment.Removed)
		}
		n := &node{
			id:       cv.Comment.ID,
			parentID: parentID,
			comment: adapters.Comment{
				Author:    author,
				Body:      body,
				Score:     cv.Counts.Score,
				CreatedAt: createdAt,
			},
		}
		nodes[n.id] = n
		order = append(order, n.id)
	}

	childrenOf := make(map[int64][]int64)
	for _, id := range order {
		n := nodes[id]
		childrenOf[n.parentID] = append(childrenOf[n.parentID], id)
	}

	var attach func(id int64) adapters.Comment
	attach = func(id int64) adapters.Comment {
		n := nodes[id]
		c := n.comment
		for _, childID := range childrenOf[id] {
			c.Children = append(c.Children, attach(childID))
		}
		return c
	}

	var roots []adapters.Comment
	for _, id := range childrenOf[0] {
		roots = append(roots, attach(id))
	}
	return roots
}
