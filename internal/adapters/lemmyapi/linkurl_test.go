package lemmyapi

import "testing"

func TestMetadataLinkURL(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		url, wantLink, wantImage string
	}{
		{"https://news.example.com/story", "https://news.example.com/story", ""},
		{"https://img.example.com/pic.webp", "", "https://img.example.com/pic.webp"},
		{"", "", ""},
	}
	for _, c := range cases {
		var pv postView
		if c.url != "" {
			pv.Post.URL = s(c.url)
		}
		m := metadataFromPostView(pv)
		if m.LinkURL != c.wantLink || m.ImageURL != c.wantImage {
			t.Errorf("url %q: LinkURL=%q ImageURL=%q, want %q / %q", c.url, m.LinkURL, m.ImageURL, c.wantLink, c.wantImage)
		}
	}
}
