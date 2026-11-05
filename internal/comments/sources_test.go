package comments

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

// urlAdapter answers only for the URLs in its map, and records every URL
// it was asked for — stands in for one platform's API.
type urlAdapter struct {
	answers map[string]adapters.CommentTree
	asked   []string
}

func (u *urlAdapter) FetchMetadata(ctx context.Context, postURL string) (adapters.Metadata, error) {
	return adapters.Metadata{}, nil
}
func (u *urlAdapter) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	u.asked = append(u.asked, postURL)
	if t, ok := u.answers[postURL]; ok {
		return t, nil
	}
	return adapters.CommentTree{}, errors.New("not found")
}
func (u *urlAdapter) FetchCommentCount(ctx context.Context, postURL string) (int, error) {
	t, err := u.FetchComments(ctx, postURL)
	return t.Total, err
}
func (u *urlAdapter) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	return nil, nil
}

func TestCommentsPreferCommunityInstance(t *testing.T) {
	conn := setupTestDB(t)
	picks := &models.PickStore{DB: conn}
	pick := makeFediPick(t, conn)
	// Created from a piefed.social link; the community lives on a PieFed
	// instance too, so the Lemmy adapter must fail there and PieFed answer.
	pick.SourcePlatform = sql.NullString{String: "piefed", Valid: true}
	pick.OriginalURL = sql.NullString{String: "https://piefed.social/post/1", Valid: true}
	pick.CommunityPostURL = sql.NullString{String: "https://community.example/post/99", Valid: true}

	full := adapters.CommentTree{Total: 3, Comments: []adapters.Comment{{Author: "a"}, {Author: "b"}, {Author: "blocked-elsewhere"}}}
	partial := adapters.CommentTree{Total: 2, Comments: []adapters.Comment{{Author: "a"}, {Author: "b"}}}
	lemmy := &urlAdapter{answers: map[string]adapters.CommentTree{}}
	piefed := &urlAdapter{answers: map[string]adapters.CommentTree{
		"https://community.example/post/99": full,
		"https://piefed.social/post/1":      partial,
	}}
	reg := adapters.NewRegistry()
	reg.Register(models.PlatformLemmy, lemmy)
	reg.Register(models.PlatformPieFed, piefed)
	svc := New(picks, reg, time.Minute)

	if r := svc.GetComments(context.Background(), pick); r.Total != 3 {
		t.Fatalf("Total = %d, want 3 (from the community instance)", r.Total)
	}
	if len(lemmy.asked) != 1 || lemmy.asked[0] != "https://community.example/post/99" {
		t.Errorf("lemmy asked %v", lemmy.asked)
	}

	// Second fetch: remembers the community host runs PieFed, so Lemmy
	// isn't tried again.
	lemmy.asked = nil
	if err := svc.RefreshCommentCount(context.Background(), pick); err != nil {
		t.Fatal(err)
	}
	if len(lemmy.asked) != 0 {
		t.Errorf("lemmy should not be retried for a known PieFed host, asked %v", lemmy.asked)
	}

	// Community instance down: falls back to the original link.
	delete(piefed.answers, "https://community.example/post/99")
	fresh, _ := picks.GetByID(pick.ID)
	fresh.CommunityPostURL = pick.CommunityPostURL
	fresh.SourcePlatform = pick.SourcePlatform
	fresh.OriginalURL = pick.OriginalURL
	fresh.CommentsCacheUpdatedAt = sql.NullString{} // force a live fetch
	if r := svc.GetComments(context.Background(), fresh); r.Total != 2 || r.Stale {
		t.Errorf("fallback: Total=%d Stale=%v, want 2 from the original link", r.Total, r.Stale)
	}
}
