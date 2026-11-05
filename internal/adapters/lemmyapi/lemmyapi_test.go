package lemmyapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

func fakeSourceFor(instanceURL string) models.Source {
	return models.Source{
		Platform:    models.PlatformLemmy,
		InstanceURL: instanceURL,
		WatchTarget: sql.NullString{String: "technology", Valid: true},
	}
}

func strPtr(s string) *string { return &s }

// fakeLemmyServer serves fixed /api/v3 responses. It deliberately doesn't
// serve resolve_object, so any accidental dependency on it fails.
func fakeLemmyServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v3/post", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "42" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(postResponse{
			PostView: postView{
				Post: postFields{
					ID:           42,
					Name:         "An interesting post",
					Body:         strPtr("Full post body text."),
					ThumbnailURL: strPtr("https://example.com/thumb.jpg"),
					Published:    "2026-09-20T12:00:00Z",
					APID:         "https://example.com/post/42",
				},
				Creator:   personView{Name: "someuser", ActorID: "https://example.com/u/someuser"},
				Counts:    postCounts{Comments: 3},
				Community: communityFields{Name: "technology", ActorID: "https://example.com/c/technology"},
			},
			// A crosspost of the same post in another community.
			CrossPosts: []postView{
				{
					Post:      postFields{ID: 99, Name: "An interesting post", Published: "2026-09-20T12:05:00Z"},
					Creator:   personView{Name: "someuser"},
					Counts:    postCounts{Comments: 2},
					Community: communityFields{Name: "nottheonion", ActorID: "https://lemmy.world/c/nottheonion"},
				},
			},
		})
	})

	mux.HandleFunc("/api/v3/comment/list", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("post_id") {
		case "42":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(commentsResponse{
				Comments: []commentView{
					{
						Comment: commentFields{ID: 1, Content: "top level", Published: "2026-09-20T12:01:00Z", Path: "0.1"},
						Creator: personView{Name: "alice"},
						Counts:  commentCounts{Score: 5},
					},
					{
						Comment: commentFields{ID: 2, Content: "a reply", Published: "2026-09-20T12:02:00Z", Path: "0.1.2"},
						Creator: personView{Name: "bob"},
						Counts:  commentCounts{Score: 2},
					},
					{
						Comment: commentFields{ID: 3, Content: "another top level", Published: "2026-09-20T12:03:00Z", Path: "0.3"},
						Creator: personView{Name: "carol"},
						Counts:  commentCounts{Score: 1},
					},
				},
			})
		case "99":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(commentsResponse{
				Comments: []commentView{
					{
						Comment: commentFields{ID: 201, Content: "crossposted-thread comment", Published: "2026-09-20T12:10:00Z", Path: "0.201"},
						Creator: personView{Name: "dana"},
						Counts:  commentCounts{Score: 8},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/v3/post/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Posts []postView `json:"posts"`
		}{
			Posts: []postView{
				{
					Post:    postFields{ID: 1, Name: "First post", Published: "2026-09-20T10:00:00Z"},
					Creator: personView{Name: "dave"},
				},
				{
					Post:    postFields{ID: 2, Name: "Second post", Published: "2026-09-20T11:00:00Z"},
					Creator: personView{Name: "erin"},
				},
			},
		})
	})

	// /api/v3/user: Poll's endpoint for a WatchTypePerson source.
	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "exampleuser" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Posts []postView `json:"posts"`
		}{
			Posts: []postView{
				{
					Post:    postFields{ID: 7, Name: "exampleuser's post", Published: "2026-09-20T10:00:00Z"},
					Creator: personView{Name: "exampleuser"},
				},
			},
		})
	})

	return httptest.NewServer(mux)
}

func TestFetchMetadataUsesPostIDNotResolveObject(t *testing.T) {
	srv := fakeLemmyServer(t)
	defer srv.Close()

	c := New()
	meta, err := c.FetchMetadata(context.Background(), srv.URL+"/post/42")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}

	if meta.Title != "An interesting post" {
		t.Errorf("Title = %q, want %q", meta.Title, "An interesting post")
	}
	if meta.Body != "Full post body text." {
		t.Errorf("Body = %q, want %q", meta.Body, "Full post body text.")
	}
	if meta.ImageURL != "https://example.com/thumb.jpg" {
		t.Errorf("ImageURL = %q", meta.ImageURL)
	}
	if meta.AuthorName != "someuser" {
		t.Errorf("AuthorName = %q", meta.AuthorName)
	}
	if meta.AuthorHandle != "someuser@example.com" {
		t.Errorf("AuthorHandle = %q, want %q", meta.AuthorHandle, "someuser@example.com")
	}
	if meta.CommunityHandle != "technology@example.com" {
		t.Errorf("CommunityHandle = %q, want %q", meta.CommunityHandle, "technology@example.com")
	}
	if meta.CreatedAt.IsZero() {
		t.Error("CreatedAt not parsed")
	}
}

