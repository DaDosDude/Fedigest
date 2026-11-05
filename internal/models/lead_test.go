package models

import (
	"database/sql"
	"strings"
	"testing"
)

func words(n int) string {
	return strings.TrimSpace(strings.Repeat("word ", n))
}

func TestLeadParagraphs(t *testing.T) {
	long := words(LeadMinWords)
	short := "Found this today."
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"one long paragraph", long + "\n\nSecond.\n\nThird.", long},
		{"short first takes the second", short + "\n\n" + long + "\n\nThird.", short + "\n\n" + long},
		{"two at most", short + "\n\nAlso short.\n\nThird.", short + "\n\nAlso short."},
		{"CRLF", short + "\r\n\r\nNext.\r\n\r\nLast.", short + "\n\nNext."},
		{"skips heading, image, rule", "# Title\n\n![pic](https://img.example/a.png)\n\n---\n\n" + long, long},
		{"skips setext heading", "Title\n=====\n\n" + long, long},
		{"skips code and tables", "```\ncode\n\nmore\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n    indented\n\n" + long, long},
		{"keeps a multi-line paragraph and list", "Line one\nline two.\n\n- a\n- b\n\nThird.", "Line one\nline two.\n\n- a\n- b"},
		{"spoiler counts as a block", "::: spoiler CW\nhidden\n\nstill hidden\n:::\n\n" + short + "\n\nThird.", "::: spoiler CW\nhidden\n\nstill hidden\n:::\n\n" + short},
		{"nested spoiler stays whole", "::: spoiler A\n::: spoiler B\nx\n:::\n\ny\n:::\n\n" + long, "::: spoiler A\n::: spoiler B\nx\n:::\n\ny\n:::\n\n" + long},
		{"reference links kept", "See [the site][1].\n\n" + long + "\n\nMore.\n\n[1]: https://example.org", "See [the site][1].\n\n" + long + "\n\n[1]: https://example.org"},
		{"only an image", "![pic](https://img.example/a.png)", ""},
	} {
		if got := LeadParagraphs(tc.in); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestAutoExcerptUsesLead(t *testing.T) {
	original := "Opening paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	p := &Pick{Excerpt: "Title", ExcerptAuto: true, UseOriginalAsBody: true,
		OriginalBodyCache: sql.NullString{String: original, Valid: true}}
	if got := p.DisplayExcerpt(); got != "Opening paragraph.\n\nSecond paragraph." {
		t.Errorf("DisplayExcerpt = %q", got)
	}
	// The post page shows the original once, in full, not the lead above it.
	if got := p.EffectiveBody(); got != "" {
		t.Errorf("EffectiveBody = %q, want empty", got)
	}
	// A hand-written excerpt still stands in for a missing body.
	p.ExcerptAuto = false
	if got := p.EffectiveBody(); got != "Title" {
		t.Errorf("EffectiveBody = %q", got)
	}
	// The curator's own body comes first.
	p = &Pick{ExcerptAuto: true, Body: "Mine.\n\nMore of mine.\n\nEnd.",
		OriginalBodyCache: sql.NullString{String: original, Valid: true}}
	if got := p.DisplayExcerpt(); got != "Mine.\n\nMore of mine." {
		t.Errorf("DisplayExcerpt = %q", got)
	}
	// No paragraph (an image on its own): shown as it is.
	p = &Pick{Excerpt: "Title", ExcerptAuto: true, OriginalBodyCache: sql.NullString{String: "![pic](https://img.example/a.png)", Valid: true}}
	if got := p.DisplayExcerpt(); got != "![pic](https://img.example/a.png)" {
		t.Errorf("DisplayExcerpt = %q", got)
	}
}

func TestHasMore(t *testing.T) {
	orig := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	for _, tc := range []struct {
		name string
		p    Pick
		want bool
	}{
		{"excerpt only", Pick{Excerpt: "Teaser"}, false},
		{"excerpt and body", Pick{Excerpt: "Teaser", Body: "Full text"}, true},
		{"body same as excerpt", Pick{Excerpt: "Same", Body: "Same\n"}, false},
		{"auto, one paragraph", Pick{ExcerptAuto: true, UseOriginalAsBody: true, OriginalBodyCache: orig("Just this.")}, false},
		{"auto, longer original", Pick{ExcerptAuto: true, UseOriginalAsBody: true, OriginalBodyCache: orig("One.\n\nTwo.\n\nThree.")}, true},
		{"original not shown", Pick{Excerpt: "Teaser", OriginalBodyCache: orig("Long original")}, false},
		{"hand-written excerpt, original shown", Pick{Excerpt: "Teaser", UseOriginalAsBody: true, OriginalBodyCache: orig("Long original")}, true},
	} {
		if got := tc.p.HasMore(); got != tc.want {
			t.Errorf("%s: HasMore = %v", tc.name, got)
		}
	}
}
