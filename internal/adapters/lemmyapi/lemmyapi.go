// Package lemmyapi implements the Lemmy API v3 wire format.
//
// Posts are fetched with /api/v3/post?id=<id>, not resolve_object: the ID
// in a post URL is always local to the URL's instance, whereas
// resolve_object 400s on a federated post's local URL.
package lemmyapi

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
	Name string `json:"name"`
	// ActorID is a full URL; Lemmy has no separate instance-domain field,
	// so the domain is parsed from this.
	ActorID string `json:"actor_id"`
}

// label formats "community@instance", or "" if the name is unknown.
func (c communityFields) label() string {
	if c.Name == "" {
		return ""
	}
	if u, err := url.Parse(c.ActorID); err == nil && u.Host != "" {
		return c.Name + "@" + u.Host
	}
	return c.Name
}

type postFields struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Body         *string `json:"body"`
	URL          *string `json:"url"`
	ThumbnailURL *string `json:"thumbnail_url"`
	Published    string  `json:"published"`
	APID         string  `json:"ap_id"`
}

type personView struct {
	Name        string  `json:"name"`
	DisplayName *string `json:"display_name"`
	// ActorID is a full URL; the poster's home instance is parsed from it.
	ActorID string `json:"actor_id"`
}

// handle formats "name@instance", or "" if the name is empty.
func (p personView) handle() string {
	if p.Name == "" {
		return ""
	}
	if u, err := url.Parse(p.ActorID); err == nil && u.Host != "" {
		return p.Name + "@" + u.Host
	}
	return p.Name
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
	Content   string `json:"content"`
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

// FetchComments returns the post's comment thread plus, as Lemmy's UI does,
// the comments of its crossposts, with their top-level comments tagged
// with the source community.
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
			continue // defensive: shouldn't list the post as its own crosspost
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
	// type_=All: without it Lemmy applies the instance's default listing
	// type, and "Local" would hide comments on a post in a remote community.
	endpoint := instance + "/api/v3/comment/list?post_id=" +
		fmt.Sprint(postID) + "&type_=All&sort=Hot&max_depth=8&limit=200"

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
// (source.WatchType). WatchTarget is a name, bare for one local to
// source.InstanceURL or "name@instance" for a federated actor.
func (c *Client) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	isInstance := source.WatchType == models.WatchTypeInstance
	if !isInstance && (!source.WatchTarget.Valid || source.WatchTarget.String == "") {
		return nil, errors.New("source has no watch_target configured")
	}
	instance := strings.TrimRight(source.InstanceURL, "/")

	var endpoint string
	if isInstance {
		endpoint = fmt.Sprintf("%s/api/v3/post/list?type_=Local&sort=New&limit=%d", instance, adapters.PollLimit)
	} else if source.WatchType == models.WatchTypePerson {
		// /api/v3/user returns the same "posts" shape as post/list.
		endpoint = instance + "/api/v3/user?username=" +
			url.QueryEscape(source.WatchTarget.String) + "&sort=New&limit=" + fmt.Sprint(adapters.PollLimit)
	} else {
		endpoint = instance + "/api/v3/post/list?community_name=" +
			url.QueryEscape(source.WatchTarget.String) + "&sort=New&limit=" + fmt.Sprint(adapters.PollLimit)
	}

	var resp struct {
		Posts []postView `json:"posts"`
	}
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	host := hostOf(instance)
	out := make([]adapters.Metadata, 0, len(resp.Posts))
	for _, pv := range resp.Posts {
		// Local feed: only posts in communities hosted on this instance,
		// in case the instance ignores type_.
		if isInstance && hostOf(pv.Community.ActorID) != host {
			continue
		}
		out = append(out, metadataFromPostView(pv))
	}
	return out, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// postIDPattern extracts the instance-local post ID from "/post/<id>"
// (or "/p/<id>").
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

	endpoint := instance + "/api/v3/post?id=" + id
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
		Title:           pv.Post.Name,
		AuthorName:      pv.Creator.Name,
		CommentURL:      pv.Post.APID,
		AuthorHandle:    pv.Creator.handle(),
		CommunityHandle: pv.Community.label(),
	}
	if pv.Creator.DisplayName != nil {
		m.AuthorName = *pv.Creator.DisplayName
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

// buildCommentTree turns Lemmy's flat comment list (each with a dotted
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
		author := cv.Creator.Name
		if cv.Creator.DisplayName != nil {
			author = *cv.Creator.DisplayName
		}
		createdAt, _ := adapters.ParseTime(cv.Comment.Published)
		body := cv.Comment.Content
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
