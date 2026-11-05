// Package web holds Fedigest's HTTP handlers and template rendering.
package web

import (
	"bytes"
	"database/sql"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/auth"
	"fedigest/internal/comments"
	"fedigest/internal/config"
	"fedigest/internal/models"
)

type Server struct {
	Cfg            *config.Config
	DB             *sql.DB
	Auth           *auth.Service
	Users          *models.UserStore
	Categories     *models.CategoryStore
	Picks          *models.PickStore
	Sources        *models.SourceStore
	SiteSettings   *models.SiteSettingsStore
	Sidebar        *models.SidebarBlockStore
	PasswordResets *models.PasswordResetStore
	Templates      *template.Template            // base theme; fallback if the selected theme is missing
	Themes         map[string]*template.Template // theme name -> parsed template set
	ThemeDirs      map[string]string             // theme name -> directory
	CustomThemes   *models.CustomThemeStore      // themes made in the admin UI (stylesheet only)
	ThemeModes     *models.ThemeModeStore        // light/dark/fixed and partner per theme
	Adapters       *adapters.Registry
	Comments       *comments.Service

	refresh  metadataRefresh
	attempts *attemptLimiter // nil = the shared default
}

// siteName returns the site name from the DB, falling back to the config.
func (s *Server) siteName() string {
	if s.SiteSettings != nil {
		if ss, err := s.SiteSettings.Get(); err == nil && ss.SiteName != "" {
			return ss.SiteName
		}
	}
	return s.Cfg.SiteName
}

// currentThemeName returns the active theme from the DB, falling back to
// the config; only ever a loaded theme, so a stale DB value can't break
// the site.
func (s *Server) currentThemeName() string {
	if s.SiteSettings != nil {
		if ss, err := s.SiteSettings.Get(); err == nil && ss.Theme != "" {
			if s.themeExists(ss.Theme) {
				return ss.Theme
			}
		}
	}
	return baseTheme
}

// currentTemplates returns the parsed template set for the active theme.
func (s *Server) currentTemplates() *template.Template {
	if t, ok := s.Themes[s.currentThemeName()]; ok {
		return t
	}
	return s.Templates
}

// pickCard pairs a Pick with its curator and category for templates.
type pickCard struct {
	*models.Pick
	Pinned   bool               // shown in the pinned section, with its label
	CanEdit  bool               // the logged-in user may edit it: shows an Edit link
	Curator  *models.User       // nil if the lookup failed
	Category *models.Category   // nil if the lookup failed or hidden
	Extra    []*models.Category // extra categories, shown after Category
}

// attachCurators resolves curators and categories once per distinct ID,
// and marks the cards the logged-in user may edit. hideCategory leaves
// Category nil, for the category page itself.
func (s *Server) attachCurators(r *http.Request, picks []*models.Pick, hideCategory bool) []pickCard {
	viewer := auth.UserFromContext(r.Context())
	curators := make(map[int64]*models.User)
	cats := make(map[int64]*models.Category)
	var extras map[int64][]int64
	if !hideCategory {
		ids := make([]int64, len(picks))
		for i, p := range picks {
			ids[i] = p.ID
		}
		extras, _ = s.Picks.ExtraCategories(ids...)
		if all, err := s.Categories.List(); err == nil {
			for _, c := range all {
				cats[c.ID] = c
			}
		}
	}
	out := make([]pickCard, 0, len(picks))
	for _, p := range picks {
		curator, cached := curators[p.CuratorID]
		if !cached {
			curator, _ = s.Users.GetByID(p.CuratorID)
			curators[p.CuratorID] = curator
		}
		var cat *models.Category
		if !hideCategory {
			var ok bool
			cat, ok = cats[p.CategoryID]
			if !ok {
				cat, _ = s.Categories.GetByID(p.CategoryID)
				cats[p.CategoryID] = cat
			}
		}
		out = append(out, pickCard{Pick: p, Curator: curator, Category: cat, Extra: categoriesByID(cats, extras[p.ID]),
			CanEdit: viewer != nil && p.CanEdit(viewer)})
	}
	return out
}

