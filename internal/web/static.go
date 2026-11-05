package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// StaticHandler serves the embedded assets (admin.css, the theme editor's
// script, the default icon); never a directory listing.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		cacheHeaders(w, r)
		files.ServeHTTP(w, r)
	})
}

// cacheHeaders lets browsers keep an asset: for a year when its URL
// carries a content version (?v=), which changes whenever the file does,
// otherwise for an hour.
func cacheHeaders(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}

// StaticAssetBytes reads one embedded static file, e.g. "admin.css".
func StaticAssetBytes(name string) ([]byte, error) {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(sub, name)
}
