package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fedigest/internal/models"
)

// TestHostileURLsAreNeutralised checks that links and images from a
// curator or a remote post can't run script.
func TestHostileURLsAreNeutralised(t *testing.T) {
	ts := newTestSite(t)
	admin, _ := ts.srv.Users.GetByUsername("admin")
	id, err := ts.srv.Picks.Create(&models.Pick{
		Slug: "x", Title: "X", Excerpt: "[click](javascript:alert(1)) ![i](javascript:alert(2))",
		Body:       "<script>alert(3)</script> [a](javascript:alert(4))",
		CategoryID: ts.catID, CuratorID: admin.ID, SourceType: models.SourceTypeFedi,
		SourcePlatform:    sql.NullString{String: "lemmy", Valid: true},
		OriginalURL:       sql.NullString{String: "javascript://%0Aalert(5)", Valid: true},
		LinkURL:           sql.NullString{String: "javascript://%0Aalert(6)", Valid: true},
		ImageURL:          sql.NullString{String: "javascript://%0Aalert(7)", Valid: true},
		OriginalAuthor:    sql.NullString{String: `<img src=x onerror=alert(8)>`, Valid: true},
		Status:            models.PickStatusPublished,
		PublishedAt:       sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true},
		OriginalBodyCache: sql.NullString{String: "<iframe src=x></iframe>", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ts.client("")
	for _, path := range []string{"/", fmt.Sprintf("/p/%d-x", id), "/feed.xml"} {
		body := ts.checkPage(c, path, http.StatusOK)
		for _, bad := range []string{`href="javascript:`, `src="javascript:`, "<script>alert", "<iframe", "<img src=x onerror"} {
			if i := strings.Index(body, bad); i >= 0 {
				t.Errorf("%s contains %q: ...%s...", path, bad, body[max(0, i-80):min(len(body), i+40)])
			}
		}
	}
}
