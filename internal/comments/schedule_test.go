package comments

import (
	"database/sql"
	"testing"
	"time"

	"fedigest/internal/models"
)

func TestCountDue(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) sql.NullString {
		return sql.NullString{String: now.Add(-d).Format(time.RFC3339), Valid: true}
	}
	sqlite := func(d time.Duration) sql.NullString {
		return sql.NullString{String: now.Add(-d).Format("2006-01-02 15:04:05"), Valid: true}
	}
	for _, tc := range []struct {
		name  string
		p     models.Pick
		tried time.Duration // ago; 0 = never
		want  bool
	}{
		{"new pick, just counted", models.Pick{PublishedAt: ts(time.Hour), CommentCountUpdatedAt: sqlite(time.Minute)}, 0, true},
		{"pinned old pick", models.Pick{PublishedAt: ts(20 * 24 * time.Hour), PinnedAt: ts(time.Hour), CommentCountUpdatedAt: sqlite(time.Minute)}, 0, true},
		{"3 days, counted 20 min ago", models.Pick{PublishedAt: ts(72 * time.Hour), CommentCountUpdatedAt: sqlite(20 * time.Minute)}, 0, false},
		{"3 days, counted 59 min ago", models.Pick{PublishedAt: ts(72 * time.Hour), CommentCountUpdatedAt: sqlite(59 * time.Minute)}, 0, true},
		{"3 days, never counted", models.Pick{PublishedAt: ts(72 * time.Hour)}, 0, true},
		{"3 days, tried 10 min ago", models.Pick{PublishedAt: ts(72 * time.Hour)}, 10 * time.Minute, false},
		{"20 days, counted 2 hours ago", models.Pick{PublishedAt: ts(20 * 24 * time.Hour), CommentCountUpdatedAt: sqlite(2 * time.Hour)}, 0, false},
		{"20 days, counted 6 hours ago", models.Pick{PublishedAt: ts(20 * 24 * time.Hour), CommentCountUpdatedAt: sqlite(6 * time.Hour)}, 0, true},
	} {
		tried := time.Time{}
		if tc.tried > 0 {
			tried = now.Add(-tc.tried)
		}
		if got := countDue(&tc.p, now, tried); got != tc.want {
			t.Errorf("%s: due = %v", tc.name, got)
		}
	}
}
