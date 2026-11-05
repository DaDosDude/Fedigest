package adapters

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientTimeoutCountsAsDeadline(t *testing.T) {
	withSpacing(t, time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := NewHTTPClient(2 * time.Second)
	if _, err := c.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := c.Get(srv.URL)
	var pe *PausedError
	if !errors.As(err, &pe) || !pe.Busy {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}
