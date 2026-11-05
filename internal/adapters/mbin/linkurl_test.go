package mbin

import "testing"

func TestLinkURL(t *testing.T) {
	s := func(v string) *string { return &v }
	if got := linkURL(s("https://news.example.com/a")); got != "https://news.example.com/a" {
		t.Errorf("link: %q", got)
	}
	if got := linkURL(s("https://x.example/p.PNG")); got != "" {
		t.Errorf("image url should not be a link: %q", got)
	}
	if got := linkURL(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
}