func TestFetchCommentCountSumsCrossposts(t *testing.T) {
	srv := fakeLemmyServer(t)
	defer srv.Close()

	c := New()
	count, err := c.FetchCommentCount(context.Background(), srv.URL+"/post/42")
	if err != nil {
		t.Fatalf("FetchCommentCount: %v", err)
	}
	if count != 5 {
		t.Errorf("count = %d, want 5 (3 from the main post + 2 from its crosspost)", count)
	}
}

func TestFetchCommentsBuildsTree(t *testing.T) {
	srv := fakeLemmyServer(t)
	defer srv.Close()

	c := New()
	tree, err := c.FetchComments(context.Background(), srv.URL+"/post/42")
	if err != nil {
		t.Fatalf("FetchComments: %v", err)
	}

	if tree.Total != 5 {
		t.Errorf("Total = %d, want 5 (3 + 2, not comment list length)", tree.Total)
	}

	// Comment 1 (path "0.1") has child 2 ("0.1.2"); crosspost comments
	// come after the original thread's roots.
	var topByAuthor = map[string]adapters.Comment{}
	for _, cm := range tree.Comments {
		topByAuthor[cm.Author] = cm
	}

	alice, ok := topByAuthor["alice"]
	if !ok {
		t.Fatal("expected a top-level comment from alice")
	}
	if len(alice.Children) != 1 {
		t.Fatalf("expected alice's comment to have 1 child, got %d", len(alice.Children))
	}
	if alice.Children[0].Author != "bob" {
		t.Errorf("child author = %q, want bob", alice.Children[0].Author)
	}
	if alice.Community != "" {
		t.Errorf("alice.Community = %q, want empty for the post's own thread", alice.Community)
	}

	if _, ok := topByAuthor["carol"]; !ok {
		t.Error("expected a second top-level comment from carol")
	}

	dana, ok := topByAuthor["dana"]
	if !ok {
		t.Fatal("expected a consolidated top-level comment from dana (on the crossposted copy)")
	}
	if dana.Community != "nottheonion@lemmy.world" {
		t.Errorf("dana.Community = %q, want %q", dana.Community, "nottheonion@lemmy.world")
	}
}

func TestPoll(t *testing.T) {
	srv := fakeLemmyServer(t)
	defer srv.Close()

	c := New()
	posts, err := c.Poll(context.Background(), fakeSourceFor(srv.URL))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(posts))
	}
	if posts[0].Title != "First post" {
		t.Errorf("posts[0].Title = %q", posts[0].Title)
	}
}

// TestPollWatchTypePersonHitsUserEndpoint checks a WatchType=person source
// polls /api/v3/user, not the community-only /api/v3/post/list.
func TestPollWatchTypePersonHitsUserEndpoint(t *testing.T) {
	srv := fakeLemmyServer(t)
	defer srv.Close()

	src := fakeSourceFor(srv.URL)
	src.WatchTarget = sql.NullString{String: "exampleuser", Valid: true}
	src.WatchType = models.WatchTypePerson

	c := New()
	posts, err := c.Poll(context.Background(), src)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(posts) != 1 || posts[0].Title != "exampleuser's post" {
		t.Fatalf("expected exampleuser's single post from /api/v3/user, got %+v", posts)
	}
}

func TestFetchMetadataNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/post", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	_, err := c.FetchMetadata(context.Background(), srv.URL+"/post/999")
	if err != adapters.ErrNotFound {
		t.Errorf("err = %v, want adapters.ErrNotFound", err)
	}
}

func TestPostIDFromURLHandlesBothURLShapes(t *testing.T) {
	cases := map[string]string{
		"https://lemmy.example/post/43727384":                     "43727384",
		"https://piefed.example/c/news@lemmy.example/p/42/a-slug": "42",
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

// TestPollWatchTypeInstanceLocalFeed checks an instance source asks for the
// Local feed and drops posts from communities hosted elsewhere.
func TestPollWatchTypeInstanceLocalFeed(t *testing.T) {
	var gotQuery string
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/api/v3/post/list", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"posts":[
			{"post":{"id":1,"name":"Local one","ap_id":"%[1]s/post/1","published":"2026-10-01T00:00:00Z"},"community":{"name":"retro","actor_id":"%[1]s/c/retro"},"creator":{"name":"a","actor_id":"%[1]s/u/a"}},
			{"post":{"id":2,"name":"Remote one","ap_id":"https://elsewhere.example/post/9","published":"2026-10-01T00:00:00Z"},"community":{"name":"news","actor_id":"https://elsewhere.example/c/news"},"creator":{"name":"b","actor_id":"https://elsewhere.example/u/b"}}
		]}`, srv.URL)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	src := fakeSourceFor(srv.URL)
	src.WatchTarget = sql.NullString{}
	src.WatchType = models.WatchTypeInstance

	posts, err := New().Poll(context.Background(), src)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.Contains(gotQuery, "type_=Local") {
		t.Errorf("query %q doesn't ask for the Local feed", gotQuery)
	}
	if len(posts) != 1 || posts[0].Title != "Local one" {
		t.Fatalf("got %+v, want only the local community's post", posts)
	}
}