// pinnedCards returns the pinned picks shown above the list on page 1,
// with categories, since pins can come from any category.
func (s *Server) pinnedCards(r *http.Request, page int) ([]pickCard, error) {
	if page != 1 {
		return nil, nil
	}
	pinned, err := s.Picks.ListPinned()
	if err != nil {
		return nil, err
	}
	cards := s.attachCurators(r, pinned, false)
	for i := range cards {
		cards[i].Pinned = true
	}
	return cards, nil
}

// frontpageData is the data for list-style pages.
type frontpageData struct {
	SiteName      string
	Pinned        []pickCard
	Picks         []pickCard
	Categories    []*models.Category
	SidebarBlocks []*models.SidebarBlock
	Page          int
	HasNext       bool   // this page was full, so another may exist
	BasePath      string // route path that pagination links append ?page=N to
	PageTitle     string
	Query         string
	Content       template.HTML // page text, e.g. the privacy page
	Head          seoHead
	// CurrentUser is nil for anonymous visitors.
	CurrentUser *models.User
}

// sidebarBlocks returns the enabled sidebar blocks, or none on error.
func (s *Server) sidebarBlocks() []*models.SidebarBlock {
	if s.Sidebar == nil {
		return nil
	}
	blocks, err := s.Sidebar.ListEnabled()
	if err != nil {
		log.Printf("loading sidebar blocks: %v", err)
		return nil
	}
	return blocks
}

