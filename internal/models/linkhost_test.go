package models

import (
	"database/sql"
	"testing"
)

func TestLinkHostAndEffectiveBody(t *testing.T) {
	p := &Pick{LinkURL: sql.NullString{String: "https://www.example.com/x", Valid: true}}
	if h := p.LinkHost(); h != "example.com" {
		t.Errorf("LinkHost = %q", h)
	}
	p.LinkURL = sql.NullString{String: "javascript:alert(1)", Valid: true}
	if h := p.LinkHost(); h != "" {
		t.Errorf("non-http link should be ignored, got %q", h)
	}
	p = &Pick{Excerpt: "teaser", Body: "  \n"}
	if b := p.EffectiveBody(); b != "teaser" {
		t.Errorf("blank body should fall back to excerpt, got %q", b)
	}
	p.Body = "full"
	if b := p.EffectiveBody(); b != "full" {
		t.Errorf("EffectiveBody = %q", b)
	}
}

func TestDisplayDates(t *testing.T) {
	p := &Pick{CreatedAt: "2026-09-20 10:00:00"}
	if d := p.DisplayDate(); d != "20 Sep 2026" {
		t.Errorf("draft DisplayDate = %q", d)
	}
	p.PublishedAt = sql.NullString{String: "2026-09-25T08:30:00Z", Valid: true}
	if d := p.DisplayDate(); d != "25 Sep 2026" {
		t.Errorf("DisplayDate = %q", d)
	}
	if d := p.OriginalDisplayDate(); d != "" {
		t.Errorf("no original date should be empty, got %q", d)
	}
	p.OriginalPublishedAt = sql.NullString{String: "2026-09-24T23:59:59.123456Z", Valid: true}
	if d := p.OriginalDisplayDate(); d != "24 Sep 2026" {
		t.Errorf("OriginalDisplayDate = %q", d)
	}
}

func TestOriginalHomeURL(t *testing.T) {
	p := &Pick{OriginalURL: sql.NullString{String: "https://piefed.social/c/x@lemmy.world/p/1/y", Valid: true}}
	if u := p.OriginalHomeURL(); u != p.OriginalURL.String {
		t.Errorf("without ap_id should fall back to pasted URL, got %q", u)
	}
	p.OriginalApID = sql.NullString{String: "https://poster.example/post/25679398", Valid: true}
	if u := p.OriginalHomeURL(); u != "https://poster.example/post/25679398" {
		t.Errorf("OriginalHomeURL = %q", u)
	}
}

func TestOriginalHomeURLPrefersCommunity(t *testing.T) {
	p := &Pick{
		OriginalURL:      sql.NullString{String: "https://piefed.social/c/microblogmemes@lemmy.world/p/2422327/x", Valid: true},
		OriginalApID:     sql.NullString{String: "https://poster.example/post/25679398", Valid: true},
		CommunityPostURL: sql.NullString{String: "https://lemmy.world/post/123", Valid: true},
	}
	if u := p.OriginalHomeURL(); u != "https://lemmy.world/post/123" {
		t.Errorf("OriginalHomeURL = %q, want the community instance URL", u)
	}
}

func TestDisplayExcerpt(t *testing.T) {
	p := &Pick{Excerpt: "Cut off at 28", Body: "Cut off at 280 chars — the full body."}
	if got := p.DisplayExcerpt(); got != "Cut off at 28" {
		t.Errorf("hand-written excerpt should be kept, got %q", got)
	}
	p.ExcerptAuto = true
	if got := p.DisplayExcerpt(); got != p.Body {
		t.Errorf("auto excerpt should show the full body, got %q", got)
	}
	// Auto-ingest: no curator body, the original post's text instead.
	p = &Pick{Excerpt: "Title", ExcerptAuto: true, OriginalBodyCache: sql.NullString{String: "Original post text", Valid: true}}
	if got := p.DisplayExcerpt(); got != "Original post text" {
		t.Errorf("got %q", got)
	}
	// Nothing to show: falls back to the stored excerpt (the title).
	p = &Pick{Excerpt: "Title", ExcerptAuto: true}
	if got := p.DisplayExcerpt(); got != "Title" {
		t.Errorf("got %q", got)
	}
}
