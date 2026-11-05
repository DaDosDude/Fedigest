package models

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const PicksPerPage = 15

type PickStore struct {
	DB *sql.DB
}

var pickColumns = `
	id, slug, title, excerpt, excerpt_auto, body, use_original_as_body, original_body_cache,
	category_id, curator_id,
	source_type, source_id, auto_ingested, source_platform, original_url, original_author, original_community, link_url, original_published_at, original_ap_id, community_post_url,
	image_url, image_upload_path, alt_text,
	status, published_at,
	comment_count, comment_count_updated_at, comments_cache, comments_cache_updated_at,
	consecutive_failures, source_unavailable, pinned_at, publish_at,
	created_at, updated_at
`

// qualifiedPickColumns is pickColumns prefixed with "picks.", for joins
// against picks_fts whose column names would otherwise collide.
var qualifiedPickColumns = `
	picks.id, picks.slug, picks.title, picks.excerpt, picks.excerpt_auto, picks.body, picks.use_original_as_body, picks.original_body_cache,
	picks.category_id, picks.curator_id,
	picks.source_type, picks.source_id, picks.auto_ingested, picks.source_platform, picks.original_url, picks.original_author, picks.original_community, picks.link_url, picks.original_published_at, picks.original_ap_id, picks.community_post_url,
	picks.image_url, picks.image_upload_path, picks.alt_text,
	picks.status, picks.published_at,
	picks.comment_count, picks.comment_count_updated_at, picks.comments_cache, picks.comments_cache_updated_at,
	picks.consecutive_failures, picks.source_unavailable, picks.pinned_at, picks.publish_at,
	picks.created_at, picks.updated_at
`

// pickListColumns is pickColumns without the comment-thread cache, which
// can be large and is only needed on the post page (GetByID). List pages
// scan it as NULL.
var pickListColumns = strings.Replace(pickColumns, "comments_cache, comments_cache_updated_at", "NULL, comments_cache_updated_at", 1)

// qualifiedPickListColumns is pickListColumns for joins (see
// qualifiedPickColumns).
var qualifiedPickListColumns = strings.Replace(qualifiedPickColumns, "picks.comments_cache, picks.comments_cache_updated_at", "NULL, picks.comments_cache_updated_at", 1)

func scanPick(row interface{ Scan(...any) error }) (*Pick, error) {
	var p Pick
	err := row.Scan(
		&p.ID, &p.Slug, &p.Title, &p.Excerpt, &p.ExcerptAuto, &p.Body, &p.UseOriginalAsBody, &p.OriginalBodyCache,
		&p.CategoryID, &p.CuratorID,
		&p.SourceType, &p.SourceID, &p.AutoIngested, &p.SourcePlatform, &p.OriginalURL, &p.OriginalAuthor, &p.OriginalCommunity, &p.LinkURL, &p.OriginalPublishedAt, &p.OriginalApID, &p.CommunityPostURL,
		&p.ImageURL, &p.ImageUploadPath, &p.AltText,
		&p.Status, &p.PublishedAt,
		&p.CommentCount, &p.CommentCountUpdatedAt, &p.CommentsCache, &p.CommentsCacheUpdatedAt,
		&p.ConsecutiveFailures, &p.SourceUnavailable, &p.PinnedAt, &p.PublishAt,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PickStore) GetByID(id int64) (*Pick, error) {
	row := s.DB.QueryRow(`SELECT `+pickColumns+` FROM picks WHERE id = ?`, id)
	return scanPick(row)
}

// ListPublished returns published picks, newest first, paginated, optionally
// filtered by category and/or curator.
func (s *PickStore) ListPublished(categoryID, curatorID *int64, page int) ([]*Pick, error) {
	return s.listPublished(categoryID, curatorID, false, false, page)
}

// ListFrontpage is ListPublished minus categories hidden from the frontpage.
func (s *PickStore) ListFrontpage(page int) ([]*Pick, error) {
	return s.listPublished(nil, nil, true, false, page)
}

// ListFrontpageUnpinned is ListFrontpage without pinned picks, which pages
// show separately.
func (s *PickStore) ListFrontpageUnpinned(page int) ([]*Pick, error) {
	return s.listPublished(nil, nil, true, true, page)
}

func (s *PickStore) ListCategoryUnpinned(categoryID int64, page int) ([]*Pick, error) {
	return s.listPublished(&categoryID, nil, false, true, page)
}

// ListPinned returns every published pinned pick, most recently pinned
// first, regardless of category.
func (s *PickStore) ListPinned() ([]*Pick, error) {
	rows, err := s.DB.Query(`SELECT ` + pickListColumns + ` FROM picks WHERE status = 'published' AND pinned_at IS NOT NULL ORDER BY pinned_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPinned pins (stamping the current time) or unpins a pick.
func (s *PickStore) SetPinned(id int64, pinned bool) error {
	var err error
	if pinned {
		_, err = s.DB.Exec(`UPDATE picks SET pinned_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, id)
	} else {
		_, err = s.DB.Exec(`UPDATE picks SET pinned_at = NULL WHERE id = ?`, id)
	}
	return err
}

