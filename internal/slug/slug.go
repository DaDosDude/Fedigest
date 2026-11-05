// Package slug produces cosmetic URL slugs.
package slug

import (
	"regexp"
	"strings"
)

var (
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
	trimDash = regexp.MustCompile(`^-+|-+$`)
)

// Slugify produces a URL slug from a title, e.g. "Title of Post!" ->
// "title-of-post". Slugs needn't be unique; the ID is the real key.
func Slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = trimDash.ReplaceAllString(s, "")
	if s == "" {
		s = "untitled"
	}
	return s
}
