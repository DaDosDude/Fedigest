package models

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

// CustomTheme is a theme created in the admin UI: a stylesheet only.
type CustomTheme struct {
	Name      string
	CSS       string
	BasedOn   sql.NullString
	UpdatedAt string
}

// ThemeNamePattern is what a custom theme may be called (it becomes part
// of URLs).
var ThemeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

var ErrThemeExists = errors.New("a theme with that name already exists")

type CustomThemeStore struct {
	DB *sql.DB
}

func (s *CustomThemeStore) List() ([]*CustomTheme, error) {
	rows, err := s.DB.Query(`SELECT name, css, based_on, updated_at FROM custom_themes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CustomTheme
	for rows.Next() {
		var t CustomTheme
		if err := rows.Scan(&t.Name, &t.CSS, &t.BasedOn, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// Names lists the custom themes' names, without their stylesheets.
func (s *CustomThemeStore) Names() ([]string, error) {
	rows, err := s.DB.Query(`SELECT name FROM custom_themes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *CustomThemeStore) Get(name string) (*CustomTheme, error) {
	var t CustomTheme
	err := s.DB.QueryRow(`SELECT name, css, based_on, updated_at FROM custom_themes WHERE name = ?`, name).
		Scan(&t.Name, &t.CSS, &t.BasedOn, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return &t, err
}

func (s *CustomThemeStore) Create(name, css, basedOn string) error {
	_, err := s.DB.Exec(`INSERT INTO custom_themes (name, css, based_on) VALUES (?, ?, ?)`, name, css, sql.NullString{String: basedOn, Valid: basedOn != ""})
	if err != nil {
		if _, getErr := s.Get(name); getErr == nil {
			return ErrThemeExists
		}
	}
	return err
}

func (s *CustomThemeStore) UpdateCSS(name, css string) error {
	_, err := s.DB.Exec(`UPDATE custom_themes SET css = ?, updated_at = datetime('now') WHERE name = ?`, css, name)
	return err
}

// Delete removes a custom theme, its mode and any pairing with it.
func (s *CustomThemeStore) Delete(name string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM custom_themes WHERE name = ?`,
		`DELETE FROM theme_modes WHERE name = ?`,
		`UPDATE theme_modes SET partner = NULL WHERE partner = ?`,
	} {
		if _, err := tx.Exec(q, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Rename renames a custom theme and every setting that refers to it: the
// site theme, the visitor theme list and themes copied from it.
func (s *CustomThemeStore) Rename(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if _, err := s.Get(newName); err == nil {
		return ErrThemeExists
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE custom_themes SET name = ?, updated_at = datetime('now') WHERE name = ?`, newName, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE custom_themes SET based_on = ? WHERE based_on = ?`, newName, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE site_settings SET theme = ? WHERE theme = ?`, newName, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE theme_modes SET name = ? WHERE name = ?`, newName, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE theme_modes SET partner = ? WHERE partner = ?`, newName, oldName); err != nil {
		return err
	}
	var visitor string
	if err := tx.QueryRow(`SELECT visitor_themes FROM site_settings WHERE id = 1`).Scan(&visitor); err != nil {
		return err
	}
	if visitor != "*" {
		parts := strings.Split(visitor, ",")
		for i, p := range parts {
			if p == oldName {
				parts[i] = newName
			}
		}
		if _, err := tx.Exec(`UPDATE site_settings SET visitor_themes = ? WHERE id = 1`, strings.Join(parts, ",")); err != nil {
			return err
		}
	}
	return tx.Commit()
}
