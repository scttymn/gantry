package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// benchFeedOwn is what bench/feed has of its own in place of what gantry
// new writes, and why: the challenge's API, where a new app has a page.
var benchFeedOwn = map[string]string{
	"app/routes.go":        "the challenge's routes",
	"app/home_test.go":     "the API's 404 and the bench's routes",
	"app/features_test.go": "the API's bearer token signs a visitor in",
	"test/features.yaml":   "the challenge's routes",
	"config/config.go":     "JWT_SECRET",
	"cmd/feed/main.go":     "the app's JWT secret and start time",
	"db/schema.sql":        "the challenge's tables",
	"db/seeds/seeds.go":    "the challenge's users and posts",
	"sqlc.yaml":            "emit_json_tags: a row is the API's JSON",
}

// benchFeedPart is a file bench/feed keeps gantry new's copy of inside text
// of its own: the part that's gantry new's, and what's around it.
var benchFeedPart = map[string]func(bench string) (before, generated, after string){
	// go mod tidy adds the indirect requirements, then the replace.
	"go.mod": func(b string) (string, string, string) {
		i := strings.Index(b, "\n)\n")
		if i < 0 {
			return "", b, ""
		}
		return "", b[:i+3], b[i+3:]
	},
	// A paragraph saying what the app is for.
	"README.md": func(b string) (string, string, string) {
		before, rest, ok := strings.Cut(b, "\n\n")
		if !ok {
			return "", b, ""
		}
		return before + "\n\n", rest, ""
	},
}

// bench/feed is a generated app (gantry new feed), so the bench measures
// what an app runs: what gantry new writes is in it as written, apart from
// what it has of its own.
func TestBenchFeedIsANewApp(t *testing.T) {
	root := t.TempDir()
	if err := newApp(root, []string{"feed", "--skip-houston"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	generated, bench := filepath.Join(root, "feed"), filepath.Join("..", "..", "bench", "feed")
	seen := map[string]bool{}
	err := filepath.WalkDir(generated, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(generated, path)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		if _, ok := benchFeedOwn[rel]; ok {
			return nil
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(bench, rel)
		got, err := os.ReadFile(target)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		missing := err != nil
		before, part, after := "", string(got), ""
		if split, ok := benchFeedPart[rel]; ok && !missing {
			before, part, after = split(string(got))
		}
		if !missing && part == string(want) {
			return nil
		}
		if *update {
			info, _ := d.Info()
			os.MkdirAll(filepath.Dir(target), 0o755)
			return os.WriteFile(target, []byte(before+string(want)+after), info.Mode().Perm())
		}
		t.Errorf("bench/feed/%s isn't what gantry new writes: go test ./cmd/gantry -update rewrites it, then GANTRY_DIR=bench/feed bin/go sh -c 'templ generate && go mod tidy'", rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range benchFeedOwn {
		if !seen[rel] {
			t.Errorf("gantry new doesn't write %s any more: take it out of benchFeedOwn", rel)
		}
	}
}
