package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// generatedCheck is `gantry check --generated`: templ's and sqlc's code,
// generated again in a copy of the app, against the app's own. A generated
// file that isn't what its source makes was edited by hand, or its source
// was and it wasn't generated again; either way the next generate replaces
// it. It runs the templ and sqlc installed here, as an app's CI does (its
// workflow copies them out of the Dockerfile's toolchain).
func generatedCheck(root string, out io.Writer) error {
	tools := [][]string{{"templ", "generate"}}
	if hasQueries(root) {
		tools = append(tools, []string{"sqlc", "generate"})
	}
	for _, tool := range tools {
		if _, err := lookPath(tool[0]); err != nil {
			return fmt.Errorf("%s isn't installed (or isn't on PATH): check --generated runs the app's generators here, the versions its Dockerfile's toolchain installs", tool[0])
		}
	}
	scratch, err := os.MkdirTemp("", "gantry-generated-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	if err := copyApp(root, scratch); err != nil {
		return err
	}
	for _, tool := range tools {
		if output, err := runQuietlyIn(scratch, tool[0], tool[1:]...); err != nil {
			out.Write(output)
			return fmt.Errorf("%s failed in a copy of the app (%v)", strings.Join(tool, " "), err)
		}
	}
	stale := 0
	err = filepath.WalkDir(scratch, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(scratch, p)
		made, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		have, err := os.ReadFile(filepath.Join(root, rel))
		if err == nil && bytes.Equal(have, made) {
			return nil
		}
		stale++
		fmt.Fprintln(out, staleGenerated(filepath.ToSlash(rel), made, err == nil))
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "gantry check --generated: %d stale generated file(s)\n", stale)
	if stale > 0 {
		return errExit(1)
	}
	return nil
}

// sqlcSource is the query file sqlc made a file from, in its header.
var sqlcSource = regexp.MustCompile(`(?m)^// source: (\S+)$`)

// staleGenerated says what's wrong with generated file rel, which its
// generator made as made, and the source to edit instead.
func staleGenerated(rel string, made []byte, there bool) string {
	tool, source, edit := "sqlc generate", "db/schema.sql and sqlc.yaml", "a migration (bin/gantry db migrate writes db/schema.sql) or sqlc.yaml"
	if strings.HasSuffix(rel, "_templ.go") {
		tool, source = "templ generate", strings.TrimSuffix(rel, "_templ.go")+".templ"
		edit = source
	} else if m := sqlcSource.FindSubmatch(made); m != nil {
		source = path.Join(path.Dir(rel), string(m[1]))
		edit = source
	}
	if !there {
		return fmt.Sprintf("%s is missing: %s makes it from %s; run bin/gantry exec %s and commit it", rel, tool, source, tool)
	}
	return fmt.Sprintf("%s differs from %s of %s; edit %s, not the generated file, and run bin/gantry exec %s (it replaces generated files)", rel, tool, source, edit, tool)
}

// hasQueries reports whether sqlc.yaml names any query files: a new app's
// names app/models/*.sql, which is empty until a model has queries, and sqlc
// fails on no queries.
func hasQueries(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "sqlc.yaml"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(strings.TrimLeft(strings.TrimSpace(line), "- "), "queries:")
		if !ok {
			continue
		}
		pattern := filepath.Join(root, filepath.FromSlash(strings.Trim(strings.TrimSpace(v), `"'`)))
		if info, err := os.Stat(pattern); err == nil && info.IsDir() {
			pattern = filepath.Join(pattern, "*.sql")
		}
		if found, _ := filepath.Glob(pattern); len(found) > 0 {
			return true
		}
	}
	return false
}

// copyApp copies the app's files at root into dir, but for what no
// generator reads: git's, the downloaded tools', the caches and the build's.
func copyApp(root, dir string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			switch filepath.ToSlash(rel) {
			case ".git", ".gantry", ".cache", "node_modules", "assets/built":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
}
