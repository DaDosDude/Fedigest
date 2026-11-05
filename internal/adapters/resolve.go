package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// InstanceResolver finds a post's copy on its community's home instance,
// the better "View original" target since an ap_id points at the poster's
// instance. It uses resolve_object (Lemmy /api/v3, PieFed /api/alpha, both
// anonymous). The instance comes from the community handle, never from
// visitor input.
type InstanceResolver struct {
	HTTP   *http.Client
	Scheme string // "https"; tests use "http" against an httptest server
}

// DefaultResolver is the resolver used by the web handlers and poller.
var DefaultResolver = &InstanceResolver{
	HTTP:   NewHTTPClient(8 * time.Second),
	Scheme: "https",
}

var instanceHostPattern = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?$`)

// CommunityPostURL returns the community instance's URL for the post with
// the given ap_id, or "" if it can't be determined (e.g. Mbin, unreachable,
// not found). communityHandle is "name@instance".
func (r *InstanceResolver) CommunityPostURL(ctx context.Context, communityHandle, apID string) string {
	at := strings.LastIndex(communityHandle, "@")
	if at < 0 || apID == "" {
		return ""
	}
	instance := strings.ToLower(communityHandle[at+1:])
	if !instanceHostPattern.MatchString(instance) {
		return ""
	}
	// Posted from the community's own instance: the ap_id already is it.
	if u, err := url.Parse(apID); err == nil && strings.EqualFold(u.Host, instance) {
		return apID
	}

	base := r.Scheme + "://" + instance
	for _, api := range []string{"/api/v3", "/api/alpha"} {
		if id := r.lookup(ctx, base+api+"/resolve_object?q="+url.QueryEscape(apID)); id > 0 {
			return base + "/post/" + strconv.FormatInt(id, 10)
		}
	}
	return ""
}

func (r *InstanceResolver) lookup(ctx context.Context, endpoint string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Post struct {
			Post struct {
				ID int64 `json:"id"`
			} `json:"post"`
		} `json:"post"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return 0
	}
	return out.Post.Post.ID
}
