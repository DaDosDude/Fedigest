// Package models defines Fedigest's domain types and their SQLite stores.
package models

import (
	"database/sql"
	"net/url"
	"strings"
	"time"
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

type SourceType string

const (
	SourceTypeFedi  SourceType = "fedi"
	SourceTypeLocal SourceType = "local"
)

type Platform string

const (
	PlatformLemmy  Platform = "lemmy"
	PlatformPieFed Platform = "piefed"
	PlatformMbin   Platform = "mbin"
)

type PickStatus string

const (
	PickStatusDraft     PickStatus = "draft"
	PickStatusPublished PickStatus = "published"
)

type SourceMode string

const (
	SourceModeManual      SourceMode = "manual"
	SourceModeAutoReview  SourceMode = "auto-review"
	SourceModeAutoPublish SourceMode = "auto-publish"
)

// WatchType says what Source.WatchTarget names.
type WatchType string

const (
	WatchTypeCommunity WatchType = "community"
	WatchTypePerson    WatchType = "person"
	WatchTypeInstance  WatchType = "instance" // the instance's Local feed; WatchTarget unused
)

type User struct {
	ID               int64
	Username         string
	DisplayName      sql.NullString
	Email            sql.NullString
	PasswordHash     string
	Role             Role
	AvatarUploadPath sql.NullString
	CreatedAt        string
	UpdatedAt        string
}

// DisplayNameOrUsername returns DisplayName if set, else Username.
func (u *User) DisplayNameOrUsername() string {
	if u.DisplayName.Valid && u.DisplayName.String != "" {
		return u.DisplayName.String
	}
	return u.Username
}

type Category struct {
	ID        int64
	Name      string
	Slug      string
	SortOrder int
	// ShowOnFrontpage: when false, this category's picks are left off the
	// frontpage and main feed.
	ShowOnFrontpage bool
	CreatedAt       string
}

type Source struct {
	ID                  int64
	Platform            Platform
	InstanceURL         string
	WatchTarget         sql.NullString
	WatchType           WatchType
	AuthToken           sql.NullString
	Mode                SourceMode
	PollIntervalSec     int
	LastPolledAt        sql.NullString
	ConsecutiveFailures int
	SourceUnavailable   bool
	DefaultCategoryID   sql.NullInt64
	CreatedByUserID     sql.NullInt64
	CreatedAt           string
	IngestSince         sql.NullString // UTC RFC3339; posts older than this are skipped (default: CreatedAt)
	Name                sql.NullString // optional display name in the admin
	// ExcludeCommunities: communities whose posts this source skips, one
	// per line ("name" matches on any instance, "name@instance" only there).
	ExcludeCommunities sql.NullString
}

type Pick struct {
	ID      int64
	Slug    string
	Title   string
	Excerpt string
	// ExcerptAuto: the excerpt was filled in automatically and never edited,
	// so the full body is shown in its place (see DisplayExcerpt).
	ExcerptAuto       bool
	Body              string
	UseOriginalAsBody bool
	OriginalBodyCache sql.NullString
	CategoryID        int64
	CuratorID         int64

	SourceType SourceType
	SourceID   sql.NullInt64
	// AutoIngested drives the auto-post byline; kept when the source is deleted.
	AutoIngested   bool
	SourcePlatform sql.NullString
	OriginalURL    sql.NullString

	// OriginalAuthor/OriginalCommunity are "name@instance" (fedi picks only).
	OriginalAuthor    sql.NullString
	OriginalCommunity sql.NullString
	// LinkURL is the external article a link post points at.
	LinkURL sql.NullString
	// OriginalPublishedAt is when the original post was published (RFC3339).
	OriginalPublishedAt sql.NullString
	// OriginalApID is the post's canonical ActivityPub ID.
	OriginalApID sql.NullString
	// CommunityPostURL is the post on its community's home instance, the
	// preferred "View original" target.
	CommunityPostURL sql.NullString

	ImageURL        sql.NullString
	ImageUploadPath sql.NullString
	AltText         sql.NullString

	Status      PickStatus
	PublishedAt sql.NullString

	CommentCount           sql.NullInt64
	CommentCountUpdatedAt  sql.NullString
	CommentsCache          sql.NullString
	CommentsCacheUpdatedAt sql.NullString
	ConsecutiveFailures    int
	SourceUnavailable      bool
	PinnedAt               sql.NullString // set = pinned
	PublishAt              sql.NullString // scheduled publish time, UTC RFC3339 (drafts only)

	CreatedAt string
	UpdatedAt string
}

// DisplayImage returns the image URL to render; an upload overrides a
// linked image.
func (p *Pick) DisplayImage(uploadsURLPrefix string) string {
	if p.ImageUploadPath.Valid && p.ImageUploadPath.String != "" {
		return uploadsURLPrefix + "/" + p.ImageUploadPath.String
	}
	if p.ImageURL.Valid {
		return p.ImageURL.String
	}
	return ""
}

// EffectiveOriginalBody returns the cached original post content to render
// below the curator's Body, or "" if UseOriginalAsBody is off.
func (p *Pick) EffectiveOriginalBody() string {
	if !p.UseOriginalAsBody {
		return ""
	}
	if p.OriginalBodyCache.Valid {
		return p.OriginalBodyCache.String
	}
	return ""
}

// EffectiveBody returns the body, or the excerpt when the body is blank.
// An automatic excerpt is left out when the original post is shown below
// it, since it's that post's own opening: the post page then shows the
// original once, in full.
func (p *Pick) EffectiveBody() string {
	if strings.TrimSpace(p.Body) != "" {
		return p.Body
	}
	if p.ExcerptAuto && strings.TrimSpace(p.EffectiveOriginalBody()) != "" {
		return ""
	}
	return p.Excerpt
}

// LinkHost returns the link's domain without "www.", or "" if there's no
// usable link.
func (p *Pick) LinkHost() string {
	if !p.LinkURL.Valid {
		return ""
	}
	u, err := url.Parse(p.LinkURL.String)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// DisplayDate returns the publish date, or the creation date for a draft.
func (p *Pick) DisplayDate() string {
	if p.PublishedAt.Valid {
		if d := formatDate(p.PublishedAt.String); d != "" {
			return d
		}
	}
	return formatDate(p.CreatedAt)
}

// OriginalDisplayDate returns when the original post was published, or "".
func (p *Pick) OriginalDisplayDate() string {
	if !p.OriginalPublishedAt.Valid {
		return ""
	}
	return formatDate(p.OriginalPublishedAt.String)
}

// formatDate renders an RFC3339 or SQLite datetime as "2 Jan 2006", or "".
func formatDate(s string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2 Jan 2006")
		}
	}
	return ""
}