func (s *Server) HandleFrontpage(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r)
	picks, err := s.Picks.ListFrontpageUnpinned(page)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pinned, err := s.pinnedCards(r, page)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if page > 1 && len(picks) == 0 {
		s.notFound(w, r)
		return
	}
	s.render(w, "frontpage.html", frontpageData{
		SiteName:      s.siteName(),
		Pinned:        pinned,
		Picks:         s.attachCurators(r, picks, false),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		Page:          page,
		HasNext:       len(picks) == models.PicksPerPage,
		BasePath:      "/",
		PageTitle:     "Frontpage",
		Head:          s.newSEOHead(r, s.siteName(), "Curated posts from the threadiverse.", pagedPath("/", page)),
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}

// HandleCategoriesIndex lists every category.
func (s *Server) HandleCategoriesIndex(w http.ResponseWriter, r *http.Request) {
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "categories.html", frontpageData{
		SiteName:      s.siteName(),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		PageTitle:     "Categories",
		Head:          s.newSEOHead(r, "Categories", "All categories on "+s.siteName()+".", "/categories"),
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}

func (s *Server) HandleCategoryPage(w http.ResponseWriter, r *http.Request, slug string) {
	cat, err := s.Categories.GetBySlug(slug)
	if err != nil {
		s.notFound(w, r)
		return
	}
	page := parsePage(r)
	picks, err := s.Picks.ListCategoryUnpinned(cat.ID, page)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pinned, err := s.pinnedCards(r, page)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if page > 1 && len(picks) == 0 {
		s.notFound(w, r)
		return
	}
	head := s.categoryHead(r, cat)
	head.Canonical = s.baseURL(r) + pagedPath("/c/"+cat.Slug, page)
	s.render(w, "frontpage.html", frontpageData{
		SiteName:      s.siteName(),
		Pinned:        pinned,
		Picks:         s.attachCurators(r, picks, true),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		Page:          page,
		HasNext:       len(picks) == models.PicksPerPage,
		BasePath:      "/c/" + cat.Slug,
		PageTitle:     cat.Name,
		Head:          head,
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}

func (s *Server) HandleAuthorPage(w http.ResponseWriter, r *http.Request, username string) {
	author, err := s.Users.GetByUsername(username)
	if err != nil {
		s.notFound(w, r)
		return
	}
	page := parsePage(r)
	picks, err := s.Picks.ListPublished(nil, &author.ID, page)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if page > 1 && len(picks) == 0 {
		s.notFound(w, r)
		return
	}
	authorPath := "/author/" + url.PathEscape(author.Username)
	s.render(w, "frontpage.html", frontpageData{
		SiteName:      s.siteName(),
		Picks:         s.attachCurators(r, picks, false),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		Page:          page,
		HasNext:       len(picks) == models.PicksPerPage,
		BasePath:      authorPath,
		PageTitle:     "Posts by " + author.DisplayNameOrUsername(),
		Head:          s.newSEOHead(r, "Posts by "+author.DisplayNameOrUsername(), "Posts curated by "+author.DisplayNameOrUsername()+".", pagedPath(authorPath, page)),
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}

type postPageData struct {
	*models.Pick
	CanEdit       bool // the logged-in user may edit this pick
	SiteName      string
	Categories    []*models.Category
	SidebarBlocks []*models.SidebarBlock
	Comments      commentsViewData
	ResolverLink  string
	Head          seoHead
	CurrentUser   *models.User
	Curator       *models.User
	Extra         []*models.Category
	Category      *models.Category
}

type commentsViewData struct {
	Enabled     bool
	Total       int
	Stale       bool
	Unavailable bool
	UpdatedAt   time.Time
	Comments    []adapters.Comment
}

func (s *Server) HandlePostPage(w http.ResponseWriter, r *http.Request, id int64) {
	pick, err := s.Picks.GetByID(id)
	if err != nil {
		s.notFound(w, r)
		return
	}

	// Drafts are visible only to their author or an admin; others get a
	// 404 so a draft's existence isn't leaked.
	if pick.Status == models.PickStatusDraft {
		u := auth.UserFromContext(r.Context())
		if u == nil || !pick.CanEdit(u) {
			s.notFound(w, r)
			return
		}
	}

	s.countView(r, pick)

	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}

	curator, _ := s.Users.GetByID(pick.CuratorID)        // nil on error
	category, _ := s.Categories.GetByID(pick.CategoryID) // nil on error
	curatorName := ""
	if curator != nil {
		curatorName = curator.DisplayNameOrUsername()
	}

	head := s.postSEOHead(r, pick, curatorName)
	if pick.Status == models.PickStatusDraft {
		head.NoIndex = true
	}

	data := postPageData{
		Pick:          pick,
		SiteName:      s.siteName(),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		Head:          head,
		CurrentUser:   auth.UserFromContext(r.Context()),
		Curator:       curator,
		Category:      category,
	}
	if u := data.CurrentUser; u != nil {
		data.CanEdit = pick.CanEdit(u)
	}
	if extras, err := s.Picks.ExtraCategories(pick.ID); err == nil && len(extras[pick.ID]) > 0 {
		all := map[int64]*models.Category{}
		for _, c := range cats {
			all[c.ID] = c
		}
		data.Extra = categoriesByID(all, extras[pick.ID])
	}

	if pick.SourceType == models.SourceTypeFedi {
		var result comments.Result
		if r.Method == http.MethodHead || isBot(r) {
			// Crawlers and link previews get the cache: no remote fetch.
			result = s.Comments.CachedComments(pick)
		} else {
			result = s.Comments.GetComments(r.Context(), pick)
		}
		data.Comments = commentsViewData{
			Enabled:     true,
			Total:       result.Total,
			Stale:       result.Stale,
			Unavailable: result.Unavailable,
			UpdatedAt:   result.UpdatedAt,
			Comments:    result.Comments,
		}
		data.ResolverLink = resolverLink(pick)
	}

	s.render(w, "post.html", data)
}

// resolverLink builds the "interact from your instance" link (/resolve).
// Mbin picks only get it once their ActivityPub ID is known.
func resolverLink(pick *models.Pick) string {
	if !pick.OriginalURL.Valid {
		return ""
	}
	platform := ""
	if pick.SourcePlatform.Valid {
		platform = pick.SourcePlatform.String
	}
	hasApID := pick.OriginalApID.Valid && pick.OriginalApID.String != ""
	switch platform {
	case string(models.PlatformLemmy), string(models.PlatformPieFed):
	case string(models.PlatformMbin):
		// Lemmy/PieFed can't resolve an Mbin page URL.
		if !hasApID {
			return ""
		}
	default:
		return ""
	}
	// Instances resolve posts anonymously by ActivityPub ID, not by
	// another instance's page link.
	target := pick.OriginalURL.String
	if pick.OriginalApID.Valid && pick.OriginalApID.String != "" {
		target = pick.OriginalApID.String
	}
	return "/resolve?url=" + url.QueryEscape(target)
}

// HandleSearch runs a full-text search across published picks.
func (s *Server) HandleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	page := parsePage(r)

	var picks []*models.Pick
	if query != "" {
		var err error
		picks, err = s.Picks.Search(query, page)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}

	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}

	head := s.newSEOHead(r, "Search", "", "/search")
	head.NoIndex = true

	s.render(w, "search.html", frontpageData{
		SiteName:      s.siteName(),
		Picks:         s.attachCurators(r, picks, false),
		Categories:    cats,
		SidebarBlocks: s.sidebarBlocks(),
		Page:          page,
		HasNext:       len(picks) == models.PicksPerPage,
		BasePath:      "/search",
		PageTitle:     "Search",
		Query:         query,
		Head:          head,
		CurrentUser:   auth.UserFromContext(r.Context()),
	})
}

func (s *Server) loginHead(r *http.Request) seoHead {
	head := s.newSEOHead(r, "Log in", "", "/login")
	head.NoIndex = true
	return head
}

func (s *Server) HandleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", map[string]any{"Head": s.loginHead(r), "EmailEnabled": s.Cfg.EmailEnabled()})
}

func (s *Server) HandleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	rememberMe := r.FormValue("remember_me") == "on"

	lim := s.limiter()
	ipKey := "login-ip:" + clientIP(r)
	userKey := "login-user:" + strings.ToLower(strings.TrimSpace(username))
	loginErr := func(status int, msg string) {
		s.renderStatus(w, status, "login.html", map[string]any{
			"Error":        msg,
			"Head":         s.loginHead(r),
			"EmailEnabled": s.Cfg.EmailEnabled(),
		})
	}
	for _, k := range []string{ipKey, userKey} {
		if locked, left := lim.locked(k); locked {
			unit := "minutes"
			mins := int(left.Minutes()) + 1
			if mins == 1 {
				unit = "minute"
			}
			loginErr(http.StatusTooManyRequests, "Too many failed login attempts. Try again in "+strconv.Itoa(mins)+" "+unit+".")
			return
		}
	}

	if _, err := s.Auth.Login(w, username, password, rememberMe); err != nil {
		lim.fail(ipKey, loginIPLimit)
		lim.fail(userKey, loginUserLimit)
		loginErr(http.StatusOK, "Invalid username or password.")
		return
	}
	lim.reset(ipKey)
	lim.reset(userKey)
	http.Redirect(w, r, "/admin/picks", http.StatusFound)
}

func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(w, r)
	http.Redirect(w, r, "/", http.StatusFound)
}

// renderAdmin renders an admin-area page, adding IsAdmin so the nav can
// hide admin-only links.
func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	u := auth.UserFromContext(r.Context())
	data["IsAdmin"] = u != nil && u.Role == models.RoleAdmin
	s.render(w, name, data)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	s.renderStatus(w, http.StatusOK, name, data)
}

// renderStatus renders a template with the given status. The page is
// rendered in full before anything is sent, so a template error gives a
// clean 500 instead of half a page.
func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data any) {
	tmpl := s.currentTemplates()
	if tmpl.Lookup(name) == nil {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template error rendering %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// maxPage keeps page numbers (and so query offsets) sane.
const maxPage = 100000

func parsePage(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return min(p, maxPage)
}

// categoryHead is a category page's head data, advertising its own feed.
func (s *Server) categoryHead(r *http.Request, cat *models.Category) seoHead {
	head := s.newSEOHead(r, cat.Name, "Posts in "+cat.Name+" — curated from the threadiverse.", "/c/"+cat.Slug)
	head.FeedURL = "/c/" + cat.Slug + "/feed.xml"
	head.FeedTitle = s.siteName() + " — " + cat.Name
	return head
}
