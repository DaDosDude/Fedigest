package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"

	"fedigest/internal/auth"
	"fedigest/internal/models"
)

// ensureFirstAdmin creates an admin account with a random password (printed
// to the log once) when no users exist, so a fresh deploy isn't locked out.
// username defaults to "admin".
func ensureFirstAdmin(users *models.UserStore, username string) error {
	existing, err := users.List()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	if username == "" {
		username = "admin"
	}

	password, err := randomPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	_, err = users.Create(&models.User{
		Username:     username,
		PasswordHash: hash,
		Role:         models.RoleAdmin,
	})
	if err != nil {
		return err
	}

	log.Printf("======================================================")
	log.Printf("First-run setup: created admin account")
	log.Printf("  username: %s", username)
	log.Printf("  password: %s", password)
	log.Printf("Log in and change this password immediately.")
	log.Printf("======================================================")
	return nil
}

func randomPassword() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("failed to generate random password")
	}
	return hex.EncodeToString(raw), nil
}
