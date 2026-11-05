package web

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) HandleAdminSidebarList(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.Sidebar.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "admin_sidebar.html", map[string]any{"Blocks": blocks})
}

func (s *Server) HandleAdminSidebarNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "admin_sidebar_form.html", map[string]any{
		"IsNew":     true,
		"Title":     "",
		"ContentMD": "",
		"SortOrder": 0,
		"Enabled":   true,
	})
}

func (s *Server) HandleAdminSidebarEditForm(w http.ResponseWriter, r *http.Request, id int64) {
	block, err := s.Sidebar.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "admin_sidebar_form.html", map[string]any{
		"IsNew":     false,
		"Block":     block,
		"Title":     block.Title,
		"ContentMD": block.ContentMD,
		"SortOrder": block.SortOrder,
		"Enabled":   block.Enabled,
	})
}

func (s *Server) HandleAdminSidebarCreate(w http.ResponseWriter, r *http.Request) {
	s.saveSidebarBlockFromForm(w, r, nil)
}

func (s *Server) HandleAdminSidebarUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := s.Sidebar.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	s.saveSidebarBlockFromForm(w, r, &id)
}

func (s *Server) saveSidebarBlockFromForm(w http.ResponseWriter, r *http.Request, id *int64) {
	if err := r.ParseMultipartForm(1 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	contentMD := r.FormValue("content_md")
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	enabled := r.FormValue("enabled") == "on"

	formErr := func(msg string) {
		data := map[string]any{
			"IsNew":     id == nil,
			"Error":     msg,
			"Title":     title,
			"ContentMD": contentMD,
			"SortOrder": sortOrder,
			"Enabled":   enabled,
		}
		if id != nil {
			data["Block"] = &struct{ ID int64 }{ID: *id}
		}
		s.render(w, "admin_sidebar_form.html", data)
	}

	if title == "" {
		formErr("Title is required.")
		return
	}

	var err error
	if id == nil {
		_, err = s.Sidebar.Create(title, contentMD, sortOrder, enabled)
	} else {
		err = s.Sidebar.Update(*id, title, contentMD, sortOrder, enabled)
	}
	if err != nil {
		formErr("Could not save sidebar block: " + err.Error())
		return
	}
	http.Redirect(w, r, "/admin/sidebar", http.StatusFound)
}

func (s *Server) HandleAdminSidebarDelete(w http.ResponseWriter, r *http.Request, id int64) {
	if err := s.Sidebar.Delete(id); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/sidebar", http.StatusFound)
}
