package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"fedigest/internal/models"
	"fedigest/internal/slug"
)

func (s *Server) HandleAdminCategoriesList(w http.ResponseWriter, r *http.Request) {
	cats, err := s.Categories.List()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "admin_categories.html", map[string]any{"Categories": cats})
}

// HandleAdminCategoriesReorder saves the drag-and-drop order; ids is a
// comma-separated list of category IDs.
func (s *Server) HandleAdminCategoriesReorder(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for _, part := range strings.Split(r.FormValue("ids"), ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			http.Error(w, "bad ids", http.StatusBadRequest)
			return
		}
		ids = append(ids, id)
	}
	if err := s.Categories.Reorder(ids); err != nil {
		s.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// categorySlug turns the slug field (or, when blank, the name) into a
// URL-safe slug, so a typed "Games/Retro" can't break the category's links.
func categorySlug(typed, name string) string {
	if strings.TrimSpace(typed) == "" {
		typed = name
	}
	return slug.Slugify(typed)
}

func (s *Server) HandleAdminCategoryNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "admin_category_form.html", map[string]any{"IsNew": true, "ShowOnFrontpage": true})
}

func (s *Server) HandleAdminCategoryCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	catSlug := categorySlug(r.FormValue("slug"), name)
	showOnFrontpage := r.FormValue("show_on_frontpage") == "on"
	// Blank puts the new category last.
	sortOrder, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
	if err != nil {
		if sortOrder, err = s.Categories.NextSortOrder(); err != nil {
			s.serverError(w, err)
			return
		}
	}
	formErr := func(msg string) {
		s.render(w, "admin_category_form.html", map[string]any{
			"IsNew":           true,
			"Error":           msg,
			"Name":            name,
			"Slug":            r.FormValue("slug"),
			"SortOrder":       r.FormValue("sort_order"),
			"ShowOnFrontpage": showOnFrontpage,
		})
	}

	if name == "" {
		formErr("Name is required.")
		return
	}
	if _, err := s.Categories.Create(name, catSlug, sortOrder, showOnFrontpage); err != nil {
		formErr("Could not create the category: the slug \"" + catSlug + "\" is already in use.")
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusFound)
}

func (s *Server) HandleAdminCategoryEditForm(w http.ResponseWriter, r *http.Request, id int64) {
	cat, err := s.Categories.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "admin_category_form.html", map[string]any{
		"IsNew":           false,
		"Category":        cat,
		"Name":            cat.Name,
		"Slug":            cat.Slug,
		"SortOrder":       strconv.Itoa(cat.SortOrder),
		"ShowOnFrontpage": cat.ShowOnFrontpage,
	})
}

func (s *Server) HandleAdminCategoryUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	cat, err := s.Categories.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	catSlug := cat.Slug // an unchanged slug is kept as it is, so links keep working
	if typed := strings.TrimSpace(r.FormValue("slug")); typed != cat.Slug {
		catSlug = categorySlug(typed, name)
	}
	sortOrder, err := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
	if err != nil {
		sortOrder = cat.SortOrder
	}
	showOnFrontpage := r.FormValue("show_on_frontpage") == "on"

	formErr := func(msg string) {
		s.render(w, "admin_category_form.html", map[string]any{
			"IsNew":           false,
			"Category":        cat,
			"Error":           msg,
			"Name":            name,
			"Slug":            r.FormValue("slug"),
			"SortOrder":       r.FormValue("sort_order"),
			"ShowOnFrontpage": showOnFrontpage,
		})
	}

	if name == "" {
		formErr("Name is required.")
		return
	}
	if err := s.Categories.Update(id, name, catSlug, sortOrder, showOnFrontpage); err != nil {
		formErr("Could not save the category: the slug \"" + catSlug + "\" is already in use.")
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusFound)
}

// HandleAdminCategoryDelete deletes a category. If sources still post into
// it, a confirmation page is shown first, which posts back with
// delete_sources=1.
func (s *Server) HandleAdminCategoryDelete(w http.ResponseWriter, r *http.Request, id int64) {
	cat, err := s.Categories.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	deleteSources := r.FormValue("delete_sources") == "1"
	if err := s.Categories.Delete(id, deleteSources); err != nil {
		switch {
		case errors.Is(err, models.ErrCategoryInUse):
			cats, _ := s.Categories.List()
			s.render(w, "admin_categories.html", map[string]any{
				"Categories": cats,
				"Error":      "Can't delete a category that still has picks assigned to it — recategorize or delete those picks first.",
			})
		case errors.Is(err, models.ErrCategoryHasSources):
			sources, lerr := s.Sources.ListByCategory(id)
			if lerr != nil {
				s.serverError(w, lerr)
				return
			}
			s.render(w, "admin_category_delete.html", map[string]any{
				"Category": cat,
				"Sources":  sources,
			})
		default:
			s.serverError(w, err)
		}
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusFound)
}
