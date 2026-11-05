package web

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fedigest/internal/auth"
	"fedigest/internal/models"
	"fedigest/internal/slug"
)

const maxUploadBytes = 10 << 20 // 10MB

// adminPickRow is one row of the admin picks list.
type adminPickRow struct {
	*models.Pick
	Curator    string
	Category   string
	curatorKey string // user ID, or "auto" for auto posts
	Views      int
	// ExtraNames are the pick's extra categories; categoryIDs is main + extras.
	ExtraNames  []string
	categoryIDs []int64
	when        time.Time
}

// pickerOption is one choice in the curator / category picker.
type pickerOption struct {
	Value, Label string
	Count        int
}

func pickerOptions(rows []adminPickRow, key func(adminPickRow) (string, string)) []pickerOption {
	idx := map[string]int{}
	var out []pickerOption
	for _, r := range rows {
		v, label := key(r)
		if i, ok := idx[v]; ok {
			out[i].Count++
			continue
		}
		idx[v] = len(out)
		out = append(out, pickerOption{Value: v, Label: label, Count: 1})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// withKey returns v encoded with key set to val ("" removes it).
func withKey(v url.Values, key, val string) string {
	c := url.Values{}
	for k := range v {
		c.Set(k, v.Get(k))
	}
	if val == "" {
		c.Del(key)
	} else {
		c.Set(key, val)
	}
	return c.Encode()
}

// pickSorts are the admin picks list's sort keys and the direction each
// starts in ("desc" for dates and flags, so newest / flagged come first).
var pickSorts = map[string]string{
	"date": "desc", "title": "asc",
	"community": "asc", "views": "desc",
}

func pickTime(p *models.Pick) time.Time {
	v := p.CreatedAt
	if p.PublishedAt.Valid && p.PublishedAt.String != "" {
		v = p.PublishedAt.String
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// HandleAdminPicksList shows all picks to an admin, or a user's own picks,
// sorted by ?sort=<key>&dir=asc|desc (see pickSorts).
func (s *Server) HandleAdminPicksList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var picks []*models.Pick
	var err error
	if u.Role == models.RoleAdmin {
		picks, err = s.Picks.ListAll()
	} else {
		picks, err = s.Picks.ListOwnedByUser(u.ID)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	q := r.URL.Query()
	unavailable := 0
	for _, p := range picks {
		if p.SourceUnavailable {
			unavailable++
		}
	}

	autoLabel := "Auto post"
	if ss, err := s.SiteSettings.Get(); err == nil && ss.AutoPostLabel != "" {
		autoLabel = ss.AutoPostLabel
	}
	userNames := map[int64]string{}
	if users, err := s.Users.List(); err == nil {
		for _, usr := range users {
			userNames[usr.ID] = usr.DisplayNameOrUsername()
		}
	}
	catNames := map[int64]string{}
	if cats, err := s.Categories.List(); err == nil {
		for _, c := range cats {
			catNames[c.ID] = c.Name
		}
	}
	viewCounts, _ := s.Picks.ViewCounts()
	pickIDs := make([]int64, len(picks))
	for i, p := range picks {
		pickIDs[i] = p.ID
	}
	extraCats, _ := s.Picks.ExtraCategories(pickIDs...)
	rows := make([]adminPickRow, 0, len(picks))
	for _, p := range picks {
		row := adminPickRow{Pick: p, Curator: userNames[p.CuratorID], Category: catNames[p.CategoryID], Views: viewCounts[p.ID], when: pickTime(p)}
		row.categoryIDs = append([]int64{p.CategoryID}, extraCats[p.ID]...)
		for _, c := range extraCats[p.ID] {
			row.ExtraNames = append(row.ExtraNames, catNames[c])
		}
		row.curatorKey = strconv.FormatInt(p.CuratorID, 10)
		if p.AutoIngested {
			row.Curator = autoLabel
			row.curatorKey = "auto"
		}
		rows = append(rows, row)
	}

	// Curator / category pickers list only what occurs in these picks, with
	// counts, and narrow the list to one of them.
	curatorOpts := pickerOptions(rows, func(r adminPickRow) (string, string) { return r.curatorKey, r.Curator })
	categoryOpts := categoryOptions(rows, catNames)
	// Filters; the two-way ones (status, type, pinned, unavailable) are
	// "" for both, or one of their two values.
	curator, category := q.Get("curator"), q.Get("category")
	pstatus, ptype := q.Get("status"), q.Get("type")
	pinned, unavail := q.Get("pinned"), q.Get("unavailable")
	if q.Get("filter") == "unavailable" { // old link form
		unavail = "yes"
	}
	yesNo := func(want string, have bool) bool { return want == "" || (want == "yes") == have }
	kept := rows[:0]
	for _, r := range rows {
		if (curator == "" || r.curatorKey == curator) &&
			(category == "" || r.hasCategory(category)) &&
			(pstatus == "" || string(r.Status) == pstatus) &&
			(ptype == "" || string(r.SourceType) == ptype) &&
			yesNo(pinned, r.PinnedAt.Valid) &&
			yesNo(unavail, r.SourceUnavailable) {
			kept = append(kept, r)
		}
	}
	rows = kept
	// Filtering by a category: show that one first in each row.
	if category != "" {
		for i, r := range rows {
			if strconv.FormatInt(r.CategoryID, 10) == category {
				continue
			}
			names := []string{r.Category}
			for j, c := range r.categoryIDs[1:] {
				if strconv.FormatInt(c, 10) == category {
					rows[i].Category = r.ExtraNames[j]
				} else {
					names = append(names, r.ExtraNames[j])
				}
			}
			rows[i].ExtraNames = names
		}
	}
	// keep carries the active filters into sort links and the back field.
	keep := url.Values{}
	for k, v := range map[string]string{"curator": curator, "category": category, "status": pstatus, "type": ptype, "pinned": pinned, "unavailable": unavail} {
		if v != "" {
			keep.Set(k, v)
		}
	}

	sortKey := q.Get("sort")
	if _, ok := pickSorts[sortKey]; !ok {
		sortKey = "date"
	}
	dir := q.Get("dir")
	if dir != "asc" && dir != "desc" {
		dir = pickSorts[sortKey]
	}
	sortPickRows(rows, sortKey, dir == "desc")

	// Header links: clicking the current sort column flips its direction,
	// any other column starts in its default direction.
	sortLinks := map[string]string{}
	arrows := map[string]string{}
	for key, def := range pickSorts {
		d := def
		if key == sortKey {
			d = map[string]string{"asc": "desc", "desc": "asc"}[dir]
			arrows[key] = map[string]string{"asc": " ▲", "desc": " ▼"}[dir]
		}
		v := url.Values{"sort": {key}, "dir": {d}}
		for k := range keep {
			v.Set(k, keep.Get(k))
		}
		sortLinks[key] = "/admin/picks?" + v.Encode()
	}

	running, status, finished := s.refresh.get()
	refreshFinished := ""
	if !running && !finished.IsZero() {
		refreshFinished = finished.UTC().Format(time.RFC3339)
	}
	s.renderAdmin(w, r, "admin_picks.html", map[string]any{
		"SortLinks":       sortLinks,
		"Arrows":          arrows,
		"Picks":           rows,
		"CurrentUser":     u,
		"RefreshRunning":  running,
		"RefreshStatus":   status,
		"RefreshFinished": refreshFinished,
		"Unavailable":     unavailable,
		"Filtered":        len(keep) > 0,
		"PinnedFilter":    pinned,
		"UnavailFilter":   unavail,
		"Curator":         curator,
		"CategoryFilter":  category,
		"StatusFilter":    pstatus,
		"TypeFilter":      ptype,
		"CuratorOptions":  curatorOpts,
		"CategoryOpts":    categoryOpts,
		"UnavailableURL":  "/admin/picks?" + withKey(keep, "unavailable", "yes"),
		"ClearURL":        "/admin/picks",
		"Sort":            sortKey,
		"Dir":             dir,
		"Query":           r.URL.RawQuery,
	})
}

// sortPickRows sorts by key; ties fall back to newest first, so groups
// (a category, a curator) list their picks newest first.
func sortPickRows(rows []adminPickRow, key string, desc bool) {
	fold := strings.ToLower
	cmp := func(a, b adminPickRow) int {
		switch key {
		case "title":
			return strings.Compare(fold(a.Title), fold(b.Title))
		case "views":
			switch {
			case a.Views < b.Views:
				return -1
			case a.Views > b.Views:
				return 1
			}
			return 0
		case "community":
			return strings.Compare(fold(a.OriginalCommunity.String), fold(b.OriginalCommunity.String))
		}
		return a.when.Compare(b.when)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := cmp(rows[i], rows[j])
		if desc {
			c = -c
		}
		if c != 0 {
			return c < 0
		}
		return rows[i].when.After(rows[j].when)
	})
}

// backToPicks returns to the picks list with the sort/filter the form was
// submitted from (the "back" field holds that page's query string).
func backToPicks(w http.ResponseWriter, r *http.Request) {
	target := "/admin/picks"
	if q := r.FormValue("back"); q != "" && !strings.ContainsAny(q, "/\\\r\n") {
		target += "?" + q
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// HandleAdminPickPin pins (pinned=1) or unpins a pick. Admin only.
func (s *Server) HandleAdminPickPin(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := s.Picks.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Picks.SetPinned(id, r.FormValue("pinned") == "1"); err != nil {
		s.serverError(w, err)
		return
	}
	backToPicks(w, r)
}

func (s *Server) HandleAdminPickNewForm(w http.ResponseWriter, r *http.Request) {
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(cats) == 0 {
		s.renderAdmin(w, r, "admin_pick_form.html", map[string]any{
			"IsNew": true,
			"Error": "Create a category first — every pick needs one.",
		})
		return
	}
	s.renderAdmin(w, r, "admin_pick_form.html", map[string]any{
		"IsNew":      true,
		"Categories": cats,
	})
}

func (s *Server) HandleAdminPickEditForm(w http.ResponseWriter, r *http.Request, id int64) {
	u := auth.UserFromContext(r.Context())
	pick, err := s.Picks.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !pick.CanEdit(u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if pick.ExcerptAuto {
		pick.Excerpt = pick.DisplayExcerpt() // what cards show now
	}
	extras, _ := s.Picks.ExtraCategories(pick.ID)
	s.renderAdmin(w, r, "admin_pick_form.html", map[string]any{
		"IsNew":      false,
		"Pick":       pick,
		"Categories": cats,
		"ExtraSet":   idSet(extras[pick.ID]),
	})
}

// HandleAdminPickDelete deletes a pick, subject to Pick.CanEdit.
func (s *Server) HandleAdminPickDelete(w http.ResponseWriter, r *http.Request, id int64) {
	u := auth.UserFromContext(r.Context())
	pick, err := s.Picks.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !pick.CanEdit(u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.Picks.Delete(id); err != nil {
		s.serverError(w, err)
		return
	}
	removeUpload(s.Cfg.UploadsDir, pick.ImageUploadPath.String)
	backToPicks(w, r)
}

// HandleAdminPickCreate saves a new pick via savePickFromForm.

func (s *Server) HandleAdminPickCreate(w http.ResponseWriter, r *http.Request) {
	s.savePickFromForm(w, r, nil)
}

func (s *Server) HandleAdminPickUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	u := auth.UserFromContext(r.Context())
	existing, err := s.Picks.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !existing.CanEdit(u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.savePickFromForm(w, r, existing)
}

// maxPickFormBytes caps a whole pick form submission: one image of up to
// maxUploadBytes plus the text fields.
const maxPickFormBytes = maxUploadBytes + 4<<20

// parseUploadForm parses a (possibly multipart) form of at most limit
// bytes, answering 413 or 400 itself when it can't be read.
func parseUploadForm(w http.ResponseWriter, r *http.Request, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := r.ParseMultipartForm(maxUploadBytes)
	if err == nil || err == http.ErrNotMultipart {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		http.Error(w, fmt.Sprintf("That's too large to upload: images can be at most %d MB. Go back and choose a smaller file.", maxUploadBytes>>20), http.StatusRequestEntityTooLarge)
		return false
	}
	http.Error(w, "bad form", http.StatusBadRequest)
	return false
}

func (s *Server) savePickFromForm(w http.ResponseWriter, r *http.Request, existing *models.Pick) {
	u := auth.UserFromContext(r.Context())
	if !parseUploadForm(w, r, maxPickFormBytes) {
		return
	}
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	hadUpload := r.MultipartForm != nil && len(r.MultipartForm.File["image_upload"]) > 0

	p := &models.Pick{}
	if existing != nil {
		*p = *existing
	} else {
		p.CuratorID = u.ID
	}
	// Everything typed is applied to p first, so a form sent back with an
	// error shows it all again.
	problem := applyPickForm(r, p, existing, cats)
	extras := validCategoryIDs(formExtraCategories(r), cats)

	formErr := func(msg string, dups []*models.Pick) {
		s.renderAdmin(w, r, "admin_pick_form.html", map[string]any{
			"IsNew":      existing == nil,
			"Categories": cats,
			"Pick":       p,
			"Error":      msg,
			"Duplicates": dups,
			"ExtraSet":   idSet(extras),
			"HadUpload":  hadUpload,
		})
	}
	if problem != "" {
		formErr(problem, nil)
		return
	}

	// A new pick of a post or article that's already picked asks for
	// confirmation first.
	if existing == nil && r.FormValue("confirm_duplicate") != "1" {
		dups, err := s.Picks.FindDuplicates(0, p.OriginalURL.String, p.OriginalApID.String, p.LinkURL.String)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if len(dups) > 0 {
			formErr("", dups)
			return
		}
	}

	// An upload overrides a linked URL; a replaced upload is deleted once
	// the pick is saved.
	if file, _, err := r.FormFile("image_upload"); err == nil {
		defer file.Close()
		savedPath, err := saveUploadedImage(s.Cfg.UploadsDir, "picks", file)
		if err != nil {
			formErr("Could not save the uploaded image: "+err.Error(), nil)
			return
		}
		p.ImageUploadPath = sql.NullString{String: savedPath, Valid: true}
		p.ImageURL = sql.NullString{}
	}
	staleUpload := ""
	if existing != nil && existing.ImageUploadPath.String != "" && existing.ImageUploadPath.String != p.ImageUploadPath.String {
		staleUpload = existing.ImageUploadPath.String
	}

	if existing == nil {
		id, err := s.Picks.Create(p)
		if err != nil {
			removeUpload(s.Cfg.UploadsDir, p.ImageUploadPath.String)
			s.serverError(w, err)
			return
		}
		if s.SiteSettings.MultiCategory() {
			if err := s.Picks.SetExtraCategories(id, extras); err != nil {
				s.serverError(w, err)
				return
			}
		}
		http.Redirect(w, r, fmt.Sprintf("/p/%d-%s", id, p.Slug), http.StatusFound)
		return
	}

	if err := s.Picks.Update(p); err != nil {
		s.serverError(w, err)
		return
	}
	if existing.OriginalURL.String != p.OriginalURL.String {
		// Another post: its comments are fetched afresh.
		if err := s.Picks.ClearComments(p.ID); err != nil {
			log.Printf("clearing comments of pick %d: %v", p.ID, err)
		}
	}
	// With the setting off the checkboxes aren't shown, so existing extras
	// are left alone.
	if s.SiteSettings.MultiCategory() {
		if err := s.Picks.SetExtraCategories(p.ID, extras); err != nil {
			s.serverError(w, err)
			return
		}
	}
	removeUpload(s.Cfg.UploadsDir, staleUpload)
	http.Redirect(w, r, fmt.Sprintf("/p/%d-%s", p.ID, p.Slug), http.StatusFound)
}

// applyPickForm copies the pick form's fields (all but the upload) into p
// and returns the first problem found, or "".
func applyPickForm(r *http.Request, p *models.Pick, existing *models.Pick, cats []*models.Category) string {
	nullable := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	problem := ""
	fail := func(msg string) {
		if problem == "" {
			problem = msg
		}
	}

	p.Title = strings.TrimSpace(r.FormValue("title"))
	p.Excerpt = strings.TrimSpace(r.FormValue("excerpt"))
	p.Body = r.FormValue("body")
	p.SourceType = models.SourceType(r.FormValue("source_type"))
	// Set by the form's JS when Fetch metadata fills the excerpt, cleared
	// once the curator edits it.
	p.ExcerptAuto = r.FormValue("excerpt_auto") == "1" && p.SourceType == models.SourceTypeFedi
	p.UseOriginalAsBody = r.FormValue("use_original_as_body") == "on"
	if p.Title == "" || p.Excerpt == "" {
		fail("Title and excerpt are required.")
	}

	p.CategoryID = 0
	if id, err := strconv.ParseInt(r.FormValue("category_id"), 10, 64); err == nil && len(validCategoryIDs([]int64{id}, cats)) == 1 {
		p.CategoryID = id
	} else {
		fail("Choose a category.")
	}

	switch p.SourceType {
	case models.SourceTypeFedi:
		platform := models.Platform(r.FormValue("source_platform"))
		originalURL := withScheme(r.FormValue("original_url"))
		p.SourcePlatform = nullable(string(platform))
		p.OriginalURL = nullable(originalURL)
		if originalURL == "" {
			fail("A linked pick needs the original post's URL. Choose \"Local post\" for a post without one.")
		} else if platform != models.PlatformLemmy && platform != models.PlatformPieFed && platform != models.PlatformMbin {
			fail("Choose the platform the original post is on.")
		}
		p.OriginalAuthor = nullable(r.FormValue("original_author"))
		p.OriginalCommunity = nullable(r.FormValue("original_community"))
		p.CommunityPostURL = nullable(r.FormValue("community_post_url"))
		p.OriginalApID = nullable(r.FormValue("original_ap_id"))
		p.OriginalPublishedAt = nullable(r.FormValue("original_published_at"))
		p.LinkURL = nullable(withScheme(r.FormValue("link_url")))
		// Kept even when use_original_as_body is off, so re-enabling it
		// doesn't require a re-fetch.
		if cache := r.FormValue("original_body_cache"); cache != "" {
			p.OriginalBodyCache = nullable(cache)
		}
		// A changed URL whose details weren't fetched again would keep
		// pointing at the old post; drop what came from it.
		if existing != nil && existing.OriginalURL.String != originalURL &&
			existing.OriginalApID.String != "" && p.OriginalApID.String == existing.OriginalApID.String {
			p.OriginalApID = sql.NullString{}
			p.CommunityPostURL = sql.NullString{}
			p.OriginalAuthor = sql.NullString{}
			p.OriginalCommunity = sql.NullString{}
			p.OriginalPublishedAt = sql.NullString{}
			p.OriginalBodyCache = sql.NullString{}
		}
	case models.SourceTypeLocal:
		p.SourcePlatform = sql.NullString{}
		p.OriginalURL = sql.NullString{}
		p.UseOriginalAsBody = false
		p.OriginalBodyCache = sql.NullString{}
		p.OriginalAuthor = sql.NullString{}
		p.OriginalCommunity = sql.NullString{}
		p.LinkURL = sql.NullString{}
		p.OriginalPublishedAt = sql.NullString{}
		p.OriginalApID = sql.NullString{}
		p.CommunityPostURL = sql.NullString{}
	default:
		p.SourceType = models.SourceTypeFedi
		fail("Choose a post type.")
	}

	// Image: "remove" clears it, a URL replaces it; an upload (handled by
	// the caller) wins over both. Otherwise an edit keeps its image.
	if r.FormValue("remove_image") == "on" {
		p.ImageURL = sql.NullString{}
		p.ImageUploadPath = sql.NullString{}
	} else if imageURL := withScheme(r.FormValue("image_url")); imageURL != "" {
		p.ImageURL = nullable(imageURL)
		p.ImageUploadPath = sql.NullString{}
	} else if p.ImageUploadPath.String == "" {
		p.ImageURL = sql.NullString{}
	}
	p.AltText = nullable(strings.TrimSpace(r.FormValue("alt_text")))

	if p.Slug == "" || existing == nil || existing.Title != p.Title {
		p.Slug = slug.Slugify(p.Title)
	}

	if r.FormValue("action") == "publish" {
		p.Status = models.PickStatusPublished
		p.PublishAt = sql.NullString{}
		if !p.PublishedAt.Valid {
			p.PublishedAt = sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}
		}
	} else {
		p.Status = models.PickStatusDraft
		// publish_at comes from the form's JS as UTC RFC3339; empty
		// unschedules.
		p.PublishAt = sql.NullString{}
		if v := strings.TrimSpace(r.FormValue("publish_at")); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				p.PublishAt = sql.NullString{String: t.UTC().Format(time.RFC3339), Valid: true}
			} else {
				fail("Couldn't read the scheduled time.")
			}
		}
	}
	return problem
}

// validCategoryIDs keeps the IDs that name an existing category.
func validCategoryIDs(ids []int64, cats []*models.Category) []int64 {
	known := map[int64]bool{}
	for _, c := range cats {
		known[c.ID] = true
	}
	var out []int64
	for _, id := range ids {
		if known[id] {
			out = append(out, id)
		}
	}
	return out
}

// removeUpload deletes an uploaded file given its stored relative path.
// Paths resolving outside uploadsDir are ignored, as is a missing file.
func removeUpload(uploadsDir, rel string) {
	if rel == "" {
		return
	}
	base, err := filepath.Abs(uploadsDir)
	if err != nil {
		return
	}
	full, err := filepath.Abs(filepath.Join(base, rel))
	if err != nil || !strings.HasPrefix(full, base+string(filepath.Separator)) {
		return
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		log.Printf("removing upload %s: %v", rel, err)
	}
}

// uploadImageTypes maps the image types accepted for upload to the
// extension they're stored with.
var uploadImageTypes = map[string]string{
	"image/png":                ".png",
	"image/jpeg":               ".jpg",
	"image/gif":                ".gif",
	"image/webp":               ".webp",
	"image/x-icon":             ".ico",
	"image/vnd.microsoft.icon": ".ico",
}

// saveUploadedImage writes an upload into uploadsDir/<subdir>/ under a
// random name and returns its path relative to uploadsDir.
func saveUploadedImage(uploadsDir, subdir string, src io.Reader) (string, error) {
	destDir := filepath.Join(uploadsDir, subdir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	// The extension comes from the content, never the uploaded name, so a
	// file can only ever be served as one of these image types.
	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", errors.New("empty or unreadable file")
	}
	head = head[:n]
	ext, ok := uploadImageTypes[http.DetectContentType(head)]
	if !ok {
		return "", errors.New("only PNG, JPEG, GIF, WebP or ICO images can be uploaded")
	}
	name := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	dest := filepath.Join(destDir, name)

	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()

	n64, err := io.Copy(f, io.LimitReader(io.MultiReader(bytes.NewReader(head), src), maxUploadBytes+1))
	if err == nil && n64 > maxUploadBytes {
		err = fmt.Errorf("images can be at most %d MB", maxUploadBytes>>20)
	}
	if err != nil {
		f.Close()
		os.Remove(dest)
		return "", err
	}
	return filepath.Join(subdir, name), nil
}

func (r adminPickRow) hasCategory(id string) bool {
	for _, c := range r.categoryIDs {
		if strconv.FormatInt(c, 10) == id {
			return true
		}
	}
	return false
}

// categoryOptions counts picks per category, extra categories included.
func categoryOptions(rows []adminPickRow, names map[int64]string) []pickerOption {
	counts := map[int64]int{}
	for _, r := range rows {
		for _, c := range r.categoryIDs {
			counts[c]++
		}
	}
	out := make([]pickerOption, 0, len(counts))
	for id, n := range counts {
		out = append(out, pickerOption{Value: strconv.FormatInt(id, 10), Label: names[id], Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}
