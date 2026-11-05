package adapters

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHTTPClientSendsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.UserAgent() }))
	defer srv.Close()
	old := UserAgent
	UserAgent = "Fedigest/1.0 (+https://fedigest.example)"
	defer func() { UserAgent = old }()
	if _, err := NewHTTPClient(5 * time.Second).Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if got != "Fedigest/1.0 (+https://fedigest.example)" {
		t.Errorf("User-Agent = %q", got)
	}
}
