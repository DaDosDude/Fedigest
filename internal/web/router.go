package web

import (
	"net/http"
	"strconv"
	"strings"
)

// Routes wires up public, account, and admin routes:
//   - public: frontpage, category/author/post pages, login
//   - logged-in (any role): account settings, pick create/edit (ownership
//     checked inside the handler for edits)
//   - admin-only: categories, sources, site settings, sidebar, users
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.HandleFrontpage)
	mux.HandleFunc("GET /search", s.HandleSearch)
	mux.HandleFunc("GET /feed.xml", s.HandleFeed)
	mux.HandleFunc("GET /c/{slug}/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		s.HandleCategoryFeed(w, r, r.PathValue("slug"))
	})
	mux.HandleFunc("GET /login", s.HandleLoginForm)
	mux.HandleFunc("POST /login", s.HandleLoginSubmit)
	mux.HandleFunc("POST /logout", s.HandleLogout)
	mux.HandleFunc("GET /forgot-password", s.HandleForgotPasswordForm)
	mux.HandleFunc("POST /forgot-password", s.HandleForgotPasswordSubmit)
	mux.HandleFunc("GET /reset-password", s.HandleResetPasswordForm)
	mux.HandleFunc("POST /reset-password", s.HandleResetPasswordSubmit)
	mux.HandleFunc("GET /categories", s.HandleCategoriesIndex)
	mux.HandleFunc("GET /privacy", s.HandlePrivacy)
	mux.HandleFunc("GET /c/{slug}", func(w http.ResponseWriter, r *http.Request) {
		s.HandleCategoryPage(w, r, r.PathValue("slug"))
	})
	mux.HandleFunc("GET /author/{username}", func(w http.ResponseWriter, r *http.Request) {
		s.HandleAuthorPage(w, r, r.PathValue("username"))
	})
	mux.HandleFunc("GET /p/{idSlug}", func(w http.ResponseWriter, r *http.Request) {
		id := leadingID(r.PathValue("idSlug"))
		if id == 0 {
			s.notFound(w, r)
			return
		}
		s.HandlePostPage(w, r, id)
	})
	mux.HandleFunc("GET /resolve", s.HandleResolve)
	mux.HandleFunc("GET /robots.txt", s.HandleRobots)
	mux.HandleFunc("GET /sitemap.xml", s.HandleSitemap)

	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", uploadsHandler(s.Cfg.UploadsDir)))
	mux.Handle("GET /static/", http.StripPrefix("/static/", StaticHandler()))
	// Resolved per request since the active theme can change at runtime.
	mux.Handle("GET /theme/", http.StripPrefix("/theme/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serveThemeFile(w, r, s.currentThemeName(), r.URL.Path)
	})))
	mux.HandleFunc("GET /themes/{name}/{file...}", func(w http.ResponseWriter, r *http.Request) {
		s.serveThemeFile(w, r, r.PathValue("name"), r.PathValue("file"))
	})
	mux.HandleFunc("GET /theme-choice", s.HandleThemeChoice)

	mux.Handle("GET /account", s.Auth.RequireUser(http.HandlerFunc(s.HandleAccountPage)))
	mux.Handle("POST /account/avatar", s.Auth.RequireUser(http.HandlerFunc(s.HandleAccountAvatar)))
	mux.Handle("POST /account/password", s.Auth.RequireUser(http.HandlerFunc(s.HandleAccountChangePassword)))

	mux.Handle("GET /admin/picks", s.Auth.RequireUser(http.HandlerFunc(s.HandleAdminPicksList)))
	mux.Handle("GET /admin/picks/new", s.Auth.RequireUser(http.HandlerFunc(s.HandleAdminPickNewForm)))
	mux.Handle("POST /admin/picks/refresh-metadata", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminRefreshAllMetadata)))
	mux.Handle("GET /admin/picks/fetch-metadata", s.Auth.RequireUser(http.HandlerFunc(s.HandleAdminFetchMetadata)))
	mux.Handle("POST /admin/picks/new", s.Auth.RequireUser(http.HandlerFunc(s.HandleAdminPickCreate)))
	mux.Handle("GET /admin/picks/{id}/edit", s.Auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminPickEditForm(w, r, id)
	})))
	mux.Handle("POST /admin/picks/{id}/edit", s.Auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminPickUpdate(w, r, id)
	})))
	mux.Handle("POST /admin/picks/{id}/pin", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminPickPin(w, r, id)
	})))
	mux.Handle("POST /admin/picks/{id}/delete", s.Auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminPickDelete(w, r, id)
	})))

	mux.Handle("GET /admin/categories", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminCategoriesList)))
	mux.Handle("POST /admin/categories/reorder", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminCategoriesReorder)))
	mux.Handle("GET /admin/categories/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminCategoryNewForm)))
	mux.Handle("POST /admin/categories/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminCategoryCreate)))
	mux.Handle("GET /admin/categories/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminCategoryEditForm(w, r, id)
	})))
	mux.Handle("POST /admin/categories/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminCategoryUpdate(w, r, id)
	})))
	mux.Handle("POST /admin/categories/{id}/delete", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminCategoryDelete(w, r, id)
	})))

	mux.Handle("GET /admin/users", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminUsersList)))
	mux.Handle("GET /admin/users/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminUserNewForm)))
	mux.Handle("POST /admin/users/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminUserCreate)))
	mux.Handle("POST /admin/users/{id}/reset-password", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminUserResetPassword(w, r, id)
	})))
	mux.Handle("POST /admin/users/{id}/delete", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminUserDelete(w, r, id)
	})))
	mux.Handle("GET /admin/users/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminUserEditForm(w, r, id)
	})))
	mux.Handle("POST /admin/users/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminUserUpdate(w, r, id)
	})))

	mux.Handle("GET /admin/sources", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSourcesList)))
	mux.Handle("GET /admin/sources/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSourceNewForm)))
	mux.Handle("POST /admin/sources/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSourceCreate)))
	mux.Handle("POST /admin/sources/{id}/delete", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminSourceDelete(w, r, id)
	})))
	mux.Handle("GET /admin/sources/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminSourceEditForm(w, r, id)
	})))
	mux.Handle("POST /admin/sources/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminSourceUpdate(w, r, id)
	})))

	mux.Handle("GET /admin/themes", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminThemes)))
	mux.Handle("POST /admin/themes/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminThemeCreate)))
	mux.Handle("GET /admin/themes/{name}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.HandleAdminThemeEditForm(w, r, r.PathValue("name"))
	})))
	mux.Handle("POST /admin/themes/{name}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.HandleAdminThemeUpdate(w, r, r.PathValue("name"))
	})))
	mux.Handle("POST /admin/themes/{name}/delete", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.HandleAdminThemeDelete(w, r, r.PathValue("name"))
	})))
	mux.Handle("GET /admin/settings", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSettings)))
	mux.Handle("POST /admin/settings", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSettingsUpdate)))

	mux.Handle("GET /admin/sidebar", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSidebarList)))
	mux.Handle("GET /admin/sidebar/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSidebarNewForm)))
	mux.Handle("POST /admin/sidebar/new", s.Auth.RequireAdmin(http.HandlerFunc(s.HandleAdminSidebarCreate)))
	mux.Handle("GET /admin/sidebar/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminSidebarEditForm(w, r, id)
	})))
	mux.Handle("POST /admin/sidebar/{id}/edit", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminSidebarUpdate(w, r, id)
	})))
	mux.Handle("POST /admin/sidebar/{id}/delete", s.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.HandleAdminSidebarDelete(w, r, id)
	})))

	mux.HandleFunc("/", s.notFound)

	return securityHeaders(crossOriginProtect(s.Auth.Middleware(mux)))
}

// leadingID parses the numeric ID from a "142-title-of-post" path segment;
// the slug is ignored.
func leadingID(idSlug string) int64 {
	cut := strings.IndexByte(idSlug, '-')
	numPart := idSlug
	if cut != -1 {
		numPart = idSlug[:cut]
	}
	id, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0
	}
	return id
}
