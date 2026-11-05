package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/models"
)

type fetchMetadataResponse struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Excerpt is the body's opening paragraph(s), for an automatic excerpt.
	Excerpt  string `json:"excerpt"`
	ImageURL string `json:"image_url"`
	LinkURL  string `json:"link_url"`
	// PublishedAt is the original post's date (RFC3339).
	PublishedAt string `json:"published_at,omitempty"`
	// ApID is the post's canonical ActivityPub ID.
	ApID             string `json:"ap_id,omitempty"`
	CommunityPostURL string `json:"community_post_url,omitempty"`
	Author           string `json:"author"`
	// AuthorHandle/CommunityHandle are "name@instance".
	AuthorHandle    string `json:"author_handle"`
	CommunityHandle string `json:"community_handle"`
	// Platform is set only when the post was found under a different
	// platform than requested, so the form can correct its dropdown.
	Platform string `json:"platform,omitempty"`
	Error    string `json:"error,omitempty"`
}

// allPlatforms is the fallback order for fetches.
var allPlatforms = []models.Platform{models.PlatformLemmy, models.PlatformPieFed, models.PlatformMbin}

// HandleAdminFetchMetadata backs the pick form's "Fetch metadata" button,
// returning the post's metadata as JSON. It writes nothing.
func (s *Server) HandleAdminFetchMetadata(w http.ResponseWriter, r *http.Request) {
	postURL := withScheme(r.URL.Query().Get("url"))
	platform := r.URL.Query().Get("platform")
	if postURL == "" || platform == "" {
		writeJSONError(w, "url and platform are required")
		return
	}

	requested := models.Platform(platform)
	adapter, err := s.Adapters.For(requested)
	if err != nil {
		writeJSONError(w, "unsupported platform: "+platform)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	meta, err := adapter.FetchMetadata(ctx, postURL)
	matchedPlatform := requested

	// The platform can't be told from the URL, so on failure try the others
	// in case the wrong one was selected.
	if err != nil && errors.Is(err, adapters.ErrNotFound) {
		for _, p := range allPlatforms {
			if p == requested {
				continue
			}
			altAdapter, aerr := s.Adapters.For(p)
			if aerr != nil {
				continue
			}
			if altMeta, aerr := altAdapter.FetchMetadata(ctx, postURL); aerr == nil {
				meta, err, matchedPlatform = altMeta, nil, p
				break
			}
		}
	}

	if err != nil {
		writeJSONError(w, "could not fetch that post — check the URL, or fill the fields in manually: "+err.Error())
		return
	}

	resp := fetchMetadataResponse{
		Title:           meta.Title,
		Body:            meta.Body,
		Excerpt:         models.LeadParagraphs(meta.Body),
		ImageURL:        meta.ImageURL,
		LinkURL:         meta.LinkURL,
		ApID:            meta.CanonicalID(),
		Author:          meta.AuthorName,
		AuthorHandle:    meta.AuthorHandle,
		CommunityHandle: meta.CommunityHandle,
	}
	resp.CommunityPostURL = adapters.DefaultResolver.CommunityPostURL(r.Context(), meta.CommunityHandle, meta.CanonicalID())
	if !meta.CreatedAt.IsZero() {
		resp.PublishedAt = meta.CreatedAt.UTC().Format(time.RFC3339)
	}
	if matchedPlatform != requested {
		resp.Platform = string(matchedPlatform)
	}
	writeJSON(w, resp)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(fetchMetadataResponse{Error: msg})
}
