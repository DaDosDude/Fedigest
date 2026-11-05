package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"fedigest/internal/auth"
	"fedigest/internal/models"
)

// validUsername: usernames appear in /author/<username> links, so no
// spaces, slashes, "?" or "#".
var validUsername = regexp.MustCompile(`^[^\s/\\?#%]{1,64}$`)

const usernameRule = "Usernames can't contain spaces or the characters / \\ ? # %, and can be at most 64 characters."

func (s *Server) HandleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	users, err := s.Users.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := map[string]any{"Users": users, "Me": auth.UserFromContext(r.Context())}
	q := r.URL.Query()
	if n := q.Get("deleted"); n != "" {
		data["Notice"] = "User deleted. Their picks and sources are now under your account, and their picks are drafts (" + n + " had been published or scheduled)."
	}
	if name := q.Get("reset"); name != "" {
		data["Notice"] = "Password changed for " + name + ". They've been logged out everywhere else."
	}
	if q.Get("error") == "empty-password" {
		data["Error"] = "Type a new password before pressing Reset password."
	}
	s.render(w, "admin_users.html", data)
}

// HandleAdminUserDelete deletes an account, unpublishing its picks and
// reassigning them and its sources to the acting admin (see
// UserStore.Delete). An admin can't delete themselves or the last admin.
func (s *Server) HandleAdminUserDelete(w http.ResponseWriter, r *http.Request, userID int64) {
	me := auth.UserFromContext(r.Context())
	target, err := s.Users.GetByID(userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if target.ID == me.ID {
		http.Error(w, "you can't delete your own account", http.StatusBadRequest)
		return
	}
	if target.Role == models.RoleAdmin {
		if n, err := s.Users.CountAdmins(); err != nil || n <= 1 {
			http.Error(w, "can't delete the last admin", http.StatusBadRequest)
			return
		}
	}
	unpublished, err := s.Users.Delete(target.ID, me.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	removeUpload(s.Cfg.UploadsDir, target.AvatarUploadPath.String)
	http.Redirect(w, r, "/admin/users?deleted="+strconv.Itoa(unpublished), http.StatusFound)
}

func (s *Server) HandleAdminUserNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "admin_user_form.html", map[string]any{"IsNew": true})
}

// HandleAdminUserCreate creates an account; there is no public registration.
func (s *Server) HandleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	if !parseUploadForm(w, r, maxPickFormBytes) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	role := models.Role(r.FormValue("role"))

	formErr := func(msg string) {
		s.render(w, "admin_user_form.html", map[string]any{
			"IsNew":       true,
			"Error":       msg,
			"Username":    username,
			"DisplayName": displayName,
			"Email":       email,
			"Role":        string(role),
		})
	}

	if username == "" || password == "" {
		formErr("Username and password are required.")
		return
	}
	if !validUsername.MatchString(username) {
		formErr(usernameRule)
		return
	}
	if role != models.RoleAdmin && role != models.RoleUser {
		formErr("Choose a role.")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}

	u := &models.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
	}
	if displayName != "" {
		u.DisplayName = sql.NullString{String: displayName, Valid: true}
	}
	if email != "" {
		u.Email = sql.NullString{String: email, Valid: true}
	}

	if _, err := s.Users.Create(u); err != nil {
		formErr("Could not create user — username may already be taken.")
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// HandleAdminUserResetPassword lets an admin set a user's password, the
// recovery path for accounts without email.
func (s *Server) HandleAdminUserResetPassword(w http.ResponseWriter, r *http.Request, userID int64) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	target, err := s.Users.GetByID(userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	newPassword := r.FormValue("new_password")
	if newPassword == "" {
		http.Redirect(w, r, "/admin/users?error=empty-password", http.StatusFound)
		return
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		s.serverError(w, err)
		return
	}
	keep := ""
	if userID == auth.UserFromContext(r.Context()).ID {
		keep = currentSessionID(r)
	}
	if err := s.Users.UpdatePasswordHash(userID, hash, keep); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/users?reset="+url.QueryEscape(target.Username), http.StatusFound)
}

// HandleAdminUserEditForm shows the form for editing an account's
// username, display name, email and role.
func (s *Server) HandleAdminUserEditForm(w http.ResponseWriter, r *http.Request, userID int64) {
	u, err := s.Users.GetByID(userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "admin_user_form.html", map[string]any{
		"IsNew":       false,
		"User":        u,
		"IsSelf":      u.ID == auth.UserFromContext(r.Context()).ID,
		"Username":    u.Username,
		"DisplayName": u.DisplayName.String,
		"Email":       u.Email.String,
		"Role":        string(u.Role),
	})
}

func (s *Server) HandleAdminUserUpdate(w http.ResponseWriter, r *http.Request, userID int64) {
	u, err := s.Users.GetByID(userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !parseUploadForm(w, r, maxPickFormBytes) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	email := strings.TrimSpace(r.FormValue("email"))
	role := models.Role(r.FormValue("role"))

	formErr := func(msg string) {
		s.render(w, "admin_user_form.html", map[string]any{
			"IsNew":       false,
			"User":        u,
			"IsSelf":      u.ID == auth.UserFromContext(r.Context()).ID,
			"Error":       msg,
			"Username":    username,
			"DisplayName": displayName,
			"Email":       email,
			"Role":        string(role),
		})
	}

	if username == "" {
		formErr("Username is required.")
		return
	}
	if username != u.Username && !validUsername.MatchString(username) {
		formErr(usernameRule)
		return
	}
	if role != models.RoleAdmin && role != models.RoleUser {
		formErr("Choose a role.")
		return
	}
	if u.Role == models.RoleAdmin && role != models.RoleAdmin {
		if n, err := s.Users.CountAdmins(); err != nil || n <= 1 {
			formErr("This is the only admin account, so it has to stay an admin.")
			return
		}
	}

	u.Username = username
	u.DisplayName = sql.NullString{String: displayName, Valid: displayName != ""}
	u.Email = sql.NullString{String: email, Valid: email != ""}
	u.Role = role

	if err := s.Users.Update(u); err != nil {
		formErr("Could not save — that username may already be taken.")
		return
	}
	if err := s.applyAvatarForm(r, u.ID); err != nil {
		formErr(err.Error())
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}
