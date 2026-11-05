package web

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
)

// baseTheme ships the full template set; every other theme clones it and
// overrides only what differs.
const baseTheme = "light"

// loadThemes parses each theme under themesRoot by cloning the base theme
// and parsing the theme's own *.html on top, so only its {{define}} blocks
// are overridden. Returns template sets and on-disk directories by name.
func loadThemes(themesRoot string, funcMap template.FuncMap) (map[string]*template.Template, map[string]string, error) {
	defaultDir := filepath.Join(themesRoot, baseTheme)
	base, err := template.New("").Funcs(funcMap).ParseGlob(filepath.Join(defaultDir, "*.html"))
	if err != nil {
		return nil, nil, fmt.Errorf("loading default theme: %w", err)
	}

	themes := map[string]*template.Template{baseTheme: base}
	dirs := map[string]string{baseTheme: defaultDir}

	entries, err := os.ReadDir(themesRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("reading themes directory: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == baseTheme {
			continue
		}
		dir := filepath.Join(themesRoot, e.Name())
		matches, err := filepath.Glob(filepath.Join(dir, "*.html"))
		if err != nil {
			return nil, nil, fmt.Errorf("scanning theme %q: %w", e.Name(), err)
		}

		cloned, err := base.Clone()
		if err != nil {
			return nil, nil, fmt.Errorf("cloning default theme for %q: %w", e.Name(), err)
		}
		if len(matches) > 0 {
			cloned, err = cloned.ParseGlob(filepath.Join(dir, "*.html"))
			if err != nil {
				return nil, nil, fmt.Errorf("loading theme %q overrides: %w", e.Name(), err)
			}
		}
		themes[e.Name()] = cloned
		dirs[e.Name()] = dir
	}

	return themes, dirs, nil
}
