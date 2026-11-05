package models

import "database/sql"

// ThemeMode says whether a theme is light, dark or fixed, and which theme
// to use instead when the visitor's device is in the other mode.
type ThemeMode struct {
	Name    string
	Mode    string // "light", "dark" or "fixed"
	Partner string // "" for none
}

type ThemeModeStore struct {
	DB *sql.DB
}

// All returns every stored mode by theme name; themes without a row are fixed.
func (s *ThemeModeStore) All() (map[string]ThemeMode, error) {
	rows, err := s.DB.Query(`SELECT name, mode, COALESCE(partner, '') FROM theme_modes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ThemeMode{}
	for rows.Next() {
		var m ThemeMode
		if err := rows.Scan(&m.Name, &m.Mode, &m.Partner); err != nil {
			return nil, err
		}
		out[m.Name] = m
	}
	return out, rows.Err()
}

func (s *ThemeModeStore) Set(name, mode, partner string) error {
	_, err := s.DB.Exec(`INSERT INTO theme_modes (name, mode, partner) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET mode = excluded.mode, partner = excluded.partner`,
		name, mode, sql.NullString{String: partner, Valid: partner != ""})
	return err
}
