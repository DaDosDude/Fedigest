package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommunityPostURL(t *testing.T) {
	// A PieFed-style instance: no /api/v3, answers on /api/alpha.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/alpha/resolve_object" && r.URL.Query().Get("q") == "https://poster.example/post/25679398" {
			w.Write([]byte(`{"post":{"activity_alert":false,"post":{"id":4242}}}`))
			return
		}
		http.Error(w, `{"error":"couldnt_find_object"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r := &InstanceResolver{HTTP: srv.Client(), Scheme: "http"}
	ctx := context.Background()

	if got := r.CommunityPostURL(ctx, "microblogmemes@"+host, "https://poster.example/post/25679398"); got != srv.URL+"/post/4242" {
		t.Errorf("resolved = %q", got)
	}
	if got := r.CommunityPostURL(ctx, "microblogmemes@"+host, "https://poster.example/post/1"); got != "" {
		t.Errorf("unknown post should give \"\", got %q", got)
	}
	// Posted on the community's own instance: ap_id is already it, no lookup.
	if got := r.CommunityPostURL(ctx, "c@lemmy.world", "https://lemmy.world/post/9"); got != "https://lemmy.world/post/9" {
		t.Errorf("same-instance = %q", got)
	}
	for _, bad := range []string{"", "nohandle", "c@bad host/x"} {
		if got := r.CommunityPostURL(ctx, bad, "https://a.example/post/1"); got != "" {
			t.Errorf("handle %q should give \"\", got %q", bad, got)
		}
	}
}
