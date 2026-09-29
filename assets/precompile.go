package assets

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tdewolff/minify/v2"
	jsmin "github.com/tdewolff/minify/v2/js"
)

// Precompile is what the build does to the assets before go build embeds
// them (Rails' assets:precompile), into dir (assets/built): each script
// minified, into js/, found again by its source's hash, so a script edited
// since isn't shadowed; and, when Images was called, every picture's copies
// into images/, only those missing (WebP alone on a machine that would make
// AVIF at a crawl: images.AVIFSlow says why). Whatever an earlier build left for
// sources that are gone is removed. Without it (development, tests) the
// scripts are served as they are and a copy is made when first asked for.
// made is how many copies it made.
func (a *Assets) Precompile(ctx context.Context, dir string) (made int, err error) {
	if err := a.minifyScripts(filepath.Join(dir, "js")); err != nil {
		return 0, err
	}
	if a.images == nil {
		return 0, nil
	}
	return a.images.Precompile(ctx, filepath.Join(dir, "images"))
}

// sources are the scripts as they are, before any build's minifying.
func (a *Assets) minifyScripts(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m := minify.New()
	m.AddFunc("text/javascript", jsmin.Minify)
	keep := map[string]bool{}
	names := make([]string, 0, len(a.sources))
	for name := range a.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		src := a.sources[name]
		file := sourceHash(src) + ".js"
		keep[file] = true
		out, err := m.Bytes("text/javascript", src)
		if err != nil {
			// Not minifiable (a syntax the minifier doesn't know): served as
			// it is.
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, file), out, 0o644); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !keep[e.Name()] && strings.HasSuffix(e.Name(), ".js") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
