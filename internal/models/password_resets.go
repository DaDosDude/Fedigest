package models

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// ResetTokenTTL is how long a password-reset link stays valid.
const ResetTokenTTL = time.Hour

var ErrResetTokenInvalid = errors.New("password reset token is invalid, expired, or already used")

type PasswordResetStore struct {
	DB *sql.DB
}

// Create issues a reset token for a user and returns it.
func (s *PasswordResetStore) Create(userID int64) (token string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw)
	expiresAt := time.Now().Add(ResetTokenTTL).UTC().Format(time.RFC3339)

	_, err = s.DB.Exec(`INSERT INTO password_resets (user_id, token, expires_at) VALUES (?, ?, ?)`,
		userID, token, expiresAt)
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetValidByToken returns the reset row if it exists, is unused and hasn't
// expired; otherwise ErrResetTokenInvalid.
func (s *PasswordResetStore) GetValidByToken(token string) (*PasswordReset, error) {
	row := s.DB.QueryRow(`SELECT id, user_id, token, expires_at, used, created_at FROM password_resets WHERE token = ?`, token)
	var pr PasswordReset
	err := row.Scan(&pr.ID, &pr.UserID, &pr.Token, &pr.ExpiresAt, &pr.Used, &pr.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResetTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if pr.Used {
		return nil, ErrResetTokenInvalid
	}
	exp, err := time.Parse(time.RFC3339, pr.ExpiresAt)
	if err != nil || time.Now().After(exp) {
		return nil, ErrResetTokenInvalid
	}
	return &pr, nil
}

// Claim marks a token used and reports whether this call was the one that
// did, so two simultaneous requests can't both spend it.
func (s *PasswordResetStore) Claim(id int64) (bool, error) {
	res, err := s.DB.Exec(`UPDATE password_resets SET used = 1 WHERE id = ? AND used = 0`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteExpiredLogins deletes expired sessions and reset links. Expired
// ones are already refused; this just keeps the tables small.
func DeleteExpiredLogins(db *sql.DB, now time.Time) error {
	cutoff := now.UTC().Format(time.RFC3339)
	if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM password_resets WHERE expires_at < ?`, cutoff)
	return err
}