// DisplayExcerpt returns the excerpt for cards, feeds and meta tags. An
// automatic excerpt is the opening paragraph(s) of the curator's body, else
// of the original post (LeadParagraphs), worked out at display time so it
// follows edits; text without a paragraph (an image on its own) is shown
// whole, and with no text at all the stored excerpt is used.
func (p *Pick) DisplayExcerpt() string {
	if p.ExcerptAuto {
		sources := []string{p.Body}
		if p.OriginalBodyCache.Valid {
			sources = append(sources, p.OriginalBodyCache.String)
		}
		for _, src := range sources {
			if lead := LeadParagraphs(src); lead != "" {
				return lead
			}
		}
		for _, src := range sources {
			if strings.TrimSpace(src) != "" {
				return src
			}
		}
	}
	return p.Excerpt
}

// HasMore reports whether the post page shows more than the card's
// excerpt: a body or original post beyond what the excerpt already shows.
// Cards then end with "Read more".
func (p *Pick) HasMore() bool {
	shown := strings.TrimSpace(p.DisplayExcerpt())
	for _, s := range []string{p.EffectiveBody(), p.EffectiveOriginalBody()} {
		if s = strings.TrimSpace(s); s != "" && s != shown {
			return true
		}
	}
	return false
}

// OriginalHomeURL returns the "View original" target: CommunityPostURL,
// else the ActivityPub ID, else the pasted URL.
func (p *Pick) OriginalHomeURL() string {
	if p.CommunityPostURL.Valid && p.CommunityPostURL.String != "" {
		return p.CommunityPostURL.String
	}
	if p.OriginalApID.Valid && p.OriginalApID.String != "" {
		return p.OriginalApID.String
	}
	return p.OriginalURL.String
}

// EffectiveAltText returns AltText, else Title.
func (p *Pick) EffectiveAltText() string {
	if p.AltText.Valid && strings.TrimSpace(p.AltText.String) != "" {
		return p.AltText.String
	}
	return p.Title
}

type SidebarBlock struct {
	ID          int64
	Title       string
	ContentMD   string
	ContentHTML sql.NullString
	SortOrder   int
	Enabled     bool
	CreatedAt   string
	UpdatedAt   string
}

type SiteSettings struct {
	ID             int64
	SiteName       string
	IconPath       sql.NullString
	Theme          string
	AutoPostLabel  string
	MultiCategory  bool           // extra categories per pick/source
	VisitorThemes  string         // themes visitors may pick: comma list, "*" = all
	PrivacyContact sql.NullString // contact line at the end of the privacy page
}

type Session struct {
	ID        string
	UserID    int64
	ExpiresAt string
}

type PasswordReset struct {
	ID        int64
	UserID    int64
	Token     string
	ExpiresAt string
	Used      bool
	CreatedAt string
}
