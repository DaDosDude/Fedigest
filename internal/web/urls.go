package web

import (
	"net/url"
	"strings"
)

// withScheme adds https:// to a URL typed without one ("lemmy.world/post/1").
func withScheme(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + strings.TrimPrefix(raw, "//")
}

// instanceBaseURL turns "lemmy.world", "https://lemmy.world/" or a pasted
// page URL on the instance into "https://lemmy.world".
func instanceBaseURL(raw string) string {
	u, err := url.Parse(withScheme(raw))
	if err != nil || u.Host == "" {
		return strings.TrimRight(withScheme(raw), "/")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
