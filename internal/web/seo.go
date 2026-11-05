package web

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fedigest/internal/models"
)

// seoHead is the data for the shared "head" template. JSONLD is a
// pre-rendered <script> block, empty except on post pages.
type seoHead struct {
	SiteName    string // shown after the page title, and as og:site_name
	Title       string
	Description string
	Canonical   string
	OGType      string // "website" or "article"
	OGImage     string
	NoIndex     bool
	JSONLD      template.HTML
	// FeedURL/FeedTitle: the RSS feed this page advertises; empty means
	// the main /feed.xml.
	FeedURL   string
	FeedTitle string
	// ThemeOptions/ThemeBack drive the footer theme picker (empty when
	// there's nothing to pick).
	ThemeOptions []themeOption
	ThemeBack    string
	// ThemeSheets are the theme stylesheet links (two for a light/dark pair).
	ThemeSheets []themeSheet
	// ColorScheme tells the browser which scheme the page is in, for its own
	// form controls and scrollbars. Never "only ...": a visitor's browser or
	// extension may darken pages, and that's their choice to keep.
	ColorScheme string
	// ThemeHasDark shows the "pure black in dark mode" checkbox; ThemeBlack
	// is the visitor's choice.
	ThemeHasDark bool
	ThemeBlack   bool
}

// seoHead builds the head data for an ordinary (non-article) public page.
func (s *Server) newSEOHead(r *http.Request, title, description, canonicalPath string) seoHead {
	h := seoHead{
		SiteName:    s.siteName(),
		Title:       title,
		Description: metaDescription(description),
		Canonical:   s.baseURL(r) + canonicalPath,
		OGType:      "website",
	}
	s.applyTheme(r, &h)
	return h
}

// postSEOHead builds the head data for a post page, including a JSON-LD
// Article block.
func (s *Server) postSEOHead(r *http.Request, p *models.Pick, curatorName string) seoHead {
	canonical := s.baseURL(r) + pickPath(p)
	image := s.absoluteImageURL(r, p.DisplayImage("/uploads"))
	// Descriptions must be plain text; the excerpt is markdown.
	description := p.DisplayExcerpt()
	if plain, err := models.PlainTextFromMarkdown(description); err == nil {
		description = plain
	}
	description = metaDescription(description)

	head := seoHead{
		SiteName:    s.siteName(),
		Title:       p.Title,
		Description: description,
		Canonical:   canonical,
		OGType:      "article",
		OGImage:     image,
	}
	s.applyTheme(r, &head)

	ld := map[string]any{
		"@context":         "https://schema.org",
		"@type":            "Article",
		"headline":         p.Title,
		"description":      description,
		"mainEntityOfPage": map[string]any{"@type": "WebPage", "@id": canonical},
	}
	if curatorName != "" {
		ld["author"] = map[string]any{"@type": "Person", "name": curatorName}
	}
	if image != "" {
		ld["image"] = image
	}
	if p.PublishedAt.Valid {
		if t, err := time.Parse(time.RFC3339, p.PublishedAt.String); err == nil {
			ld["datePublished"] = t.Format(time.RFC3339)
		}
	}

	if raw, err := json.Marshal(ld); err == nil {
		head.JSONLD = template.HTML(`<script type="application/ld+json">` + string(raw) + `</script>`)
	}

	return head
}

// metaDescriptionMax is about what search results and link previews show.
const metaDescriptionMax = 160

// metaDescription shortens a plain-text description for meta tags, cutting
// at a word boundary and adding an ellipsis.
func metaDescription(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= metaDescriptionMax {
		return text
	}
	runes := []rune(text)[:metaDescriptionMax]
	cut := string(runes)
	if i := strings.LastIndex(cut, " "); i > metaDescriptionMax/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-–—") + "…"
}

// pagedPath adds ?page=N to a list page's path beyond page 1, for its
// canonical URL.
func pagedPath(path string, page int) string {
	if page <= 1 {
		return path
	}
	return path + "?page=" + strconv.Itoa(page)
}

// pickPath returns a pick's canonical path, e.g. "/p/142-some-title".
func pickPath(p *models.Pick) string {
	return "/p/" + strconv.FormatInt(p.ID, 10) + "-" + p.Slug
}

// absoluteImageURL makes a site-relative upload URL absolute; hotlinked
// images already are.
func (s *Server) absoluteImageURL(r *http.Request, image string) string {
	if image == "" {
		return ""
	}
	if len(image) > 0 && image[0] == '/' {
		return s.baseURL(r) + image
	}
	return image
}

// HandleRobots serves a robots.txt pointing at the sitemap.
func (s *Server) HandleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /admin/\nDisallow: /account\nSitemap: " + s.baseURL(r) + "/sitemap.xml\n"))
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// HandleSitemap lists the frontpage, the category pages, the author pages
// of everyone with published picks, and every published pick.
func (s *Server) HandleSitemap(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	set.URLs = append(set.URLs, sitemapURL{Loc: base + "/"})

	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, c := range cats {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + "/c/" + url.PathEscape(c.Slug)})
	}

	picks, err := s.Picks.ListPublishedRefs()
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasPicks := map[int64]bool{}
	for _, p := range picks {
		hasPicks[p.CuratorID] = true
	}
	authors, err := s.Users.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, u := range authors {
		if hasPicks[u.ID] {
			set.URLs = append(set.URLs, sitemapURL{Loc: base + "/author/" + url.PathEscape(u.Username)})
		}
	}

	for _, p := range picks {
		u := sitemapURL{Loc: base + "/p/" + strconv.FormatInt(p.ID, 10) + "-" + url.PathEscape(p.Slug)}
		if t, err := time.Parse("2006-01-02 15:04:05", p.UpdatedAt); err == nil {
			u.LastMod = t.Format("2006-01-02")
		}
		set.URLs = append(set.URLs, u)
	}

	writeXML(w, "application/xml; charset=utf-8", set)
}

// writeXML encodes v in full before sending it, so an encoding error is a
// clean 500.
func writeXML(w http.ResponseWriter, contentType string, v any) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("encoding XML: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(buf.Bytes())
}

// notFound renders the styled 404 page.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if s.currentTemplates().Lookup("not_found.html") == nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	head := s.newSEOHead(r, "Page not found", "", r.URL.Path)
	head.NoIndex = true
	s.renderStatus(w, http.StatusNotFound, "not_found.html", map[string]any{
		"SiteName": s.siteName(),
		"Head":     head,
	})
}
