package models

import (
	"strings"
	"testing"
)

func TestSpoilerRendering(t *testing.T) {
	src := "Source: link\n\n::: spoiler transcript\n@someone: hidden **text**\n:::\n\nafter"
	h, err := RenderMarkdown(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<details class="spoiler"><summary>transcript</summary>`,
		"<strong>text</strong>",
		"</details>",
		"<p>after</p>",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	if strings.Contains(h, ":::") {
		t.Errorf("raw ::: left in output:\n%s", h)
	}

	plain, _ := PlainTextFromMarkdown(src)
	if strings.Contains(plain, "hidden") {
		t.Errorf("plain text reveals spoiler content: %q", plain)
	}
	if !strings.Contains(plain, "[Spoiler: transcript]") || !strings.Contains(plain, "after") {
		t.Errorf("plain = %q", plain)
	}
}

func TestSpoilerEdgeCases(t *testing.T) {
	// Unclosed (truncated excerpt): runs to the end, still hidden.
	plain, _ := PlainTextFromMarkdown("intro\n::: spoiler big reveal\nthe butler did i")
	if strings.Contains(plain, "butler") || !strings.Contains(plain, "[Spoiler: big reveal]") {
		t.Errorf("unclosed: %q", plain)
	}
	// No title.
	h, _ := RenderMarkdown(":::spoiler\nx\n:::")
	if !strings.Contains(h, "<summary>Spoiler</summary>") {
		t.Errorf("untitled: %s", h)
	}
	// Nested.
	h, _ = RenderMarkdown("::: spoiler outer\na\n::: spoiler inner\nb\n:::\nc\n:::\nd")
	if strings.Count(h, "<details") != 2 || !strings.Contains(h, "<p>c</p>\n</details>") || !strings.HasSuffix(strings.TrimSpace(h), "<p>d</p>") {
		t.Errorf("nested: %s", h)
	}
	// Inside a code fence: left alone.
	h, _ = RenderMarkdown("```\n::: spoiler not one\n:::\n```")
	if strings.Contains(h, "<details") {
		t.Errorf("fenced spoiler was rendered: %s", h)
	}
	// Title is escaped, raw HTML in content still escaped.
	h, _ = RenderMarkdown("::: spoiler <b>x</b>\n<script>alert(1)</script>\n:::")
	if strings.Contains(h, "<b>x</b>") || strings.Contains(h, "<script>") {
		t.Errorf("unescaped HTML: %s", h)
	}
}

func TestExcerptHTML(t *testing.T) {
	h, err := ExcerptHTML("What do you **think**? 1 < 2\n\n![shot.png](https://img.example/shot.png)\n\nSee [the post](https://x.example/p).\n\n::: spoiler my \"title\"\nsecret stuff\n:::\n\n<script>alert(1)</script>")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<strong>think</strong>",
		"1 &lt; 2",
		`<img src="https://img.example/shot.png" alt="shot.png">`,
		`<span class="card-link">the post</span>`,
		`<span class="spoiler-card" data-spoiler="my &#34;title&#34;"><span class="spoiler-card-summary">my &#34;title&#34;</span></span>`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	for _, bad := range []string{"<a ", "secret", "<script>", "\x01"} {
		if strings.Contains(h, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, h)
		}
	}
}

func TestExcerptHTMLFlattensHeadings(t *testing.T) {
	got, err := ExcerptHTML("# Big heading\n\nText")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<h1") || !strings.Contains(got, "<p><strong>Big heading</strong></p>") {
		t.Errorf("got %q", got)
	}
}
