package piefed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"fedigest/internal/models"
)

func TestPollInstanceLocalFeed(t *testing.T) {
	var gotQuery string
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/alpha/post/list", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		host, _ := url.Parse(srv.URL)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"posts":[
			{"post":{"id":1,"title":"Local one","ap_id":"%[1]s/post/1","published":"2026-10-01T00:00:00Z"},"community":{"name":"retro","ap_domain":"%[2]s"},"creator":{"user_name":"a"}},
			{"post":{"id":2,"title":"Remote one","ap_id":"https://elsewhere.example/post/9","published":"2026-10-01T00:00:00Z"},"community":{"name":"news","ap_domain":"elsewhere.example"},"creator":{"user_name":"b"}}
		]}`, srv.URL, host.Host)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	src := models.Source{Platform: models.PlatformPieFed, InstanceURL: srv.URL, WatchType: models.WatchTypeInstance}
	posts, err := New().Poll(context.Background(), src)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.Contains(gotQuery, "type_=Local") {
		t.Errorf("query %q doesn't ask for the Local feed", gotQuery)
	}
	if len(posts) != 1 || posts[0].Title != "Local one" {
		t.Fatalf("got %d posts %+v, want only the local community's post", len(posts), posts)
	}
}
