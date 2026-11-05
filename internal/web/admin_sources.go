package web

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fedigest/internal/auth"
	"fedigest/internal/models"
)

// HandleAdminSourcesList shows every registered source.
func (s *Server) HandleAdminSourcesList(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.Sources.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "admin_sources.html", map[string]any{"Sources": srcs})
}

func (s *Server) HandleAdminSourceNewForm(w http.ResponseWriter, r *http.Request) {
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "admin_source_form.html", map[string]any{
		"IsNew":      true,
		"Categories": cats,
	})
}

// sourceFromForm reads the source form into a Source and validates it.
// The Source is returned even when there's a problem (the user-facing
// message), so the form can be shown again as typed.
func sourceFromForm(r *http.Request, cats []*models.Category) (*models.Source, string) {
	nullable := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	src := &models.Source{
		Platform:    models.Platform(r.FormValue("platform")),
		InstanceURL: instanceBaseURL(r.FormValue("instance_url")),
		WatchType:   models.WatchType(r.FormValue("watch_type")),
		Mode:        models.SourceMode(r.FormValue("mode")),
		Name:        nullable(strings.TrimSpace(r.FormValue("name"))),
	}
	if src.WatchType != models.WatchTypeCommunity && src.WatchType != models.WatchTypePerson && src.WatchType != models.WatchTypeInstance {
		src.WatchType = models.WatchTypeCommunity
	}
	if src.WatchType != models.WatchTypeInstance {
		src.WatchTarget = nullable(strings.TrimSpace(r.FormValue("watch_target")))
	}
	if src.WatchType != models.WatchTypeCommunity {
		src.ExcludeCommunities = nullable(models.NormalizeCommunityList(r.FormValue("exclude_communities")))
	}
	src.PollIntervalSec = 900
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("poll_interval_sec"))); err == nil {
		src.PollIntervalSec = min(max(n, minPollInterval), maxPollInterval)
	}
	if id, err := strconv.ParseInt(r.FormValue("default_category_id"), 10, 64); err == nil && len(validCategoryIDs([]int64{id}, cats)) == 1 {
		src.DefaultCategoryID = sql.NullInt64{Int64: id, Valid: true}
	}

	switch {
	case src.Platform != models.PlatformLemmy && src.Platform != models.PlatformPieFed && src.Platform != models.PlatformMbin:
		return src, "Choose a platform."
	case src.InstanceURL == "":
		return src, "Instance URL is required."
	case src.WatchType != models.WatchTypeInstance && !src.WatchTarget.Valid:
		return src, "Enter the community or account to watch."
	case src.Mode != models.SourceModeManual && src.Mode != models.SourceModeAutoReview && src.Mode != models.SourceModeAutoPublish:
		return src, "Choose a mode."
	// A polled source needs a category for its picks; the column is
	// nullable, so it's enforced here.
	case src.Mode != models.SourceModeManual && !src.DefaultCategoryID.Valid:
		return src, "A default category is required before a source can auto-review or auto-publish."
	}
	return src, ""
}

// Poll interval bounds in seconds: the poller checks once a minute, and
// polling less than daily isn't useful.
const (
	minPollInterval = 60
	maxPollInterval = 86400
)

func (s *Server) HandleAdminSourceCreate(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	if err := r.ParseMultipartForm(1 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	src, errMsg := sourceFromForm(r, cats)
	if errMsg != "" {
		s.render(w, "admin_source_form.html", map[string]any{
			"IsNew":      true,
			"Error":      errMsg,
			"Source":     src,
			"Categories": cats,
			"Lookback":   r.FormValue("lookback_amount"),
			"LookUnit":   r.FormValue("lookback_unit"),
			"ExtraSet":   idSet(validCategoryIDs(formExtraCategories(r), cats)),
		})
		return
	}
	src.CreatedByUserID = sql.NullInt64{Int64: u.ID, Valid: true}

	id, err := s.Sources.Create(src)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if d := formLookback(r); d > 0 {
		if err := s.Sources.SetIngestSince(id, time.Now().Add(-d)); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if s.SiteSettings.MultiCategory() {
		if err := s.Sources.SetExtraCategories(id, validCategoryIDs(formExtraCategories(r), cats)); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/admin/sources", http.StatusFound)
}

// HandleAdminSourceEditForm shows the source form pre-filled from the row.
func (s *Server) HandleAdminSourceEditForm(w http.ResponseWriter, r *http.Request, id int64) {
	src, err := s.Sources.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	extras, _ := s.Sources.ExtraCategories(src.ID)
	s.render(w, "admin_source_form.html", map[string]any{
		"IsNew":      false,
		"Source":     src,
		"Categories": cats,
		"ExtraSet":   idSet(extras),
	})
}

func (s *Server) HandleAdminSourceUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	existing, err := s.Sources.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	src, errMsg := sourceFromForm(r, cats)
	src.ID = id
	src.IngestSince = existing.IngestSince
	src.AuthToken = existing.AuthToken // not used or offered: sources only read public APIs
	if errMsg != "" {
		s.render(w, "admin_source_form.html", map[string]any{
			"IsNew":      false,
			"Source":     src,
			"Error":      errMsg,
			"Categories": cats,
			"Lookback":   r.FormValue("lookback_amount"),
			"LookUnit":   r.FormValue("lookback_unit"),
			"ExtraSet":   idSet(validCategoryIDs(formExtraCategories(r), cats)),
		})
		return
	}

	if err := s.Sources.Update(src); err != nil {
		s.serverError(w, err)
		return
	}
	if d := formLookback(r); d > 0 {
		if err := s.Sources.SetIngestSince(id, time.Now().Add(-d)); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if s.SiteSettings.MultiCategory() {
		if err := s.Sources.SetExtraCategories(id, validCategoryIDs(formExtraCategories(r), cats)); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/admin/sources", http.StatusFound)
}

// HandleAdminSourceDelete removes a source; picks it created stay.
func (s *Server) HandleAdminSourceDelete(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := s.Sources.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Sources.Delete(id); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/sources", http.StatusFound)
}

// formLookback reads the "include posts from the last N minutes/hours/days"
// fields; zero means not set.
func formLookback(r *http.Request) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("lookback_amount")))
	if err != nil || n <= 0 {
		return 0
	}
	unit := map[string]time.Duration{"minutes": time.Minute, "hours": time.Hour, "days": 24 * time.Hour}[r.FormValue("lookback_unit")]
	if unit == 0 {
		unit = time.Hour
	}
	if d := time.Duration(n) * unit; d <= 30*24*time.Hour {
		return d
	}
	return 30 * 24 * time.Hour
}
