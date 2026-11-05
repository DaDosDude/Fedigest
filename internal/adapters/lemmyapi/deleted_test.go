package lemmyapi

import (
	"strings"
	"testing"
	"time"

	"fedigest/internal/adapters"
)

func TestDeletedAndRemovedCommentsHideTheirText(t *testing.T) {
	flat := []commentView{
		{Comment: commentFields{ID: 1, Content: "secret spam", Path: "0.1", Removed: true}, Creator: personView{Name: "spammer"}},
		{Comment: commentFields{ID: 2, Content: "a reply", Path: "0.1.2"}, Creator: personView{Name: "alice"}},
		{Comment: commentFields{ID: 3, Content: "regret", Path: "0.3", Deleted: true}, Creator: personView{Name: "bob"}},
	}
	roots := buildCommentTree(flat)
	if len(roots) != 2 {
		t.Fatalf("%d roots", len(roots))
	}
	removed, deleted := roots[0], roots[1]
	if strings.Contains(removed.Body, "spam") || removed.Author != "" || !strings.Contains(removed.Body, "Removed") {
		t.Errorf("removed comment shown as %q by %q", removed.Body, removed.Author)
	}
	if len(removed.Children) != 1 || removed.Children[0].Body != "a reply" {
		t.Error("a removed comment's replies should stay in place")
	}
	if strings.Contains(deleted.Body, "regret") || deleted.Author != "" {
		t.Errorf("deleted comment shown as %q by %q", deleted.Body, deleted.Author)
	}
}

func TestParseTimeWithoutZone(t *testing.T) {
	got, ok := adapters.ParseTime("2023-06-21T18:36:57.528929")
	if !ok || !got.Equal(time.Date(2023, 6, 21, 18, 36, 57, 528929000, time.UTC)) {
		t.Errorf("got %v, %v", got, ok)
	}
}
