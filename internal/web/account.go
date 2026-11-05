package web

import (
	"database/sql"
	"errors"
	"net/http"

	"fedigest/internal/auth"
)

// HandleAccountPage shows the self-service account page to any logged-in user.

func (s *Server) HandleAccountPage(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, map[string]any{})
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, data map[string]any) {
	u := auth.UserFromContext(r.Context())
	if u != nil {
		if fresh, err := s.Users.GetByID(u.ID); err == nil {
			u = fresh
		}
	}
	data["Me"] = u
	s.renderAdmin(w, r, "account.html", data)
}

// HandleAccountAvatar lets a logged-in user upload or remove their avatar.
func (s *Server) HandleAccountAvatar(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if !parseUploadForm(w, r, maxPickFormBytes) {
		return
	}
	if err := s.applyAvatarForm(r, u.ID); err != nil {
		s.renderAccount(w, r, map[string]any{"Error": err.Error()})
		return
	}
	s.renderAccount(w, r, map[string]any{"Success": "Avatar updated."})
}

// applyAvatarForm applies the avatar upload/remove fields for userID,
// deleting a replaced file. The caller has parsed the form. Returns a
// user-facing error.
func (s *Server) applyAvatarForm(r *http.Request, userID int64) error {
	target, err := s.Users.GetByID(userID)
	if err != nil {
		return errors.New("User not found.")
	}
	old := target.AvatarUploadPath.String
	if file, _, err := r.FormFile("avatar_upload"); err == nil {
		defer file.Close()
		saved, err := saveUploadedImage(s.Cfg.UploadsDir, "avatars", file)
		if err != nil {
			return errors.New("Could not save the uploaded avatar: " + err.Error())
		}
		if err := s.Users.SetAvatar(userID, sql.NullString{String: saved, Valid: true}); err != nil {
			removeUpload(s.Cfg.UploadsDir, saved)
			return errors.New("Could not save the avatar.")
		}
		removeUpload(s.Cfg.UploadsDir, old)
	} else if r.FormValue("remove_avatar") == "on" {
		if err := s.Users.SetAvatar(userID, sql.NullString{}); err != nil {
			return errors.New("Could not remove the avatar.")
		}
		removeUpload(s.Cfg.UploadsDir, old)
	}
	return nil
}

func (s *Server) HandleAccountChangePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	current := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	formErr := func(msg string) {
		s.renderAccount(w, r, map[string]any{"Error": msg})
	}

	if !auth.CheckPassword(u.PasswordHash, current) {
		formErr("Current password is incorrect.")
		return
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
	if err := s.Users.UpdatePasswordHash(u.ID, hash, currentSessionID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.renderAccount(w, r, map[string]any{"Success": "Password updated."})
}
