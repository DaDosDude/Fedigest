package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"fedigest/internal/models"
)

// RSS 2.0 output.

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	GUID        rssGUID `xml:"guid"`
	Description string  `xml:"description"`
	PubDate     string  `xml:"pubDate,omitempty"`
	Author      string  `xml:"author,omitempty"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// HandleFeed serves the site-wide RSS feed.
func (s *Server) HandleFeed(w http.ResponseWriter, r *http.Request) {
	picks, err := s.Picks.ListFrontpage(1)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.writeFeed(w, r, s.siteName(), "", picks)
}

// HandleCategoryFeed serves the RSS feed for one category.
func (s *Server) HandleCategoryFeed(w http.ResponseWriter, r *http.Request, slug string) {
	cat, err := s.Categories.GetBySlug(slug)
	if err != nil {
		s.notFound(w, r)
		return
	}
	picks, err := s.Picks.ListPublished(&cat.ID, nil, 1)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.writeFeed(w, r, s.siteName()+" — "+cat.Name, "/c/"+cat.Slug, picks)
}

func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, title, pathSuffix string, picks []*models.Pick) {
	base := s.baseURL(r)

	items := make([]rssItem, 0, len(picks))
	for _, p := range picks {
		link := fmt.Sprintf("%s/p/%d-%s", base, p.ID, p.Slug)
		// The excerpt is markdown; feeds get plain text.
		description := p.DisplayExcerpt()
		if plain, err := models.PlainTextFromMarkdown(description); err == nil {
			description = plain
		}
		item := rssItem{
			Title:       p.Title,
			Link:        link,
			GUID:        rssGUID{IsPermaLink: true, Value: link},
			Description: description,
		}
		if p.PublishedAt.Valid {
			if t, err := time.Parse(time.RFC3339, p.PublishedAt.String); err == nil {
				item.PubDate = t.Format(time.RFC1123Z)
			}
		}
		items = append(items, item)
	}

	feed := rssFeed{
		Version: "2.0",
		Channel: rssChannel{
			Title:       title,
			Link:        base + pathSuffix,
			Description: title,
			Items:       items,
		},
	}

	writeXML(w, "application/rss+xml; charset=utf-8", feed)
}

// baseURL returns the absolute site URL from SITE_DOMAIN (always https),
// falling back to the request's Host and scheme.
func (s *Server) baseURL(r *http.Request) string {
	if s.Cfg.SiteDomain != "" {
		return "https://" + s.Cfg.SiteDomain
	}
	scheme := "http" // e.g. local testing
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
