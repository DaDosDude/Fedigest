package web

import (
	"html/template"
	"net/http"
	"strings"

	"fedigest/internal/auth"
	"fedigest/internal/models"
)

// privacyMD is the privacy page. It describes what Fedigest does and isn't
// editable, so it can't drift from the code; keep it accurate when
// changing cookies, storage, view counting or anything loaded from other
// servers. Only the contact line (Site settings) differs per site.
const privacyMD = `This site collects as little as it can. There are no visitor accounts, no analytics and no third-party scripts or trackers.

## Cookies and browser storage

- **fedigest_session** — set only when a curator logs in, to keep them logged in. Visitors never get it.
- **fedigest_theme** — set only if you pick a theme from the menu at the bottom of the page, to remember your choice for a year. Choosing "Site default" deletes it.
- **fedigest_black** — set only if you tick "Pure black (AMOLED)", to remember it for a year. Unticking it deletes it.
- **Your home instance** — if you use "Interact from your instance" on a post, the instance you type is kept in your browser's local storage so you don't have to type it again. It's never sent to this site.

These are all needed for something you asked for, so there's no cookie banner.

## View counts

To count how often a post is read, the site keeps a one-way hash of your IP address and browser name in memory for the rest of the day, so a reload isn't counted twice. Only the number of views is saved; the hash is never written to disk and is gone the next day or when the server restarts. Logged-in users and bots aren't counted.

## Logging in

Failed logins are counted per IP address and username, and password-reset requests per IP address, in memory for 15 minutes to stop password guessing. Nothing is saved to disk.

## Content from other servers

Posts here come from Lemmy, PieFed and Mbin servers in the fediverse. Their images are loaded straight from the servers they're hosted on, so your browser connects to those servers and they can see your IP address, like any website you visit. "Interact from your instance" asks the instance you enter, from your browser, where to find the post. Comments shown on a post are fetched by this site's server, not by your browser.

## Questions

`

const defaultPrivacyContact = "Contact the people who run this site."

func (s *Server) privacyMarkdown() string {
	contact := defaultPrivacyContact
	if ss, err := s.SiteSettings.Get(); err == nil && ss.PrivacyContact.Valid && strings.TrimSpace(ss.PrivacyContact.String) != "" {
		contact = ss.PrivacyContact.String
	}
	return privacyMD + contact
}

// HandlePrivacy shows the privacy page.
func (s *Server) HandlePrivacy(w http.ResponseWriter, r *http.Request) {
	html, err := models.RenderMarkdown(s.privacyMarkdown())
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, _ := s.Categories.List()
	s.render(w, "privacy.html", frontpageData{
		SiteName:      s.siteName(),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		PageTitle:     "Privacy",
		Content:       template.HTML(html),
		Head:          s.newSEOHead(r, "Privacy", "How "+s.siteName()+" handles your data.", "/privacy"),
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}