func (s *PickStore) listPublished(categoryID, curatorID *int64, frontpageOnly, excludePinned bool, page int) ([]*Pick, error) {
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * PicksPerPage

	query := `SELECT ` + pickListColumns + ` FROM picks WHERE status = 'published'`
	args := []any{}
	if categoryID != nil {
		query += ` AND (category_id = ? OR id IN (SELECT pick_id FROM pick_categories WHERE category_id = ?))`
		args = append(args, *categoryID, *categoryID)
	}
	if curatorID != nil {
		query += ` AND curator_id = ?`
		args = append(args, *curatorID)
	}
	if frontpageOnly {
		query += ` AND (category_id IN (SELECT id FROM categories WHERE show_on_frontpage = 1)
			OR id IN (SELECT pc.pick_id FROM pick_categories pc JOIN categories c ON c.id = pc.category_id WHERE c.show_on_frontpage = 1))`
	}
	if excludePinned {
		query += ` AND pinned_at IS NULL`
	}
	query += ` ORDER BY published_at DESC LIMIT ? OFFSET ?`
	args = append(args, PicksPerPage, offset)

	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Search runs an FTS5 query over title/excerpt/body and returns matching
// published picks, best match first.
func (s *PickStore) Search(query string, page int) ([]*Pick, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil // nothing to search for (MATCH '' is a syntax error)
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * PicksPerPage

	rows, err := s.DB.Query(`
		SELECT `+qualifiedPickListColumns+`
		FROM picks
		JOIN picks_fts ON picks_fts.rowid = picks.id
		WHERE picks.status = 'published' AND picks_fts MATCH ?
		ORDER BY bm25(picks_fts) LIMIT ? OFFSET ?`,
		match, PicksPerPage, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ftsQuery turns user input into an FTS5 MATCH query of quoted,
// prefix-matched terms ANDed together. Quoting neutralizes FTS5 operators.
func ftsQuery(input string) string {
	fields := strings.Fields(input)
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		terms = append(terms, `"`+f+`"*`)
	}
	return strings.Join(terms, " AND ")
}

// Create inserts a new pick. The caller validates category_id.
func (s *PickStore) Create(p *Pick) (int64, error) {
	res, err := s.DB.Exec(`
		INSERT INTO picks (
			slug, title, excerpt, excerpt_auto, body, use_original_as_body, original_body_cache,
			category_id, curator_id,
			source_type, source_id, auto_ingested, source_platform, original_url, original_author, original_community, link_url, original_published_at, original_ap_id, community_post_url,
			image_url, image_upload_path, alt_text,
			status, published_at, publish_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Slug, p.Title, p.Excerpt, p.ExcerptAuto, p.Body, p.UseOriginalAsBody, p.OriginalBodyCache,
		p.CategoryID, p.CuratorID,
		p.SourceType, p.SourceID, p.AutoIngested, p.SourcePlatform, p.OriginalURL, p.OriginalAuthor, p.OriginalCommunity, p.LinkURL, p.OriginalPublishedAt, p.OriginalApID, p.CommunityPostURL,
		p.ImageURL, p.ImageUploadPath, p.AltText,
		p.Status, p.PublishedAt, p.PublishAt,
	)
	if err != nil {
		return 0, fmt.Errorf("creating pick: %w", err)
	}
	return res.LastInsertId()
}

// CanEdit reports whether the given user may edit this pick: admins may
// edit any pick, users may only edit their own.
func (p *Pick) CanEdit(u *User) bool {
	if u.Role == RoleAdmin {
		return true
	}
	return p.CuratorID == u.ID
}

// Update overwrites a pick's editable fields. The caller checks ownership.
func (s *PickStore) Update(p *Pick) error {
	_, err := s.DB.Exec(`
		UPDATE picks SET
			slug = ?, title = ?, excerpt = ?, excerpt_auto = ?, body = ?, use_original_as_body = ?, original_body_cache = ?,
			category_id = ?,
			source_type = ?, source_id = ?, source_platform = ?, original_url = ?, original_author = ?, original_community = ?, link_url = ?, original_published_at = ?, original_ap_id = ?, community_post_url = ?,
			image_url = ?, image_upload_path = ?, alt_text = ?,
			status = ?, published_at = ?, publish_at = ?,
			updated_at = datetime('now')
		WHERE id = ?`,
		p.Slug, p.Title, p.Excerpt, p.ExcerptAuto, p.Body, p.UseOriginalAsBody, p.OriginalBodyCache,
		p.CategoryID,
		p.SourceType, p.SourceID, p.SourcePlatform, p.OriginalURL, p.OriginalAuthor, p.OriginalCommunity, p.LinkURL, p.OriginalPublishedAt, p.OriginalApID, p.CommunityPostURL,
		p.ImageURL, p.ImageUploadPath, p.AltText,
		p.Status, p.PublishedAt, p.PublishAt,
		p.ID,
	)
	return err
}

// ListOwnedByUser lists all picks (any status) by curatorID, newest first.
func (s *PickStore) ListOwnedByUser(curatorID int64) ([]*Pick, error) {
	rows, err := s.DB.Query(`SELECT `+pickListColumns+` FROM picks WHERE curator_id = ? ORDER BY created_at DESC`, curatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClearComments forgets a pick's cached comments, count and fetch health,
// for when it's been pointed at another post.
func (s *PickStore) ClearComments(id int64) error {
	_, err := s.DB.Exec(`UPDATE picks SET comment_count = NULL, comment_count_updated_at = NULL,
		comments_cache = NULL, comments_cache_updated_at = NULL,
		consecutive_failures = 0, source_unavailable = 0 WHERE id = ?`, id)
	return err
}

// CommentFailureThreshold is how many consecutive comment-fetch failures
// set the admin-visible source_unavailable flag.
const CommentFailureThreshold = 5

// SaveCommentsSuccess stores a fetched thread and count and resets the
// failure state.
func (s *PickStore) SaveCommentsSuccess(pickID int64, count int, cacheJSON string) error {
	_, err := s.DB.Exec(`
		UPDATE picks SET
			comment_count = ?, comment_count_updated_at = datetime('now'),
			comments_cache = ?, comments_cache_updated_at = datetime('now'),
			consecutive_failures = 0, source_unavailable = 0
		WHERE id = ?`, count, cacheJSON, pickID)
	return err
}

// SaveCommentCountSuccess stores a fetched count and resets the failure state.
func (s *PickStore) SaveCommentCountSuccess(pickID int64, count int) error {
	_, err := s.DB.Exec(`
		UPDATE picks SET
			comment_count = ?, comment_count_updated_at = datetime('now'),
			consecutive_failures = 0, source_unavailable = 0
		WHERE id = ?`, count, pickID)
	return err
}

// RecordCommentFetchFailure increments the failure streak and sets
// source_unavailable at the threshold, leaving the cache intact.
func (s *PickStore) RecordCommentFetchFailure(pickID int64) error {
	_, err := s.DB.Exec(`
		UPDATE picks SET
			consecutive_failures = consecutive_failures + 1,
			source_unavailable = CASE WHEN consecutive_failures + 1 >= ? THEN 1 ELSE source_unavailable END
		WHERE id = ?`, CommentFailureThreshold, pickID)
	return err
}

// ListPublishedFedi returns the published fedi picks published since the
// given time, plus every pinned one, newest first.
func (s *PickStore) ListPublishedFedi(since time.Time) ([]*Pick, error) {
	rows, err := s.DB.Query(`SELECT `+pickListColumns+` FROM picks
		WHERE status = 'published' AND source_type = 'fedi' AND (published_at >= ? OR pinned_at IS NOT NULL)
		ORDER BY published_at DESC`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a pick. The caller checks ownership.
func (s *PickStore) Delete(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM picks WHERE id = ?`, id)
	return err
}

// ListAll lists every pick (any status), newest first.
func (s *PickStore) ListAll() ([]*Pick, error) {
	rows, err := s.DB.Query(`SELECT ` + pickListColumns + ` FROM picks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FillSourceMetadata fills a fedi pick's source-derived fields that are
// currently empty, never overwriting existing values. Reports whether
// anything changed.
func (s *PickStore) FillSourceMetadata(id int64, author, community, publishedAt, linkURL, bodyCache, apID, communityPostURL, imageURL string) (bool, error) {
	res, err := s.DB.Exec(`
		UPDATE picks SET
			original_author       = CASE WHEN IFNULL(original_author, '') = ''       THEN NULLIF(?, '') ELSE original_author END,
			original_community    = CASE WHEN IFNULL(original_community, '') = ''    THEN NULLIF(?, '') ELSE original_community END,
			original_published_at = CASE WHEN IFNULL(original_published_at, '') = '' THEN NULLIF(?, '') ELSE original_published_at END,
			link_url              = CASE WHEN IFNULL(link_url, '') = ''              THEN NULLIF(?, '') ELSE link_url END,
			original_body_cache   = CASE WHEN IFNULL(original_body_cache, '') = ''   THEN NULLIF(?, '') ELSE original_body_cache END,
			original_ap_id        = CASE WHEN IFNULL(original_ap_id, '') = ''        THEN NULLIF(?, '') ELSE original_ap_id END,
			community_post_url    = CASE WHEN IFNULL(community_post_url, '') = ''    THEN NULLIF(?, '') ELSE community_post_url END,
			image_url             = CASE WHEN IFNULL(image_url, '') = '' AND IFNULL(image_upload_path, '') = '' THEN NULLIF(?, '') ELSE image_url END
		WHERE id = ? AND source_type = 'fedi' AND (
			(IFNULL(original_author, '') = ''       AND ? != '') OR
			(IFNULL(original_community, '') = ''    AND ? != '') OR
			(IFNULL(original_published_at, '') = '' AND ? != '') OR
			(IFNULL(link_url, '') = ''              AND ? != '') OR
			(IFNULL(original_body_cache, '') = ''   AND ? != '') OR
			(IFNULL(original_ap_id, '') = ''        AND ? != '') OR
			(IFNULL(community_post_url, '') = ''    AND ? != '') OR
			(IFNULL(image_url, '') = '' AND IFNULL(image_upload_path, '') = '' AND ? != '')
		)`,
		author, community, publishedAt, linkURL, bodyCache, apID, communityPostURL, imageURL,
		id,
		author, community, publishedAt, linkURL, bodyCache, apID, communityPostURL, imageURL,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetAutoExcerpt stores a new automatic excerpt; a hand-written one
// (excerpt_auto off) is never touched. Reports whether it changed.
func (s *PickStore) SetAutoExcerpt(id int64, excerpt string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE picks SET excerpt = ? WHERE id = ? AND excerpt_auto = 1 AND excerpt != ?`, excerpt, id, excerpt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FindDuplicates returns picks (any status, other than excludeID) for the
// same original post — matched on original_url or ActivityPub ID, either
// way round — or, when linkURL is set, linking to the same article.
func (s *PickStore) FindDuplicates(excludeID int64, originalURL, apID, linkURL string) ([]*Pick, error) {
	ids := []any{}
	cond := []string{}
	for _, v := range []string{originalURL, apID} {
		if v == "" {
			continue
		}
		cond = append(cond, `original_url = ?`, `original_ap_id = ?`)
		ids = append(ids, v, v)
	}
	if linkURL != "" {
		cond = append(cond, `link_url = ?`)
		ids = append(ids, linkURL)
	}
	if len(cond) == 0 {
		return nil, nil
	}
	args := append([]any{excludeID}, ids...)
	rows, err := s.DB.Query(`SELECT `+pickListColumns+` FROM picks WHERE id != ? AND (`+strings.Join(cond, " OR ")+`) ORDER BY created_at DESC LIMIT 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pick
	for rows.Next() {
		p, err := scanPick(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PublishDue publishes every draft whose publish_at has passed, with that
// time as its published_at. Returns how many were published.
func (s *PickStore) PublishDue(now time.Time) (int64, error) {
	res, err := s.DB.Exec(`
		UPDATE picks SET status = 'published', published_at = publish_at, publish_at = NULL, updated_at = datetime('now')
		WHERE status = 'draft' AND publish_at IS NOT NULL AND publish_at <= ?`,
		now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PickRef is the little the sitemap needs about a published pick.
type PickRef struct {
	ID        int64
	Slug      string
	CuratorID int64
	UpdatedAt string
}

// ListPublishedRefs returns every published pick's ID, slug, curator and
// last update, newest first.
func (s *PickStore) ListPublishedRefs() ([]PickRef, error) {
	rows, err := s.DB.Query(`SELECT id, slug, curator_id, updated_at FROM picks WHERE status = 'published' ORDER BY published_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PickRef
	for rows.Next() {
		var p PickRef
		if err := rows.Scan(&p.ID, &p.Slug, &p.CuratorID, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddView counts one post-page view.
func (s *PickStore) AddView(id int64) error {
	_, err := s.DB.Exec(`INSERT INTO pick_views (pick_id, views) VALUES (?, 1)
		ON CONFLICT(pick_id) DO UPDATE SET views = views + 1`, id)
	return err
}

// ViewCounts returns every pick's view count by pick ID.
func (s *PickStore) ViewCounts() (map[int64]int, error) {
	rows, err := s.DB.Query(`SELECT pick_id, views FROM pick_views`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ExtraCategories returns the extra category IDs of the given picks, by
// pick ID.
func (s *PickStore) ExtraCategories(pickIDs ...int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(pickIDs) == 0 {
		return out, nil
	}
	q := `SELECT pick_id, category_id FROM pick_categories`
	args := []any{}
	if len(pickIDs) < 500 {
		q += ` WHERE pick_id IN (?` + strings.Repeat(",?", len(pickIDs)-1) + `)`
		for _, id := range pickIDs {
			args = append(args, id)
		}
	}
	rows, err := s.DB.Query(q+` ORDER BY pick_id, category_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, c int64
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		out[p] = append(out[p], c)
	}
	return out, rows.Err()
}

// SetExtraCategories replaces a pick's extra categories. The main category
// is never stored as an extra.
func (s *PickStore) SetExtraCategories(pickID int64, categoryIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM pick_categories WHERE pick_id = ?`, pickID); err != nil {
		return err
	}
	for _, c := range categoryIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO pick_categories (pick_id, category_id)
			SELECT ?, ? WHERE ? != (SELECT category_id FROM picks WHERE id = ?)`, pickID, c, c, pickID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddCategories adds categories to a pick, skipping its main category and
// ones it already has.
func (s *PickStore) AddCategories(pickID int64, categoryIDs []int64) error {
	for _, c := range categoryIDs {
		if _, err := s.DB.Exec(`INSERT OR IGNORE INTO pick_categories (pick_id, category_id)
			SELECT ?, ? WHERE ? != (SELECT category_id FROM picks WHERE id = ?)`, pickID, c, c, pickID); err != nil {
			return err
		}
	}
	return nil
}
