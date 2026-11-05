package models

import (
	"bytes"
	"database/sql"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
)

type SidebarBlockStore struct {
	DB *sql.DB
}

var sidebarColumns = `id, title, content_md, content_html, sort_order, enabled, created_at, updated_at`

func scanSidebarBlock(row interface{ Scan(...any) error }) (*SidebarBlock, error) {
	var b SidebarBlock
	err := row.Scan(&b.ID, &b.Title, &b.ContentMD, &b.ContentHTML, &b.SortOrder, &b.Enabled, &b.CreatedAt, &b.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// List returns every block, enabled or not.
func (s *SidebarBlockStore) List() ([]*SidebarBlock, error) {
	rows, err := s.DB.Query(`SELECT ` + sidebarColumns + ` FROM sidebar_blocks ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*SidebarBlock
	for rows.Next() {
		b, err := scanSidebarBlock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListEnabled returns only the blocks toggled on, for public rendering.
func (s *SidebarBlockStore) ListEnabled() ([]*SidebarBlock, error) {
	rows, err := s.DB.Query(`SELECT ` + sidebarColumns + ` FROM sidebar_blocks WHERE enabled = 1 ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*SidebarBlock
	for rows.Next() {
		b, err := scanSidebarBlock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *SidebarBlockStore) GetByID(id int64) (*SidebarBlock, error) {
	row := s.DB.QueryRow(`SELECT `+sidebarColumns+` FROM sidebar_blocks WHERE id = ?`, id)
	return scanSidebarBlock(row)
}

// Create renders contentMD to HTML at save time and stores both.
func (s *SidebarBlockStore) Create(title, contentMD string, sortOrder int, enabled bool) (int64, error) {
	html, err := RenderMarkdown(contentMD)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`
		INSERT INTO sidebar_blocks (title, content_md, content_html, sort_order, enabled)
		VALUES (?, ?, ?, ?, ?)`,
		title, contentMD, html, sortOrder, enabled)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *SidebarBlockStore) Update(id int64, title, contentMD string, sortOrder int, enabled bool) error {
	html, err := RenderMarkdown(contentMD)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`
		UPDATE sidebar_blocks SET
			title = ?, content_md = ?, content_html = ?, sort_order = ?, enabled = ?, updated_at = datetime('now')
		WHERE id = ?`,
		title, contentMD, html, sortOrder, enabled, id)
	return err
}

func (s *SidebarBlockStore) Delete(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM sidebar_blocks WHERE id = ?`, id)
	return err
}

// RenderMarkdown converts markdown to HTML, including Lemmy/PieFed spoiler
// blocks. goldmark's default renderer escapes raw HTML.
func RenderMarkdown(src string) (string, error) {
	return renderWithSpoilers(src, spoilerHTML, nil)
}

func renderGoldmark(src string) (string, error) {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type spoilerMode int

const (
	spoilerHTML  spoilerMode = iota // collapsible <details> (post page)
	spoilerPlain                    // "[Spoiler: Title]" (feeds, meta)
	spoilerCard                     // placeholder for ExcerptHTML (cards)
)

// Placeholder delimiters for spoilerCard mode: control characters that
// can't occur in rendered text and survive tag-stripping and escaping.
const (
	spoilerMarkOpen  = '\x01'
	spoilerMarkClose = '\x02'
)

var (
	spoilerOpenPattern  = regexp.MustCompile(`^\s*:::\s*spoiler(?:\s+(.*?))?\s*$`)
	spoilerClosePattern = regexp.MustCompile(`^\s*:::\s*$`)
	fencePattern        = regexp.MustCompile("^\\s*(```|~~~)")
)

// renderWithSpoilers renders markdown, turning Lemmy/PieFed's spoiler
// syntax into a collapsible <details> block:
//
//	::: spoiler Title
//	hidden *markdown* content
//	:::
//
// goldmark doesn't support this and escapes raw HTML, so the source is
// split into plain chunks and spoiler blocks rendered separately. Spoilers
// can nest, fenced code is untouched, and an unclosed spoiler runs to the
// end, as in Lemmy.
//
// spoilerPlain renders "[Spoiler: Title]" so hidden content isn't revealed
// in feeds/meta. spoilerCard emits a placeholder for ExcerptHTML and
// appends the title to *titles.
func renderWithSpoilers(src string, mode spoilerMode, titles *[]string) (string, error) {
	lines := strings.Split(src, "\n")
	var out strings.Builder
	var chunk []string
	inFence := false

	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		h, err := renderGoldmark(strings.Join(chunk, "\n"))
		if err != nil {
			return err
		}
		out.WriteString(h)
		chunk = chunk[:0]
		return nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if fencePattern.MatchString(line) {
			inFence = !inFence
		}
		m := spoilerOpenPattern.FindStringSubmatch(line)
		if inFence || m == nil {
			chunk = append(chunk, line)
			continue
		}
		if err := flush(); err != nil {
			return "", err
		}
		title := strings.TrimSpace(m[1])
		if title == "" {
			title = "Spoiler"
		}
		// Collect the block body up to the matching close, allowing nesting.
		depth, j := 1, i+1
		var inner []string
		innerFence := false
		for ; j < len(lines); j++ {
			l := lines[j]
			if fencePattern.MatchString(l) {
				innerFence = !innerFence
			}
			if !innerFence {
				if spoilerOpenPattern.MatchString(l) {
					depth++
				} else if spoilerClosePattern.MatchString(l) {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			inner = append(inner, l)
		}
		i = j // skip past the closing ::: (or to the end if unclosed)

		switch mode {
		case spoilerPlain:
			out.WriteString("<p>[Spoiler: " + html.EscapeString(title) + "]</p>")
			continue
		case spoilerCard:
			out.WriteString(fmt.Sprintf("<p>%c%d%c</p>", spoilerMarkOpen, len(*titles), spoilerMarkClose))
			*titles = append(*titles, title)
			continue
		}
		body, err := renderWithSpoilers(strings.Join(inner, "\n"), spoilerHTML, nil)
		if err != nil {
			return "", err
		}
		out.WriteString(`<details class="spoiler"><summary>` + html.EscapeString(title) + "</summary>" + body + "</details>")
	}
	if err := flush(); err != nil {
		return "", err
	}
	return out.String(), nil
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// PlainTextFromMarkdown flattens markdown into plain text. It renders then
// strips tags, which copes with excerpts truncated mid-syntax.
func PlainTextFromMarkdown(src string) (string, error) {
	rendered, err := renderWithSpoilers(src, spoilerPlain, nil)
	if err != nil {
		return "", err
	}
	noTags := htmlTagPattern.ReplaceAllString(rendered, " ")
	return strings.Join(strings.Fields(html.UnescapeString(noTags)), " "), nil
}

// ExcerptHTML renders an excerpt for list cards. Links become
// <span class="card-link"> since the card is already a link (no nested
// <a>), and spoilers become <span class="spoiler-card"> placeholders that
// base.html's script links to "#spoiler=<title>" on the post page.
func ExcerptHTML(src string) (string, error) {
	var titles []string
	rendered, err := renderWithSpoilers(src, spoilerCard, &titles)
	if err != nil {
		return "", err
	}
	rendered = anchorOpenPattern.ReplaceAllString(rendered, `<span class="card-link">`)
	rendered = strings.ReplaceAll(rendered, "</a>", "</span>")
	// Headings in an excerpt would break the page's heading outline.
	rendered = headingOpenPattern.ReplaceAllString(rendered, "<p><strong>")
	rendered = headingClosePattern.ReplaceAllString(rendered, "</strong></p>")
	return spoilerCardPattern.ReplaceAllStringFunc(rendered, func(m string) string {
		sub := spoilerCardPattern.FindStringSubmatch(m)
		i, err := strconv.Atoi(sub[1])
		if err != nil || i >= len(titles) {
			return ""
		}
		t := html.EscapeString(titles[i])
		return `<span class="spoiler-card" data-spoiler="` + t + `"><span class="spoiler-card-summary">` + t + `</span></span>`
	}), nil
}

var (
	headingOpenPattern  = regexp.MustCompile(`<h[1-6](\s[^>]*)?>`)
	headingClosePattern = regexp.MustCompile(`</h[1-6]>`)
	anchorOpenPattern   = regexp.MustCompile(`<a\s[^>]*>`)
	spoilerCardPattern  = regexp.MustCompile("<p>\x01(\\d+)\x02</p>")
)
