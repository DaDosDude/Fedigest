package web

import (
	"database/sql"
	"testing"
	"time"

	"fedigest/internal/models"
)

func TestSortPickRows(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	mk := func(title, cat string, d int, pinned bool) adminPickRow {
		return adminPickRow{Pick: &models.Pick{Title: title, PinnedAt: sql.NullString{Valid: pinned}}, Category: cat, when: day(d)}
	}
	rows := []adminPickRow{mk("beta", "News", 1, false), mk("Alpha", "Retro", 3, true), mk("gamma", "News", 2, false)}
	titles := func() string {
		s := ""
		for _, r := range rows {
			s += r.Title + ","
		}
		return s
	}

	sortPickRows(rows, "date", true)
	if got := titles(); got != "Alpha,gamma,beta," {
		t.Errorf("newest first: %s", got)
	}
	sortPickRows(rows, "title", false)
	if got := titles(); got != "Alpha,beta,gamma," {
		t.Errorf("title A-Z (case-insensitive): %s", got)
	}
}
