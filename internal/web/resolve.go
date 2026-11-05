package web

import (
	"net/http"

	"fedigest/internal/auth"
	"fedigest/internal/models"
)

// resolvePageData backs resolve.html.
type resolvePageData struct {
	SiteName      string
	Categories    []*models.Category
	SidebarBlocks []*models.SidebarBlock
	TargetURL     string // the original fedi post URL to resolve
	Head          seoHead
	CurrentUser   *models.User
}

// HandleResolve serves the "interact from your instance" page. Its script
// runs in the visitor's browser (so their instance never reaches the
// server), asks their instance's resolve_object API for its copy of the
// post and redirects there, falling back to that instance's search page.
func (s *Server) HandleResolve(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		s.notFound(w, r)
		return
	}

	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}

	head := s.newSEOHead(r, "Interact from your instance", "", "/resolve")
	head.NoIndex = true

	s.render(w, "resolve.html", resolvePageData{
		SiteName:      s.siteName(),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		TargetURL:     target,
		Head:          head,
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}
