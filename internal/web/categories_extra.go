package web

import (
	"net/http"
	"strconv"

	"fedigest/internal/models"
)

// formExtraCategories reads the "extra_category" checkboxes.
func formExtraCategories(r *http.Request) []int64 {
	var out []int64
	for _, v := range r.Form["extra_category"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func idSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// categoriesByID returns the given categories in order, skipping unknown IDs.
func categoriesByID(all map[int64]*models.Category, ids []int64) []*models.Category {
	out := make([]*models.Category, 0, len(ids))
	for _, id := range ids {
		if c := all[id]; c != nil {
			out = append(out, c)
		}
	}
	return out
}
