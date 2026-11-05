// Package lemmy is the Lemmy platform adapter, a thin wrapper around
// lemmyapi.
package lemmy

import (
	"context"

	"fedigest/internal/adapters"
	"fedigest/internal/adapters/lemmyapi"
	"fedigest/internal/models"
)

type Adapter struct {
	client *lemmyapi.Client
}

func New() *Adapter {
	return &Adapter{client: lemmyapi.New()}
}

func (a *Adapter) FetchMetadata(ctx context.Context, postURL string) (adapters.Metadata, error) {
	return a.client.FetchMetadata(ctx, postURL)
}

func (a *Adapter) FetchComments(ctx context.Context, postURL string) (adapters.CommentTree, error) {
	return a.client.FetchComments(ctx, postURL)
}

func (a *Adapter) FetchCommentCount(ctx context.Context, postURL string) (int, error) {
	return a.client.FetchCommentCount(ctx, postURL)
}

func (a *Adapter) Poll(ctx context.Context, source models.Source) ([]adapters.Metadata, error) {
	return a.client.Poll(ctx, source)
}
