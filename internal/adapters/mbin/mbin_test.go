package mbin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

func strPtr(s string) *string { return &s }

func fakeMbinServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/entry/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entryResponse{
			Title:        "An Mbin entry",
			Body:         strPtr("Entry body text."),
			Image:        &mbinImage{StorageURL: "https://example.com/img.png"},
			User:         mbinUser{Username: "entryauthor"},
			Magazine:     mbinMagazine{Name: "technology"},
			CreatedAt:    "2026-09-20T12:00:00Z",
			CommentCount: 4,
		})
	})

	// Entries and microblog posts are numbered separately: entry 99 is
	// unrelated to post 99, and a /p/ URL must never fetch it.
	mux.HandleFunc("/api/entry/99", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entryResponse{Title: "UNRELATED ENTRY", CommentCount: 50})
	})
	mux.HandleFunc("/api/post/99", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(postResponse{
			Body:         strPtr("A short microblog post.\nWith a second line."),
			User:         mbinUser{Username: "@postauthor@remote.example"},
			Magazine:     mbinMagazine{Name: "news@remote.example"},
			CreatedAt:    "2026-09-21T09:00:00Z",
			CommentCount: 1,
		})
	})

	mux.HandleFunc("/api/magazine/name/technology", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(magazineResponse{MagazineID: 7})
	})
	mux.HandleFunc("/api/magazine/name/doesnotexist", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/magazine/7/entries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entryListResponse{
			Items: []entryListItem{
				{
					EntryID:      101,
					Title:        "Polled entry",
					Slug:         "polled-entry",
					Body:         strPtr("Some polled body."),
					User:         mbinUser{Username: "poller"},
					Magazine:     mbinMagazine{Name: "technology"},
					CreatedAt:    "2026-09-22T08:00:00Z",
					CommentCount: 2,
				},
			},
		})
	})

	mux.HandleFunc("/api/users/name/exampleuser", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"userId":12,"username":"exampleuser"}`))
	})
	mux.HandleFunc("/api/users/12/entries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entryListResponse{
			Items: []entryListItem{
				{
					EntryID:   202,
					Title:     "exampleuser's entry",
					Slug:      "exampleusers-entry",
					User:      mbinUser{Username: "exampleuser"},
					Magazine:  mbinMagazine{Name: "retro"},
					CreatedAt: "2026-09-23T08:00:00Z",
				},
			},
		})
	})

	mux.HandleFunc("/api/entry/42/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(commentsListResponse{
			Items: []mbinComment{
				{
					Body:      "top comment",
					User:      mbinUser{Username: "alice"},
					CreatedAt: "2026-09-20T12:05:00Z",
					Children: []mbinComment{
						{Body: "a reply", User: mbinUser{Username: "bob"}, CreatedAt: "2026-09-20T12:06:00Z"},
					},
				},
			},
		})
	})

	return httptest.NewServer(mux)
}

func TestFetchMetadataEntry(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	meta, err := a.FetchMetadata(context.Background(), srv.URL+"/m/test/t/42/an-entry")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if meta.Title != "An Mbin entry" {
		t.Errorf("Title = %q", meta.Title)
	}
	if meta.AuthorName != "entryauthor" {
		t.Errorf("AuthorName = %q", meta.AuthorName)
	}
	// A local username is qualified with the fetching instance's host.
	if want := "entryauthor@" + hostOf(srv.URL); meta.AuthorHandle != want {
		t.Errorf("AuthorHandle = %q, want %q", meta.AuthorHandle, want)
	}
	if want := "technology@" + hostOf(srv.URL); meta.CommunityHandle != want {
		t.Errorf("CommunityHandle = %q, want %q", meta.CommunityHandle, want)
	}
	if meta.ImageURL != "https://example.com/img.png" {
		t.Errorf("ImageURL = %q", meta.ImageURL)
	}
}

func TestFetchMetadataPostURLUsesPostAPI(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	meta, err := a.FetchMetadata(context.Background(), srv.URL+"/m/test/p/99")
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if meta.Title != "A short microblog post." {
		t.Errorf("Title = %q, want the post's first line (not entry 99)", meta.Title)
	}
	if meta.AuthorName != "postauthor@remote.example" {
		t.Errorf("AuthorName = %q", meta.AuthorName)
	}
	// Mbin's "@name@instance" loses its leading "@"; a remote magazine
	// name already carries its instance.
	if meta.AuthorHandle != "postauthor@remote.example" {
		t.Errorf("AuthorHandle = %q, want %q", meta.AuthorHandle, "postauthor@remote.example")
	}
	if meta.CommunityHandle != "news@remote.example" {
		t.Errorf("CommunityHandle = %q, want news@remote.example", meta.CommunityHandle)
	}
	if n, err := a.FetchCommentCount(context.Background(), srv.URL+"/m/test/p/99"); err != nil || n != 1 {
		t.Errorf("FetchCommentCount = %d, %v; want the post's 1", n, err)
	}
}

func TestFetchCommentsNested(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	tree, err := a.FetchComments(context.Background(), srv.URL+"/m/test/t/42/an-entry")
	if err != nil {
		t.Fatalf("FetchComments: %v", err)
	}
	if len(tree.Comments) != 1 {
		t.Fatalf("expected 1 top-level comment, got %d", len(tree.Comments))
	}
	if tree.Comments[0].Author != "alice" {
		t.Errorf("top author = %q", tree.Comments[0].Author)
	}
	if len(tree.Comments[0].Children) != 1 || tree.Comments[0].Children[0].Author != "bob" {
		t.Error("expected nested reply from bob")
	}
}

func TestParsePostURL(t *testing.T) {
	instance, kind, id, err := parsePostURL("https://mbin.example/m/tech/t/12345/some-slug")
	if err != nil {
		t.Fatalf("parsePostURL: %v", err)
	}
	if instance != "https://mbin.example" || kind != kindEntry || id != 12345 {
		t.Errorf("got %q, kind %d, id %d", instance, kind, id)
	}
	if _, kind, id, err := parsePostURL("https://mbin.example/m/tech/p/77/a-post"); err != nil || kind != kindPost || id != 77 {
		t.Errorf("post URL: kind %d, id %d, err %v", kind, id, err)
	}
	// Another platform's URL: ErrNotFound, so the caller tries the others.
	if _, _, _, err := parsePostURL("https://lemmy.example/post/5"); !errors.Is(err, adapters.ErrNotFound) {
		t.Errorf("non-Mbin URL: err = %v, want ErrNotFound", err)
	}
}

func TestPollListsMagazineEntries(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	items, err := a.Poll(context.Background(), models.Source{
		Platform:    models.PlatformMbin,
		InstanceURL: srv.URL,
		WatchTarget: sql.NullString{String: "technology", Valid: true},
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	got := items[0]
	if got.Title != "Polled entry" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.AuthorName != "poller" {
		t.Errorf("AuthorName = %q", got.AuthorName)
	}
	wantURL := srv.URL + "/m/technology/t/101/polled-entry"
	if got.CommentURL != wantURL {
		t.Errorf("CommentURL = %q, want %q", got.CommentURL, wantURL)
	}

	// The reconstructed URL must be parseable so comments can be re-fetched.
	if _, _, id, err := parsePostURL(got.CommentURL); err != nil || id != 101 {
		t.Errorf("reconstructed CommentURL isn't parseable back: id=%d err=%v", id, err)
	}
}

// TestPollWatchTypePersonHitsUserEntriesEndpoint checks a WatchType=person
// source polls /api/users/{username}/entries, not the magazine listing.
func TestPollWatchTypePersonHitsUserEntriesEndpoint(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	items, err := a.Poll(context.Background(), models.Source{
		Platform:    models.PlatformMbin,
		InstanceURL: srv.URL,
		WatchTarget: sql.NullString{String: "exampleuser", Valid: true},
		WatchType:   models.WatchTypePerson,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 || items[0].Title != "exampleuser's entry" {
		t.Fatalf("expected exampleuser's single entry from /api/users/12/entries, got %+v", items)
	}
}

func TestPollUnknownMagazine(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	_, err := a.Poll(context.Background(), models.Source{
		Platform:    models.PlatformMbin,
		InstanceURL: srv.URL,
		WatchTarget: sql.NullString{String: "doesnotexist", Valid: true},
	})
	if err == nil {
		t.Fatal("expected an error for an unresolvable magazine")
	}
}

func TestPollNoWatchTarget(t *testing.T) {
	srv := fakeMbinServer(t)
	defer srv.Close()

	a := New()
	_, err := a.Poll(context.Background(), models.Source{
		Platform:    models.PlatformMbin,
		InstanceURL: srv.URL,
	})
	if err == nil {
		t.Fatal("expected an error when watch_target isn't set")
	}
}

func TestFetchMetadataNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New()
	_, err := a.FetchMetadata(context.Background(), srv.URL+"/m/test/t/1/x")
	if err != adapters.ErrNotFound {
		t.Errorf("err = %v, want adapters.ErrNotFound", err)
	}
}
