package adapters

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func statusFrom(t *testing.T, code int, contentType, body string, header http.Header) *StatusError {
	t.Helper()
	rec := httptest.NewRecorder()
	for k, v := range header {
		rec.Header()[k] = v
	}
	if contentType != "" {
		rec.Header().Set("Content-Type", contentType)
	}
	rec.WriteHeader(code)
	rec.WriteString(body)
	return NewStatusError(rec.Result(), "https://lemmy.example/api/v3/post?id=1")
}

func TestStatusErrorMessages(t *testing.T) {
	e := statusFrom(t, 400, "application/json", `{"error":"rate_limit_error"}`, nil)
	if !e.RateLimited() || !strings.HasSuffix(e.Error(), ": rate_limit_error") {
		t.Errorf("Lemmy rate limit: %v (limited %v)", e, e.RateLimited())
	}
	e = statusFrom(t, 400, "application/json", `{"error":"couldnt_find_post"}`, nil)
	if e.RateLimited() || !strings.HasSuffix(e.Error(), ": couldnt_find_post") {
		t.Errorf("other 400: %v", e)
	}
	e = statusFrom(t, 429, "text/plain", "Too many\nrequests", http.Header{"Retry-After": {"30"}})
	if !e.RateLimited() || e.RetryAfter != 30*time.Second || e.Message != "Too many requests" {
		t.Errorf("429: %v, retry after %v", e, e.RetryAfter)
	}
	e = statusFrom(t, 502, "text/html", "<html><body>Bad gateway</body></html>", nil)
	if e.Message != "" {
		t.Errorf("HTML body shouldn't become the message: %q", e.Message)
	}
}

func TestRateLimitThroughJoinedErrors(t *testing.T) {
	limited := &StatusError{Code: 400, Message: "rate_limit_error"}
	other := &StatusError{Code: 400, Message: "couldnt_find_post"}
	if ok, _ := RateLimit(errors.Join(other, fmt.Errorf("wrapped: %w", limited))); !ok {
		t.Error("a rate limit behind another status error was missed")
	}
	if ok, _ := RateLimit(errors.Join(other, ErrNotFound)); ok {
		t.Error("no rate limit reported as one")
	}
	for _, tc := range []struct {
		asked time.Duration
		n     int
		want  time.Duration
	}{
		{0, 1, time.Minute}, {0, 2, 2 * time.Minute}, {0, 3, 5 * time.Minute}, {0, 9, 5 * time.Minute},
		{10 * time.Second, 1, time.Minute}, {3 * time.Minute, 1, 3 * time.Minute}, {time.Hour, 1, 15 * time.Minute},
	} {
		if got := RateLimitPause(tc.asked, tc.n); got != tc.want {
			t.Errorf("RateLimitPause(%v, %d) = %v, want %v", tc.asked, tc.n, got, tc.want)
		}
	}
}
