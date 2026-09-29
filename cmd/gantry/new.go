package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"text/template"
)

// NewApp is what `gantry new` fills its templates with.
type NewApp struct {
	Name     string // "myapp": the command, and Houston's project
	Title    string // "Myapp": for people
	Module   string // "myapp", or --module's
	Postgres bool
	Engine   string // sqlc's: "sqlite" or "postgresql"
	Gantry   string // the gantry release go.mod requires
	Templ    string // templ's version, the one gantry builds with
	// With --gantry: the checkout, relative to the app (go.work and the dev
	// container's mount), and where the container sees it.
	GantryPath, GantryMount string
}

// latestRelease is what a new app requires when this gantry isn't a release
// itself (built from a checkout): the newest tag when it was built.
const latestRelease = "v0.6.0"

const templVersion = "v0.3.1020"

var appName = regexp.MustCompile(`\A[a-z][a-z0-9-]{0,62}\z`)

// run runs a command in dir, attached to the terminal: houston, go or git.
// Tests replace it.
var runIn = func(dir, name string, args ...string) error {
	cmd := execCommand(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// runQuietlyIn is runIn keeping the command's output, which it returns
// when the command fails. Tests replace it.
var runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// newApp is `gantry new NAME [flags]`: the app's folder, in the main plan's
// layout, then Houston's files and the generated code.
func newApp(root string, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: gantry new NAME [--db sqlite|postgres] [--module PATH] [--gantry PATH] [--skip-houston]")
	}
	name := args[0]
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(errOut)
	engine := fs.String("db", "sqlite", "the database: sqlite or postgres")
	module := fs.String("module", name, "the Go module path (github.com/you/"+name+")")
	gantryPath := fs.String("gantry", "", "a gantry checkout to build against, with go.work (for developing both)")
	skipHouston := fs.Bool("skip-houston", false, "write the files only: no houston init, go mod tidy, templ generate or git init")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !appName.MatchString(name) || strings.HasSuffix(name, "-") {
		return fmt.Errorf("%q: an app's name is lowercase letters, digits and dashes, starting with a letter", name)
	}
	if *engine != "sqlite" && *engine != "postgres" {
		return fmt.Errorf("--db %s: sqlite or postgres", *engine)
	}
	dir := filepath.Join(root, name)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is there already, and isn't empty", name)
	}

	a := NewApp{Name: name, Title: title(name), Module: *module, Postgres: *engine == "postgres", Engine: "sqlite", Templ: templVersion}
	if a.Postgres {
		a.Engine = "postgresql"
	}
	release := gantryRelease()
	a.Gantry = release
	if release == "" {
		a.Gantry = latestRelease
	}
	if *gantryPath != "" {
		abs, err := filepath.Abs(*gantryPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			return fmt.Errorf("--gantry %s: no gantry checkout there (no go.mod)", *gantryPath)
		}
		appAbs, _ := filepath.Abs(dir)
		rel, err := filepath.Rel(appAbs, abs)
		if err != nil || !strings.HasPrefix(rel, "..") {
			return fmt.Errorf("--gantry %s: the checkout must be outside the app", *gantryPath)
		}
		a.GantryPath, a.GantryMount = filepath.ToSlash(rel), filepath.ToSlash(filepath.Join("/app", rel))
	} else if release == "" {
		fmt.Fprintf(errOut, "note: this gantry isn't a release, so the app requires %s, the newest before it; --gantry PATH builds it against this checkout instead\n", latestRelease)
	}

	if err := writeApp(dir, a); err != nil {
		return err
	}
	fmt.Fprintf(out, "  wrote %s/ (%s, %s)\n", name, a.Module, *engine)
	if err := generateErrorPages(dir, false, io.Discard); err != nil {
		return err
	}
	if *skipHouston {
		fmt.Fprintf(out, "\nNext, in %s/: houston init --name %s, then gantry exec go mod tidy, gantry exec templ generate, and gantry dev.\n", name, name)
		return nil
	}
	for _, step := range [][]string{
		{"houston", "init", "--name", name},
		{"houston", "exec", "go", "mod", "tidy"},
		{"houston", "exec", "templ", "generate"},
		{"git", "init", "-q"},
	} {
		fmt.Fprintln(out, "  run", strings.Join(step, " "))
		var err error
		if step[1] == "init" {
			// Its own "Next:" would be the wrong one here: its output is
			// shown only when it fails.
			var output []byte
			if output, err = runQuietlyIn(dir, step[0], step[1:]...); err != nil {
				errOut.Write(output)
			}
		} else {
			err = runIn(dir, step[0], step[1:]...)
		}
		if err != nil {
			return fmt.Errorf("%s failed (%v): the files are written; run it, and the steps after it, in %s/", strings.Join(step, " "), err, name)
		}
	}
	fmt.Fprintf(out, "\nNext: cd %s && gantry dev, and it's at http://%s.localhost\n", name, name)
	return nil
}

// writeApp writes the templates under templates/new into dir.
func writeApp(dir string, a NewApp) error {
	sub, _ := fs.Sub(templates, "templates/new")
	err := fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		t, err := template.New(path).Parse(string(body))
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, a); err != nil {
			return err
		}
		out := strings.Replace(strings.TrimSuffix(path, ".tmpl"), "cmd/NAME/", "cmd/"+a.Name+"/", 1)
		switch out {
		case "gitignore", "dockerignore":
			out = "." + out
		}
		return write(filepath.Join(dir, out), b.Bytes())
	})
	if err != nil {
		return err
	}
	schema := emptySQLiteSchema
	if a.Postgres {
		schema = emptyPostgresSchema
	}
	if err := write(filepath.Join(dir, "db", "schema.sql"), []byte(schema)); err != nil {
		return err
	}
	if a.GantryPath != "" {
		work := fmt.Sprintf("go 1.27.1\n\nuse (\n\t.\n\t%s\n)\n", a.GantryPath)
		if err := write(filepath.Join(dir, "go.work"), []byte(work)); err != nil {
			return err
		}
	}
	if a.Postgres {
		raw := make([]byte, 16)
		rand.Read(raw)
		// Local only (.env is ignored by git): the dev database's password.
		if err := write(filepath.Join(dir, ".env"), []byte("POSTGRES_PASSWORD="+hex.EncodeToString(raw)+"\n")); err != nil {
			return err
		}
	}
	return nil
}

// db/schema.sql before any table: the header db.Schema writes, on each
// engine.
const (
	emptySQLiteSchema   = "-- The schema as the migrations leave it, for sqlc and for reading. It's\n-- written from the migrations; don't edit it by hand.\n\n"
	emptyPostgresSchema = "-- The schema as the migrations leave it, for sqlc and for reading. It's\n-- written from the migrations by pg_dump; don't edit it by hand.\n"
)

func write(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// title is a name for people: "my-app" is "My app".
func title(name string) string {
	s := strings.ReplaceAll(name, "-", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// gantryRelease is this gantry's version when it was installed as a release
// (go install .../cmd/gantry@v0.6.0), else "".
func gantryRelease() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || !strings.HasPrefix(info.Main.Version, "v") || strings.Contains(info.Main.Version, "-0.") {
		return ""
	}
	return info.Main.Version
}
