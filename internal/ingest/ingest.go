// Package ingest implements the auto-ingest poller: per-source mode,
// exponential backoff on failure, dedup by original URL, and a baseline so
// a new source only ingests posts made after it was set up.
package ingest

import (
	"context"
	"database/sql"
	"log"
	"math"
	"time"
	"unicode/utf8"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
	"fedigest/internal/slug"
)

// maxIntervalSec caps backoff, unless a source's own interval is longer.
const maxIntervalSec = 3600

// Poller periodically walks every non-manual source and, for each one that
// is due, fetches recent posts and turns unseen ones into picks.
type Poller struct {
	Sources  *models.SourceStore
	Picks    *models.PickStore
	Adapters *adapters.Registry
	Interval time.Duration // how often the poller wakes up to check what's due
	Now      func() time.Time
	// Resolver finds each new pick's community-instance URL; nil skips it.
	Resolver *adapters.InstanceResolver
	// SettleDelay: a post younger than this is left for a later poll, since
	// Lemmy/PieFed fetch link thumbnails in the background after creation.
	SettleDelay time.Duration
	// MultiCategory reports whether picks get a source's extra categories,
	// and whether a post found again by another source gets that source's
	// categories added; nil means on.
	MultiCategory func() bool
	// NewPick, if set, is called with each published pick the poller
	// creates, so its comment count is fetched straight away instead of
	// waiting for the background refresh.
	NewPick func(id int64)
}

func NewPoller(sources *models.SourceStore, picks *models.PickStore, reg *adapters.Registry) *Poller {
	return &Poller{
		Sources:     sources,
		Picks:       picks,
		Adapters:    reg,
		Resolver:    adapters.DefaultResolver,
		SettleDelay: 10 * time.Minute,
		Interval:    time.Minute,
		Now:         time.Now,
	}
}

// Run polls due sources every p.Interval until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	sources, err := p.Sources.ListPollable()
	if err != nil {
		log.Printf("ingest: listing pollable sources: %v", err)
		return
	}
	for _, src := range sources {
		if !p.due(src) {
			continue
		}
		p.pollOne(ctx, src)
	}
}

// due reports whether src should be polled now: interval =
// poll_interval_sec * 2^consecutive_failures, capped at
// max(poll_interval_sec, maxIntervalSec). poll_interval_sec is kept
// between a minute and a day.
func (p *Poller) due(src *models.Source) bool {
	if !src.LastPolledAt.Valid {
		return true
	}
	last, err := time.Parse(time.RFC3339, normalizeTimestamp(src.LastPolledAt.String))
	if err != nil {
		return true
	}

	interval := min(max(src.PollIntervalSec, 60), 86400)
	capSec := max(interval, maxIntervalSec)
	effective := float64(interval) * math.Pow(2, float64(src.ConsecutiveFailures))
	if effective > float64(capSec) {
		effective = float64(capSec)
	}

	return p.Now().Sub(last) >= time.Duration(effective)*time.Second
}

// normalizeTimestamp converts SQLite's datetime('now') format to RFC3339.
func normalizeTimestamp(s string) string {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return s
	}
	return t.Format(time.RFC3339)
}

// sourceBaseline parses src.CreatedAt; ok is false if it doesn't parse.
func sourceBaseline(src *models.Source) (t time.Time, ok bool) {
	if src.IngestSince.Valid {
		if t, err := time.Parse(time.RFC3339, src.IngestSince.String); err == nil {
			return t, true
		}
	}
	t, err := time.Parse(time.RFC3339, normalizeTimestamp(src.CreatedAt))
	return t, err == nil
}

func (p *Poller) pollOne(ctx context.Context, src *models.Source) {
	if !src.DefaultCategoryID.Valid || !src.CreatedByUserID.Valid {
		// The admin UI should prevent this; guard anyway.
		log.Printf("ingest: source %d has no default category/curator, skipping", src.ID)
		return
	}

	adapter, err := p.Adapters.For(src.Platform)
	if err != nil {
		log.Printf("ingest: source %d: %v", src.ID, err)
		if markErr := p.Sources.MarkPollFailure(src.ID); markErr != nil {
			log.Printf("ingest: source %d: recording failure: %v", src.ID, markErr)
		}
		return
	}

	items, err := adapter.Poll(ctx, *src)
	if limited, _ := adapters.RateLimit(err); limited {
		// The instance asked us to slow down: its pause holds every request
		// to it (adapters/pause.go), and this isn't the source failing.
		if !adapters.NothingSent(err) {
			log.Printf("ingest: source %d: instance asked to slow down: %v", src.ID, err)
		}
		return
	}
	if err != nil {
		log.Printf("ingest: source %d: poll failed: %v", src.ID, err)
		if markErr := p.Sources.MarkPollFailure(src.ID); markErr != nil {
			log.Printf("ingest: source %d: recording failure: %v", src.ID, markErr)
		}
		return
	}

	// Poll returns the N newest items with no "since" cursor, so items
	// older than the source itself are skipped as pre-existing history.
	baseline, ok := sourceBaseline(src)

	for _, item := range items {
		if item.CommentURL == "" {
			continue
		}
		if ok && !item.CreatedAt.IsZero() && item.CreatedAt.Before(baseline) {
			continue
		}
		if p.SettleDelay > 0 && !item.CreatedAt.IsZero() && p.Now().Sub(item.CreatedAt) < p.SettleDelay {
			continue // see SettleDelay
		}
		if src.Excludes(item.CommunityHandle) {
			continue
		}
		// Skip posts already picked, from any source or by hand, and links
		// already picked from another community.
		dups, err := p.Picks.FindDuplicates(0, item.CommentURL, item.CanonicalID(), item.LinkURL)
		if err != nil {
			log.Printf("ingest: source %d: dedup check: %v", src.ID, err)
			continue
		}
		if len(dups) > 0 {
			if p.multi() {
				cats := p.sourceCategories(src)
				for _, d := range dups {
					if err := p.Picks.AddCategories(d.ID, cats); err != nil {
						log.Printf("ingest: source %d: adding categories to pick %d: %v", src.ID, d.ID, err)
					}
				}
			}
			continue
		}
		if err := p.createPick(src, item); err != nil {
			log.Printf("ingest: source %d: creating pick: %v", src.ID, err)
		}
	}

	if err := p.Sources.MarkPollSuccess(src.ID); err != nil {
		log.Printf("ingest: source %d: recording success: %v", src.ID, err)
	}
}

