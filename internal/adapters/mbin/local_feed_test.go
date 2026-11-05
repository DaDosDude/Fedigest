package mbin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fedigest/internal/models"
)

func TestPollInstanceLocalFeed(t *testing.T) {
	var gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/entries", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[
			{"entryId":1,"title":"Local one","slug":"local-one","user":{"username":"a"},"magazine":{"name":"retro"},"createdAt":"2026-10-01T00:00:00+00:00"},
			{"entryId":2,"title":"Remote one","slug":"remote-one","apId":"https://elsewhere.example/m/news/t/9","user":{"username":"b@elsewhere.example"},"magazine":{"name":"news@elsewhere.example"},"createdAt":"2026-10-01T00:00:00+00:00"}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src := models.Source{Platform: models.PlatformMbin, InstanceURL: srv.URL, WatchType: models.WatchTypeInstance}
	posts, err := New().Poll(context.Background(), src)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.Contains(gotQuery, "federation=local") {
		t.Errorf("query %q doesn't ask for local entries", gotQuery)
	}
	if len(posts) != 1 || posts[0].Title != "Local one" {
		t.Fatalf("got %d posts %+v, want only the local magazine's entry", len(posts), posts)
	}
}

// TestRealMbinFieldNames feeds JSON shaped like Mbin's own responses
// (EntryResponseDto / EntryCommentResponseDto field names).
func TestRealMbinFieldNames(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/entry/5", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"entryId":5,"title":"Real shape","slug":"real-shape","numComments":7,
			"image":{"storageUrl":null,"sourceUrl":"https://img.example/a.png"},
			"user":{"userId":1,"username":"alice"},"magazine":{"magazineId":2,"name":"retro"},
			"createdAt":"2026-10-01T00:00:00+00:00","apId":null}`))
	})
	mux.HandleFunc("/api/entry/5/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"commentId":1,"body":"hi","favourites":4,"user":{"username":"bob"},"createdAt":"2026-10-01T01:00:00+00:00","children":[]}],"pagination":{"count":1}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New()
	md, err := a.FetchMetadata(context.Background(), srv.URL+"/m/retro/t/5/real-shape")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if md.ImageURL != "https://img.example/a.png" {
		t.Errorf("ImageURL = %q, want the sourceUrl fallback", md.ImageURL)
	}
	if n, err := a.FetchCommentCount(context.Background(), srv.URL+"/m/retro/t/5/real-shape"); err != nil || n != 7 {
		t.Errorf("FetchCommentCount = %d, %v; want 7 from numComments", n, err)
	}
	tree, err := a.FetchComments(context.Background(), srv.URL+"/m/retro/t/5/real-shape")
	if err != nil || len(tree.Comments) != 1 || tree.Comments[0].Score != 4 {
		t.Errorf("FetchComments = %+v, %v; want one comment with score 4", tree, err)
	}
}
