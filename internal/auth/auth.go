// Package auth implements session-based authentication: a sessions table
// row referenced by an HTTP-only, secure cookie.
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"fedigest/internal/models"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName = "fedigest_session"

	// Session lifetimes; "remember me" gets the long one.
	ShortSessionTTL = 12 * time.Hour
	LongSessionTTL  = 90 * 24 * time.Hour
)

type contextKey int

const userContextKey contextKey = iota

type Service struct {
	DB    *sql.DB
	Users *models.UserStore
}

func NewService(db *sql.DB, users *models.UserStore) *Service {
	return &Service{DB: db, Users: users}
}

// HashPassword hashes a password with bcrypt.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

var (
	dummyHashOnce  sync.Once
	dummyHashValue string
)

// dummyHash is a bcrypt hash to compare against for unknown usernames.
func dummyHash() string {
	dummyHashOnce.Do(func() {
		dummyHashValue, _ = HashPassword("not a real password")
	})
	return dummyHashValue
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Login verifies credentials and, on success, creates a session and sets
// the cookie on w. rememberMe controls whether the session (and cookie)
// lives for LongSessionTTL instead of ShortSessionTTL.
func (s *Service) Login(w http.ResponseWriter, username, password string, rememberMe bool) (*models.User, error) {
	user, err := s.Users.GetByUsername(username)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			// Take as long as a wrong password would, so response times
			// don't reveal which usernames exist.
			CheckPassword(dummyHash(), password)
			return nil, errors.New("invalid username or password")
		}
		return nil, err
	}
	if !CheckPassword(user.PasswordHash, password) {
		return nil, errors.New("invalid username or password")
	}

	ttl := ShortSessionTTL
	if rememberMe {
		ttl = LongSessionTTL
	}
	sessionID, expiresAt, err := s.createSession(user.ID, ttl)
	if err != nil {
		return nil, err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})
	return user, nil
}

func (s *Service) createSession(userID int64, ttl time.Duration) (id string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	id = hex.EncodeToString(raw)
	expiresAt = time.Now().Add(ttl)

	_, err = s.DB.Exec(`INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
		id, userID, expiresAt.UTC().Format(time.RFC3339))
	return id, expiresAt, err
}

// Logout deletes the session referenced by the request's cookie (if any)
// and clears the cookie.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		s.DB.Exec(`DELETE FROM sessions WHERE id = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// userFromRequest resolves the session cookie to a user, or nil.
func (s *Service) userFromRequest(r *http.Request) *models.User {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil
	}

	var userID int64
	var expiresAt string
	err = s.DB.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE id = ?`, c.Value).Scan(&userID, &expiresAt)
	if err != nil {
		return nil
	}

	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().After(exp) {
		s.DB.Exec(`DELETE FROM sessions WHERE id = ?`, c.Value) // lazy cleanup
		return nil
	}

	user, err := s.Users.GetByID(userID)
	if err != nil {
		return nil
	}
	return user
}

// Middleware attaches the resolved user (or nil) to the request context.
// It doesn't reject anonymous requests; RequireUser/RequireAdmin do.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.userFromRequest(r)
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext returns the current request's user, or nil if anonymous.
func UserFromContext(ctx context.Context) *models.User {
	u, _ := ctx.Value(userContextKey).(*models.User)
	return u
}

// RequireUser redirects anonymous requests to /login.
func (s *Service) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin rejects anonymous requests (302 to /login) and non-admin
// requests (403).
func (s *Service) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r.Context())
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if u.Role != models.RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
