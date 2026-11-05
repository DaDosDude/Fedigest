package models

import (
	"database/sql"
	"net/url"
	"strings"
	"time"
)

// SourceFailureThreshold is how many consecutive poll failures set a
// source's source_unavailable flag.
const SourceFailureThreshold = 5

type SourceStore struct {
	DB *sql.DB
}

var sourceColumns = `
	id, platform, instance_url, watch_target, watch_type, auth_token, mode,
	poll_interval_sec, last_polled_at, consecutive_failures, source_unavailable,
	default_category_id, created_by_user_id, created_at, ingest_since,
	name, exclude_communities
`

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var s Source
	err := row.Scan(
		&s.ID, &s.Platform, &s.InstanceURL, &s.WatchTarget, &s.WatchType, &s.AuthToken, &s.Mode,
		&s.PollIntervalSec, &s.LastPolledAt, &s.ConsecutiveFailures, &s.SourceUnavailable,
		&s.DefaultCategoryID, &s.CreatedByUserID, &s.CreatedAt, &s.IngestSince,
		&s.Name, &s.ExcludeCommunities,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *SourceStore) List() ([]*Source, error) {
	rows, err := s.DB.Query(`SELECT ` + sourceColumns + ` FROM sources ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListPollable returns every source not in manual mode.
func (s *SourceStore) ListPollable() ([]*Source, error) {
	rows, err := s.DB.Query(`SELECT ` + sourceColumns + ` FROM sources WHERE mode != 'manual'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListByCategory returns the sources that post into the given category.
func (s *SourceStore) ListByCategory(categoryID int64) ([]*Source, error) {
	rows, err := s.DB.Query(`SELECT `+sourceColumns+` FROM sources WHERE default_category_id = ? ORDER BY created_at DESC`, categoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *SourceStore) GetByID(id int64) (*Source, error) {
	row := s.DB.QueryRow(`SELECT `+sourceColumns+` FROM sources WHERE id = ?`, id)
	return scanSource(row)
}

func (s *SourceStore) Create(src *Source) (int64, error) {
	// An unset WatchType would fail the watch_type CHECK constraint.
	if src.WatchType == "" {
		src.WatchType = WatchTypeCommunity
	}
	res, err := s.DB.Exec(`
		INSERT INTO sources (platform, instance_url, watch_target, watch_type, auth_token, mode, poll_interval_sec, default_category_id, created_by_user_id, name, exclude_communities)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.Platform, src.InstanceURL, src.WatchTarget, src.WatchType, src.AuthToken, src.Mode, src.PollIntervalSec,
		src.DefaultCategoryID, src.CreatedByUserID, src.Name, src.ExcludeCommunities)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update saves a source's configuration fields, leaving polling state
// (last_polled_at, failure counters) alone.
func (s *SourceStore) Update(src *Source) error {
	if src.WatchType == "" {
		src.WatchType = WatchTypeCommunity
	}
	_, err := s.DB.Exec(`
		UPDATE sources SET
			platform = ?, instance_url = ?, watch_target = ?, watch_type = ?,
			auth_token = ?, mode = ?, poll_interval_sec = ?, default_category_id = ?,
			name = ?, exclude_communities = ?
		WHERE id = ?`,
		src.Platform, src.InstanceURL, src.WatchTarget, src.WatchType,
		src.AuthToken, src.Mode, src.PollIntervalSec, src.DefaultCategoryID,
		src.Name, src.ExcludeCommunities, src.ID)
	return err
}

// MarkPollSuccess resets the failure state and records the poll time.
func (s *SourceStore) MarkPollSuccess(id int64) error {
	_, err := s.DB.Exec(`
		UPDATE sources SET
			last_polled_at = datetime('now'), consecutive_failures = 0, source_unavailable = 0
		WHERE id = ?`, id)
	return err
}

// MarkPollFailure increments the failure streak, sets source_unavailable
// at the threshold, and records last_polled_at so backoff counts from now.
func (s *SourceStore) MarkPollFailure(id int64) error {
	_, err := s.DB.Exec(`
		UPDATE sources SET
			last_polled_at = datetime('now'),
			consecutive_failures = consecutive_failures + 1,
			source_unavailable = CASE WHEN consecutive_failures + 1 >= ? THEN 1 ELSE source_unavailable END
		WHERE id = ?`, SourceFailureThreshold, id)
	return err
}

// Delete removes a source. Picks it created stay, with source_id cleared.
func (s *SourceStore) Delete(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE picks SET source_id = NULL WHERE source_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sources WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ExtraCategories returns a source's extra category IDs.
func (s *SourceStore) ExtraCategories(sourceID int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT category_id FROM source_categories WHERE source_id = ? ORDER BY category_id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var c int64
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetExtraCategories replaces a source's extra categories.
func (s *SourceStore) SetExtraCategories(sourceID int64, categoryIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM source_categories WHERE source_id = ?`, sourceID); err != nil {
		return err
	}
	for _, c := range categoryIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO source_categories (source_id, category_id) VALUES (?, ?)`, sourceID, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetIngestSince makes the source pick up posts newer than t (instead of
// only posts newer than the source itself).
func (s *SourceStore) SetIngestSince(sourceID int64, t time.Time) error {
	_, err := s.DB.Exec(`UPDATE sources SET ingest_since = ? WHERE id = ?`, t.UTC().Format(time.RFC3339), sourceID)
	return err
}

// Host is the source's instance host, e.g. "lemmy.world".
func (s *Source) Host() string {
	if u, err := url.Parse(s.InstanceURL); err == nil && u.Host != "" {
		return u.Host
	}
	return s.InstanceURL
}

// Label is what the admin shows for a source: its name if set, else what
// it watches (the instance host for a whole-instance source).
func (s *Source) Label() string {
	if s.Name.Valid && strings.TrimSpace(s.Name.String) != "" {
		return s.Name.String
	}
	if s.WatchType == WatchTypeInstance {
		return s.Host()
	}
	return s.WatchTarget.String
}

// NormalizeCommunityList turns the exclude box's text (one per line or
// comma-separated; "name", "name@instance", "!name@instance" or a
// community URL) into one lower-case entry per line, without duplicates.
func NormalizeCommunityList(text string) string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' || r == '\r' }) {
		e := normalizeCommunity(f)
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return strings.Join(out, "\n")
}

func normalizeCommunity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, "/") {
		// https://lemmy.world/c/memes, lemmy.world/m/memes, /c/memes@x
		if !strings.Contains(s, "://") && !strings.HasPrefix(s, "/") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || (parts[0] != "c" && parts[0] != "m") || parts[1] == "" {
			return ""
		}
		name := parts[1]
		if !strings.Contains(name, "@") && u.Host != "" {
			name += "@" + u.Host
		}
		return name
	}
	s = strings.TrimPrefix(s, "!")
	if strings.HasPrefix(s, "@") || strings.HasSuffix(s, "@") {
		return ""
	}
	return s
}

// Excludes reports whether a post in community (a "name@instance"
// handle) is on the source's exclude list.
func (s *Source) Excludes(community string) bool {
	if !s.ExcludeCommunities.Valid || community == "" {
		return false
	}
	c := strings.ToLower(community)
	name := c
	if i := strings.Index(c, "@"); i >= 0 {
		name = c[:i]
	}
	for _, e := range strings.Split(s.ExcludeCommunities.String, "\n") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "@") {
			if e == c {
				return true
			}
		} else if e == name {
			return true
		}
	}
	return false
}

// ExcludeCount is the number of communities on the exclude list.
func (s *Source) ExcludeCount() int {
	if !s.ExcludeCommunities.Valid {
		return 0
	}
	n := 0
	for _, e := range strings.Split(s.ExcludeCommunities.String, "\n") {
		if strings.TrimSpace(e) != "" {
			n++
		}
	}
	return n
}
