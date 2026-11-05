package models

import "database/sql"

// SiteSettingsStore manages the single-row (id = 1) site_settings table.
type SiteSettingsStore struct {
	DB *sql.DB
}

// Get returns the site settings row.
func (s *SiteSettingsStore) Get() (*SiteSettings, error) {
	row := s.DB.QueryRow(`SELECT id, site_name, icon_path, theme, auto_post_label, multi_category, visitor_themes, privacy_contact FROM site_settings WHERE id = 1`)
	var ss SiteSettings
	if err := row.Scan(&ss.ID, &ss.SiteName, &ss.IconPath, &ss.Theme, &ss.AutoPostLabel, &ss.MultiCategory, &ss.VisitorThemes, &ss.PrivacyContact); err != nil {
		return nil, err
	}
	return &ss, nil
}

// UpdateSiteName sets the site name.
func (s *SiteSettingsStore) UpdateSiteName(name string) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET site_name = ? WHERE id = 1`, name)
	return err
}

func (s *SiteSettingsStore) UpdateTheme(theme string) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET theme = ? WHERE id = 1`, theme)
	return err
}

// UpdateAutoPostLabel sets the byline label shown on auto-ingested picks.
func (s *SiteSettingsStore) UpdateAutoPostLabel(label string) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET auto_post_label = ? WHERE id = 1`, label)
	return err
}

// UpdateIconPath sets or clears (pass an invalid/empty NullString) the site
// icon's path relative to the uploads directory.
func (s *SiteSettingsStore) UpdateIconPath(path sql.NullString) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET icon_path = ? WHERE id = 1`, path)
	return err
}

// UpdateMultiCategory turns extra categories per pick/source on or off.
func (s *SiteSettingsStore) UpdateMultiCategory(on bool) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET multi_category = ? WHERE id = 1`, on)
	return err
}

// MultiCategory reports whether extra categories are enabled (true if the
// setting can't be read).
func (s *SiteSettingsStore) MultiCategory() bool {
	ss, err := s.Get()
	return err != nil || ss.MultiCategory
}

// UpdateVisitorThemes sets which themes visitors may choose ("*" = all).
func (s *SiteSettingsStore) UpdateVisitorThemes(v string) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET visitor_themes = ? WHERE id = 1`, v)
	return err
}

// UpdatePrivacyContact sets the privacy page's contact line; "" means the
// generic one.
func (s *SiteSettingsStore) UpdatePrivacyContact(contact string) error {
	_, err := s.DB.Exec(`UPDATE site_settings SET privacy_contact = ? WHERE id = 1`, sql.NullString{String: contact, Valid: contact != ""})
	return err
}
