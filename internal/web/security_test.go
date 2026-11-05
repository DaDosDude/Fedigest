package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCrossOriginProtect(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := crossOriginProtect(ok)

	cases := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"GET always allowed", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 204},
		{"same-origin POST", "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		{"typed-in navigation", "POST", map[string]string{"Sec-Fetch-Site": "none"}, 204},
		{"cross-site POST", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"other subdomain POST", "POST", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"old browser, matching Origin", "POST", map[string]string{"Origin": "https://fedigest.example"}, 204},
		{"old browser, other Origin", "POST", map[string]string{"Origin": "https://evil.example"}, 403},
		{"non-browser client", "POST", nil, 204},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "https://fedigest.example/admin/picks/1/delete", nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("got %d, want %d", w.Code, c.want)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.1:5555" // Docker gateway: the proxy in front of us
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(r); got != "5.6.7.8" {
		t.Errorf("behind proxy: got %q, want last X-Forwarded-For entry", got)
	}
	r.RemoteAddr = "9.9.9.9:5555" // public peer: header can't be trusted
	if got := clientIP(r); got != "9.9.9.9" {
		t.Errorf("direct: got %q, want peer address", got)
	}
}

func TestAttemptLimiter(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	l := newAttemptLimiter(15*time.Minute, 15*time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		l.fail("ip", 5)
	}
	if locked, _ := l.locked("ip"); locked {
		t.Fatal("locked after 4 of 5 failures")
	}
	l.fail("ip", 5)
	if locked, left := l.locked("ip"); !locked || left != 15*time.Minute {
		t.Fatalf("after 5 failures: locked=%v left=%v", locked, left)
	}
	now = now.Add(16 * time.Minute)
	if locked, _ := l.locked("ip"); locked {
		t.Fatal("still locked after the lockout period")
	}

	// Failures spread wider than the window don't add up.
	for i := 0; i < 4; i++ {
		l.fail("slow", 5)
	}
	now = now.Add(20 * time.Minute)
	l.fail("slow", 5)
	if locked, _ := l.locked("slow"); locked {
		t.Fatal("locked by failures outside the window")
	}

	// A successful login clears the count.
	for i := 0; i < 4; i++ {
		l.fail("user", 5)
	}
	l.reset("user")
	l.fail("user", 5)
	if locked, _ := l.locked("user"); locked {
		t.Fatal("reset didn't clear the count")
	}
}

func TestViewCounterOncePerDay(t *testing.T) {
	c := &viewCounter{}
	if !c.firstToday("1.2.3.4", "Firefox", 1) {
		t.Fatal("first view not counted")
	}
	if c.firstToday("1.2.3.4", "Firefox", 1) {
		t.Fatal("repeat view counted")
	}
	if !c.firstToday("1.2.3.4", "Firefox", 2) || !c.firstToday("5.6.7.8", "Firefox", 1) {
		t.Fatal("other pick or other visitor not counted")
	}
	for _, ua := range []string{"Mozilla/5.0 (compatible; Googlebot/2.1)", "Mastodon/4.3 (http.rb)", "Lemmy/0.19", "curl/8.0"} {
		if !botUA.MatchString(ua) {
			t.Errorf("%q not treated as a bot", ua)
		}
	}
	if botUA.MatchString("Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0") {
		t.Error("a normal browser treated as a bot")
	}
}
