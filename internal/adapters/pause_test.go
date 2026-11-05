package adapters

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPauseHoldsEveryRequestToTheInstance(t *testing.T) {
	ResetPauses()
	t.Cleanup(ResetPauses)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"rate_limit_error"}`))
	}))
	defer srv.Close()

	c := NewHTTPClient(5 * time.Second)
	resp, err := c.Get(srv.URL + "/api/v3/post?id=1")
	if err != nil {
		t.Fatal(err)
	}
	se := NewStatusError(resp, srv.URL)
	resp.Body.Close()
	if !se.RateLimited() {
		t.Fatalf("not seen as a rate limit: %v", se)
	}
	// Any other request to that instance now fails at once, unsent.
	_, err = c.Get(srv.URL + "/api/v3/comment/list?post_id=2")
	var pe *PausedError
	if !errors.As(err, &pe) || hits.Load() != 1 {
		t.Fatalf("second request: %v (server hit %d times)", err, hits.Load())
	}
	if limited, left := RateLimit(err); !limited || left <= 0 || left > time.Minute {
		t.Errorf("RateLimit = %v %v", limited, left)
	}
	if !NothingSent(errors.Join(err, fmt.Errorf("wrapped: %w", err))) || NothingSent(errors.Join(err, ErrNotFound)) {
		t.Error("NothingSent")
	}
}

func TestPauseGrowsAndResets(t *testing.T) {
	ResetPauses()
	t.Cleanup(func() { ResetPauses(); pauses.now = time.Now })
	now := time.Unix(1_000_000, 0)
	pauses.now = func() time.Time { return now }

	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		if got := pauseHost("a.example", 0); got != want {
			t.Errorf("refusal %d: pause %v, want %v", i+1, got, want)
		}
		now = now.Add(want) // the pause runs out, then it refuses again
	}
	if PauseLeft("A.example") != 0 {
		t.Error("pause still running")
	}
	if pauseHost("a.example", 10*time.Minute) != 10*time.Minute {
		t.Error("a longer Retry-After wasn't used")
	}
	now = now.Add(10*time.Minute + pauseResetAfter + time.Second)
	if got := pauseHost("a.example", 0); got != time.Minute {
		t.Errorf("after a quiet spell: %v, want a minute again", got)
	}
	if PauseLeft("b.example") != 0 {
		t.Error("another instance paused too")
	}
}
