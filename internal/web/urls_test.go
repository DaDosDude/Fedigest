package web

import "testing"

func TestInstanceBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"lemmy.world":                   "https://lemmy.world",
		" Lemmy.World/ ":                "https://lemmy.world",
		"https://piefed.social/":        "https://piefed.social",
		"https://piefed.social/c/retro": "https://piefed.social",
		"http://localhost:8536":         "http://localhost:8536",
		"lemmy.example/post/123":        "https://lemmy.example",
	} {
		if got := instanceBaseURL(in); got != want {
			t.Errorf("instanceBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := withScheme("lemmy.world/post/5"); got != "https://lemmy.world/post/5" {
		t.Errorf("withScheme = %q", got)
	}
	if got := withScheme("https://a.example/x"); got != "https://a.example/x" {
		t.Errorf("withScheme changed a full URL: %q", got)
	}
}
