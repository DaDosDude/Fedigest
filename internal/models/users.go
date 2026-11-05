package models

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("not found")

type UserStore struct {
	DB *sql.DB
}

func (s *UserStore) GetByID(id int64) (*User, error) {
	row := s.DB.QueryRow(`
		SELECT id, username, display_name, email, password_hash, role, avatar_upload_path, created_at, updated_at
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *UserStore) GetByUsername(username string) (*User, error) {
	row := s.DB.QueryRow(`
		SELECT id, username, display_name, email, password_hash, role, avatar_upload_path, created_at, updated_at
		FROM users WHERE username = ?`, username)
	return scanUser(row)
}

// GetByEmail returns the first user with this email, ignoring case
// (emails aren't unique).
func (s *UserStore) GetByEmail(email string) (*User, error) {
	row := s.DB.QueryRow(`
		SELECT id, username, display_name, email, password_hash, role, avatar_upload_path, created_at, updated_at
		FROM users WHERE lower(email) = lower(?) ORDER BY id LIMIT 1`, strings.TrimSpace(email))
	return scanUser(row)
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarUploadPath, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Create inserts a new user.
func (s *UserStore) Create(u *User) (int64, error) {
	res, err := s.DB.Exec(`
		INSERT INTO users (username, display_name, email, password_hash, role, avatar_upload_path)
		VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.Email, u.PasswordHash, u.Role, u.AvatarUploadPath)
	if err != nil {
		return 0, fmt.Errorf("creating user: %w", err)
	}
	return res.LastInsertId()
}

// UpdatePasswordHash sets a new password and logs the account out
// everywhere except keepSession (the session making the change, or ""),
// and voids its unused reset links.
func (s *UserStore) UpdatePasswordHash(userID int64, hash, keepSession string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ?, updated_at = datetime('now') WHERE id = ?`, hash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepSession); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE password_resets SET used = 1 WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// Update changes a user's username, display name, email and role. A
// duplicate username returns the UNIQUE constraint error.
func (s *UserStore) Update(u *User) error {
	_, err := s.DB.Exec(`
		UPDATE users SET username = ?, display_name = ?, email = ?, role = ?, updated_at = datetime('now')
		WHERE id = ?`,
		u.Username, u.DisplayName, u.Email, u.Role, u.ID)
	return err
}

func (s *UserStore) List() ([]*User, error) {
	rows, err := s.DB.Query(`
		SELECT id, username, display_name, email, password_hash, role, avatar_upload_path, created_at, updated_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarUploadPath, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// SetAvatar sets (or, with an invalid NullString, clears) a user's
// uploaded avatar path.
func (s *UserStore) SetAvatar(userID int64, path sql.NullString) error {
	_, err := s.DB.Exec(`UPDATE users SET avatar_upload_path = ?, updated_at = datetime('now') WHERE id = ?`, path, userID)
	return err
}

// CountAdmins returns how many admin accounts exist.
func (s *UserStore) CountAdmins() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(1) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// Delete removes a user. Their own non-auto-ingested picks are set back to
// draft, and scheduled ones unscheduled; all their picks and sources are
// reassigned to reassignTo. Returns how many picks were taken offline
// (published or scheduled).
func (s *UserStore) Delete(userID, reassignTo int64) (int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE picks SET status = 'draft', publish_at = NULL, updated_at = datetime('now')
		WHERE curator_id = ? AND auto_ingested = 0 AND (status = 'published' OR publish_at IS NOT NULL)`, userID)
	if err != nil {
		return 0, err
	}
	unpublished, _ := res.RowsAffected()
	for _, q := range []string{
		`UPDATE picks SET curator_id = ? WHERE curator_id = ?`,
		`UPDATE sources SET created_by_user_id = ? WHERE created_by_user_id = ?`,
	} {
		if _, err := tx.Exec(q, reassignTo, userID); err != nil {
			return 0, err
		}
	}
	for _, q := range []string{
		`DELETE FROM sessions WHERE user_id = ?`,
		`DELETE FROM password_resets WHERE user_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, userID); err != nil {
			return 0, err
		}
	}
	return int(unpublished), tx.Commit()
}
