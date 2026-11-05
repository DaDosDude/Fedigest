package models

import (
	"database/sql"
	"errors"
)

// ErrCategoryInUse is returned by Delete when picks still reference the
// category.
var ErrCategoryInUse = errors.New("category is still used by one or more picks")

// ErrCategoryHasSources is returned by Delete when auto-ingest sources still
// post into the category and the caller didn't ask to delete them too.
var ErrCategoryHasSources = errors.New("category is still used by one or more sources")

type CategoryStore struct {
	DB *sql.DB
}

func (s *CategoryStore) List() ([]*Category, error) {
	rows, err := s.DB.Query(`SELECT id, name, slug, sort_order, show_on_frontpage, created_at FROM categories ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.SortOrder, &c.ShowOnFrontpage, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *CategoryStore) GetByID(id int64) (*Category, error) {
	row := s.DB.QueryRow(`SELECT id, name, slug, sort_order, show_on_frontpage, created_at FROM categories WHERE id = ?`, id)
	var c Category
	if err := row.Scan(&c.ID, &c.Name, &c.Slug, &c.SortOrder, &c.ShowOnFrontpage, &c.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (s *CategoryStore) GetBySlug(slug string) (*Category, error) {
	row := s.DB.QueryRow(`SELECT id, name, slug, sort_order, show_on_frontpage, created_at FROM categories WHERE slug = ?`, slug)
	var c Category
	if err := row.Scan(&c.ID, &c.Name, &c.Slug, &c.SortOrder, &c.ShowOnFrontpage, &c.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// NextSortOrder is the sort order that puts a new category last.
func (s *CategoryStore) NextSortOrder() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COALESCE(MAX(sort_order) + 1, 0) FROM categories`).Scan(&n)
	return n, err
}

func (s *CategoryStore) Create(name, slug string, sortOrder int, showOnFrontpage bool) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO categories (name, slug, sort_order, show_on_frontpage) VALUES (?, ?, ?, ?)`, name, slug, sortOrder, showOnFrontpage)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *CategoryStore) Update(id int64, name, slug string, sortOrder int, showOnFrontpage bool) error {
	_, err := s.DB.Exec(`UPDATE categories SET name = ?, slug = ?, sort_order = ?, show_on_frontpage = ? WHERE id = ?`, name, slug, sortOrder, showOnFrontpage, id)
	return err
}

// Reorder sets sort_order to each category's position in ids (0, 1, 2…).
// Categories not listed keep their current sort_order.
func (s *CategoryStore) Reorder(ids []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE categories SET sort_order = ? WHERE id = ?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes a category. It fails with ErrCategoryInUse if picks
// reference it, and with ErrCategoryHasSources if sources post into it
// unless deleteSources is set, in which case they're deleted too.
func (s *CategoryStore) Delete(id int64, deleteSources bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM picks WHERE category_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrCategoryInUse
	}
	if err := tx.QueryRow(`SELECT COUNT(1) FROM sources WHERE default_category_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		if !deleteSources {
			return ErrCategoryHasSources
		}
		if _, err := tx.Exec(`UPDATE picks SET source_id = NULL WHERE source_id IN (SELECT id FROM sources WHERE default_category_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sources WHERE default_category_id = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM categories WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