// createPick turns one fetched item into a pick with the original body
// shown via use_original_as_body: a draft for auto-review, published for
// auto-publish.
func (p *Poller) createPick(src *models.Source, item adapters.Metadata) error {
	// The stored excerpt is what the edit form shows; cards work it out
	// from the original at display time (Pick.DisplayExcerpt).
	excerpt := models.LeadParagraphs(item.Body)
	if excerpt == "" {
		excerpt = item.Title
	}
	if excerpt == item.Title && len(excerpt) > 300 {
		cut := 300
		for cut > 0 && !utf8.RuneStart(excerpt[cut]) {
			cut-- // don't split a character
		}
		excerpt = excerpt[:cut]
	}

	pk := &models.Pick{
		Title:             item.Title,
		Slug:              slug.Slugify(item.Title),
		Excerpt:           excerpt,
		ExcerptAuto:       true, // cards show the original's opening (Pick.DisplayExcerpt)
		UseOriginalAsBody: true,
		OriginalBodyCache: sql.NullString{String: item.Body, Valid: item.Body != ""},
		CategoryID:        src.DefaultCategoryID.Int64,
		CuratorID:         src.CreatedByUserID.Int64,
		SourceType:        models.SourceTypeFedi,
		SourceID:          sql.NullInt64{Int64: src.ID, Valid: true},
		AutoIngested:      true,
		SourcePlatform:    sql.NullString{String: string(src.Platform), Valid: true},
		OriginalURL:       sql.NullString{String: item.CommentURL, Valid: true},
		OriginalAuthor:    sql.NullString{String: item.AuthorHandle, Valid: item.AuthorHandle != ""},
		OriginalCommunity: sql.NullString{String: item.CommunityHandle, Valid: item.CommunityHandle != ""},
		LinkURL:           sql.NullString{String: item.LinkURL, Valid: item.LinkURL != ""},
		OriginalApID:      sql.NullString{String: item.CanonicalID(), Valid: item.CanonicalID() != ""},
		ImageURL:          sql.NullString{String: item.ImageURL, Valid: item.ImageURL != ""},
	}
	if p.Resolver != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if u := p.Resolver.CommunityPostURL(ctx, item.CommunityHandle, item.CanonicalID()); u != "" {
			pk.CommunityPostURL = sql.NullString{String: u, Valid: true}
		}
		cancel()
	}
	if !item.CreatedAt.IsZero() {
		pk.OriginalPublishedAt = sql.NullString{String: item.CreatedAt.UTC().Format(time.RFC3339), Valid: true}
	}

	switch src.Mode {
	case models.SourceModeAutoPublish:
		pk.Status = models.PickStatusPublished
		pk.PublishedAt = sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}
	default: // auto-review
		pk.Status = models.PickStatusDraft
	}

	id, err := p.Picks.Create(pk)
	if err != nil {
		return err
	}
	if p.multi() {
		if err := p.Picks.AddCategories(id, p.sourceCategories(src)); err != nil {
			return err
		}
	}
	if pk.Status == models.PickStatusPublished && p.NewPick != nil {
		p.NewPick(id)
	}
	return nil
}

func (p *Poller) multi() bool {
	return p.MultiCategory == nil || p.MultiCategory()
}

// sourceCategories is a source's main category plus its extra ones.
func (p *Poller) sourceCategories(src *models.Source) []int64 {
	cats := []int64{}
	if src.DefaultCategoryID.Valid {
		cats = append(cats, src.DefaultCategoryID.Int64)
	}
	extra, err := p.Sources.ExtraCategories(src.ID)
	if err != nil {
		log.Printf("ingest: source %d: extra categories: %v", src.ID, err)
	}
	return append(cats, extra...)
}
