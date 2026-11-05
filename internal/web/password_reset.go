package web

import (
	"log"
	"net/http"
	"strings"

	"fedigest/internal/auth"
	"fedigest/internal/mail"
)

// HandleForgotPasswordForm shows the "enter your email" form (SMTP only).
func (s *Server) HandleForgotPasswordForm(w http.ResponseWriter, r *http.Request) {
	head := s.newSEOHead(r, "Forgot password", "", "/forgot-password")
	head.NoIndex = true
	s.render(w, "forgot_password.html", map[string]any{
		"EmailEnabled": s.Cfg.EmailEnabled(),
		"Head":         head,
	})
}

// HandleForgotPasswordSubmit always shows the same confirmation, so
// registered addresses can't be enumerated.
func (s *Server) HandleForgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.EmailEnabled() {
		http.Error(w, "password reset by email isn't configured on this site", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))

	// Same page even when this IP has hit the reset-email limit.
	resetKey := "reset-ip:" + clientIP(r)
	limited, _ := s.limiter().locked(resetKey)
	if !limited {
		s.limiter().fail(resetKey, resetIPLimit) // counts every request, not just failures
	}
	if email != "" && !limited {
		if user, err := s.Users.GetByEmail(email); err == nil && user.Email.Valid && user.Email.String != "" {
			if token, err := s.PasswordResets.Create(user.ID); err == nil {
				link := s.baseURL(r) + "/reset-password?token=" + token
				body := "Someone (hopefully you) requested a password reset for your " + s.siteName() + " account.\n\n" +
					"Reset your password: " + link + "\n\n" +
					"This link expires in an hour. If you didn't request this, you can ignore this email."
				to, subject := user.Email.String, "Reset your "+s.siteName()+" password"
				// Sent in the background, so the response time doesn't
				// reveal whether the address belongs to an account.
				go func() {
					if err := mail.Send(s.Cfg, to, subject, body); err != nil {
						log.Printf("sending password reset email: %v", err)
					}
				}()
			} else {
				log.Printf("creating password reset token: %v", err)
			}
		}
	}

	head := s.newSEOHead(r, "Check your email", "", "/forgot-password")
	head.NoIndex = true
	s.render(w, "forgot_password.html", map[string]any{
		"EmailEnabled": true,
		"Submitted":    true,
		"Head":         head,
	})
}

// HandleResetPasswordForm shows the new-password form if the token is valid.
func (s *Server) HandleResetPasswordForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	head := s.newSEOHead(r, "Reset password", "", "/reset-password")
	head.NoIndex = true

	if _, err := s.PasswordResets.GetValidByToken(token); err != nil {
		s.render(w, "reset_password.html", map[string]any{
			"Invalid": true,
			"Head":    head,
		})
		return
	}
	s.render(w, "reset_password.html", map[string]any{
		"Token": token,
		"Head":  head,
	})
}

func (s *Server) HandleResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	head := s.newSEOHead(r, "Reset password", "", "/reset-password")
	head.NoIndex = true

	reset, err := s.PasswordResets.GetValidByToken(token)
	if err != nil {
		s.render(w, "reset_password.html", map[string]any{"Invalid": true, "Head": head})
		return
	}

	formErr := func(msg string) {
		s.render(w, "reset_password.html", map[string]any{"Error": msg, "Token": token, "Head": head})
	}

	if newPassword == "" || newPassword != confirm {
		formErr("New password and confirmation must match.")
		return
	}

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if ok, err := s.PasswordResets.Claim(reset.ID); err != nil {
		s.serverError(w, err)
		return
	} else if !ok {
		s.render(w, "reset_password.html", map[string]any{"Invalid": true, "Head": head})
		return
	}
	if err := s.Users.UpdatePasswordHash(reset.UserID, hash, ""); err != nil {
		s.serverError(w, err)
		return
	}

	http.Redirect(w, r, "/login", http.StatusFound)
}
