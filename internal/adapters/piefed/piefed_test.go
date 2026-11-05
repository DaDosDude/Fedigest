package piefed

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fedigest/internal/models"
)

func strPtr(s string) *string { return &s }

// fakePieFedServer serves fixed /api/alpha responses shaped like real
// PieFed responses.
func fakePieFedServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// resolve_object isn't served: posts are fetched by their local ID.

	mux.HandleFunc("/api/alpha/post", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "2095688" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(postResponse{
			PostView: postView{
				Post: postFields{
					ID:           2095688,
					Title:        "three business days feels generous",
					ThumbnailURL: strPtr("https://piefed.example/static/media/posts/thumb.webp"),
					Published:    "2026-09-23T13:05:28.435134Z",
					APID:         "https://sopuli.xyz/post/52141910",
				},
				Creator:   personView{UserName: "ooli3", ActorID: "https://sopuli.xyz/u/ooli3"},
				Counts:    postCounts{Comments: 109},
				Community: communityFields{Name: "politicalmemes", ApDomain: "lemmy.ca"},
			},
			// A crosspost of the same post in another community.
			CrossPosts: []postView{
				{
					Post:      postFields{ID: 2095999, Title: "three business days feels generous", Published: "2026-09-23T13:10:00Z"},
					Creator:   personView{UserName: "ooli3"},
					Counts:    postCounts{Comments: 4},
					Community: communityFields{Name: "worldnews", ApDomain: "lemmy.world"},
				},
			},
		})
	})

	mux.HandleFunc("/api/alpha/comment/list", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("post_id") {
		case "2095688":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(commentsResponse{
				Comments: []commentView{
					{
						Comment: commentFields{ID: 2889644, Body: "top level comment", Published: "2026-09-23T15:11:46Z", Path: "0.2889644"},
						Creator: personView{UserName: "Canconda", Title: strPtr("Canaconda")},
						Counts:  commentCounts{Score: 67},
					},
					{
						Comment: commentFields{ID: 2889700, Body: "a reply", Published: "2026-09-23T15:20:00Z", Path: "0.2889644.2889700"},
						Creator: personView{UserName: "someoneelse"},
						Counts:  commentCounts{Score: 3},
					},
				},
			})
		case "2095999":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(commentsResponse{
				Comments: []commentView{
					{
						Comment: commentFields{ID: 91, Body: "crossposted-thread comment", Published: "2026-09-23T15:30:00Z", Path: "0.91"},
						Creator: personView{UserName: "worldnewsfan"},
						Counts:  commentCounts{Score: 12},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/alpha/post/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("community_name") != "politicalmemes" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(postListResponse{
			Posts: []postView{
				{Post: postFields{ID: 1, Title: "Community post"}, Creator: personView{UserName: "someone"}},
			},
		})
	})

	// /api/alpha/user: Poll's endpoint for a WatchTypePerson source.
	mux.HandleFunc("/api/alpha/user", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "ooli3" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(postListResponse{
			Posts: []postView{
				{Post: postFields{ID: 2, Title: "ooli3's post"}, Creator: personView{UserName: "ooli3"}},
			},
		})
	})

	return httptest.NewServer(mux)
}

func TestPollWatchTypeCommunityHitsPostList(t *testing.T) {
	srv := fakePieFedServer(t)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	src := models.Source{
		Platform:    models.PlatformPieFed,
		InstanceURL: srv.URL,
		WatchTarget: sql.NullString{String: "politicalmemes", Valid: true},
		WatchType:   models.WatchTypeCommunity,
	}
	posts, err := c.Poll(context.Background(), src)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(posts) != 1 || posts[0].Title != "Community post" {
		t.Fatalf("expected the community's single post, got %+v", posts)
	}
}

// TestPollWatchTypePersonHitsUserEndpoint checks a WatchType=person source
// polls /api/alpha/user, not the community-only /api/alpha/post/list.
func TestPollWatchTypePersonHitsUserEndpoint(t *testing.T) {
	srv := fakePieFedServer(t)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	src := models.Source{
		Platform:    models.PlatformPieFed,
		InstanceURL: srv.URL,
		WatchTarget: sql.NullString{String: "ooli3", Valid: true},
		WatchType:   models.WatchTypePerson,
	}
	posts, err := c.Poll(context.Background(), src)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(posts) != 1 || posts[0].Title != "ooli3's post" {
		t.Fatalf("expected ooli3's single post from /api/alpha/user, got %+v", posts)
	}
}

func TestFetchMetadataUsesPostIDNotResolveObject(t *testing.T) {
	srv := fakePieFedServer(t)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	postURL := srv.URL + "/c/politicalmemes@lemmy.ca/p/2095688/three-business-days-feels-generous"

	meta, err := c.FetchMetadata(context.Background(), postURL)
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if meta.Title != "three business days feels generous" {
		t.Errorf("Title = %q", meta.Title)
	}
	if meta.AuthorName != "ooli3" {
		t.Errorf("AuthorName = %q", meta.AuthorName)
	}
	if meta.AuthorHandle != "ooli3@sopuli.xyz" {
		t.Errorf("AuthorHandle = %q, want %q", meta.AuthorHandle, "ooli3@sopuli.xyz")
	}
	if meta.CommunityHandle != "politicalmemes@lemmy.ca" {
		t.Errorf("CommunityHandle = %q, want %q", meta.CommunityHandle, "politicalmemes@lemmy.ca")
	}
}

func TestFetchCommentsParsesRealShapeAndTree(t *testing.T) {
	srv := fakePieFedServer(t)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	postURL := srv.URL + "/c/politicalmemes@lemmy.ca/p/2095688/three-business-days-feels-generous"

	tree, err := c.FetchComments(context.Background(), postURL)
	if err != nil {
		t.Fatalf("FetchComments: %v", err)
	}
	if tree.Total != 113 {
		t.Errorf("Total = %d, want 113 (109 from the main post + 4 from its crosspost, not comment list length)", tree.Total)
	}
	// The original thread's comment comes first, with no Community tag.
	if len(tree.Comments) < 1 {
		t.Fatalf("expected at least 1 top-level comment, got %d", len(tree.Comments))
	}
	top := tree.Comments[0]
	if top.Body != "top level comment" {
		t.Errorf("top.Body = %q", top.Body)
	}
	if top.Author != "Canaconda" {
		t.Errorf("top.Author = %q, want display name (Title field), not raw user_name", top.Author)
	}
	if top.Community != "" {
		t.Errorf("top.Community = %q, want empty for the post's own thread", top.Community)
	}
	if len(top.Children) != 1 || top.Children[0].Body != "a reply" {
		t.Errorf("expected the reply nested under the top-level comment, got %+v", top.Children)
	}
}

// TestFetchCommentsConsolidatesCrossposts checks crosspost comments are
// included and tagged with their community.
func TestFetchCommentsConsolidatesCrossposts(t *testing.T) {
	srv := fakePieFedServer(t)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	postURL := srv.URL + "/c/politicalmemes@lemmy.ca/p/2095688/three-business-days-feels-generous"

	tree, err := c.FetchComments(context.Background(), postURL)
	if err != nil {
		t.Fatalf("FetchComments: %v", err)
	}
	if len(tree.Comments) != 2 {
		t.Fatalf("expected 2 top-level comments (1 own thread + 1 crossposted), got %d: %+v", len(tree.Comments), tree.Comments)
	}
	cross := tree.Comments[1]
	if cross.Body != "crossposted-thread comment" {
		t.Errorf("cross.Body = %q", cross.Body)
	}
	if cross.Community != "worldnews@lemmy.world" {
		t.Errorf("cross.Community = %q, want %q", cross.Community, "worldnews@lemmy.world")
	}
}

func TestFetchCommentCountSumsCrossposts(t *testing.T) {
	srv := fakePieFedServer(t)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	postURL := srv.URL + "/c/politicalmemes@lemmy.ca/p/2095688/three-business-days-feels-generous"

	n, err := c.FetchCommentCount(context.Background(), postURL)
	if err != nil {
		t.Fatalf("FetchCommentCount: %v", err)
	}
	if n != 113 {
		t.Errorf("FetchCommentCount = %d, want 113 (109 + 4, matching what FetchComments totals)", n)
	}
}

func TestPostIDFromURLHandlesBothURLShapes(t *testing.T) {
	cases := map[string]string{
		"https://piefed.example/c/politicalmemes@lemmy.ca/p/2095688/three-business-days-feels-generous": "2095688",
		"https://piefed.example/post/42": "42",
	}
	for url, want := range cases {
		got, err := postIDFromURL(url)
		if err != nil {
			t.Errorf("postIDFromURL(%q): %v", url, err)
			continue
		}
		if got != want {
			t.Errorf("postIDFromURL(%q) = %q, want %q", url, got, want)
		}
	}
}
