package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
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
	Houston  string // the Houston release bin/gantry downloads
	Templ    string // templ's version, the one gantry builds with
	// With --gantry: the checkout, relative to the app (go.work and the dev
	// container's mount), and where the container sees it.
	GantryPath, GantryMount string
	// With --in-module: the app is Dir in the module whose root is Up from
	// it, mounted at /app, so its own folder is Work there.
	InModule bool
	Dir, Up  string
	Work     string // "/app", or "/app/<Dir>"
}

// latestRelease is what a new app requires when this gantry isn't a release
// itself (built from a checkout): the newest tag when it was built. It's
// raised to a release's tag before the tag is pushed, and the release's tests
// check it was (TestLatestReleaseIsTheTag), so it can't fall behind.
const latestRelease = "v0.15.0"

// releaseVersion is the release's tag in the CLI binaries a release
// publishes: the Dockerfile's release-build stage sets it (-X
// main.releaseVersion). They're built from a checkout, so Go's build info
// doesn't say which release they are.
var releaseVersion string

var releaseTag = regexp.MustCompile(`\Av(\d+)\.(\d+)\.(\d+)\z`)

// houstonRelease is the Houston CLI bin/gantry downloads. It is a release
// that published houston-$os-$arch and SHA256SUMS.
const houstonRelease = "v0.5.4"

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
// layout, then Houston's files and the generated code. With --in-module the
// app is a folder of the module it's made in: it has no go.mod of its own,
// and it's built from the module's root.
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
	inModule := fs.Bool("in-module", false, "an app in a folder of the module you're in (a monorepo): no go.mod of its own")
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

	a := NewApp{Name: name, Title: title(name), Module: *module, Postgres: *engine == "postgres", Engine: "sqlite", Templ: templVersion, Work: "/app"}
	if *inModule {
		if *gantryPath != "" {
			return errors.New("--in-module and --gantry: point the module's go.mod at a gantry checkout with a replace instead")
		}
		root, path, err := enclosingModule(dir)
		if err != nil {
			return err
		}
		appAbs, _ := filepath.Abs(dir)
		rel, _ := filepath.Rel(root, appAbs)
		a.InModule, a.Dir = true, filepath.ToSlash(rel)
		a.Up = strings.TrimSuffix(strings.Repeat("../", strings.Count(a.Dir, "/")+1), "/")
		a.Work = "/app/" + a.Dir
		a.Module = path + "/" + a.Dir
	}
	if a.Postgres {
		a.Engine = "postgresql"
	}
	a.Houston = houstonRelease
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
		fmt.Fprintf(out, "\nNext, in %s/: houston init --name %s, then gantry exec templ generate, gantry exec go mod tidy, and bin/gantry dev.\n", name, name)
		return nil
	}
	houston, err := houstonFor(dir, name, errOut)
	if err != nil {
		return err
	}
	steps := [][]string{
		{houston, "init", "--name", name},
		// templ's code first, so tidy sees the imports it adds.
		{houston, "exec", "templ", "generate"},
		{houston, "exec", "go", "mod", "tidy"},
	}
	if !a.InModule {
		steps = append(steps, []string{"git", "init", "-q"})
	}
	for _, step := range steps {
		// The pinned Houston is a path: it's "houston" to people.
		shown := strings.Join(append([]string{filepath.Base(step[0])}, step[1:]...), " ")
		fmt.Fprintln(out, "  run", shown)
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
			return fmt.Errorf("%s failed (%v): the files are written; run it, and the steps after it, in %s/", shown, err, name)
		}
	}
	fmt.Fprintf(out, "\nNext: cd %s && bin/gantry dev, and it's at http://%s.localhost\n", name, name)
	return nil
}

// writeApp writes the templates under templates/new into dir: a .tmpl file
// filled in with a, any other (vendored JavaScript) as it is.
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
		if !strings.HasSuffix(path, ".tmpl") {
			return write(filepath.Join(dir, path), body)
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
		case "gitignore":
			out = ".gitignore"
		case "assets/built/gitkeep":
			// Kept so the folder is there for go:embed before a build.
			out = "assets/built/.gitkeep"
		case "dockerignore":
			// In a module, the build's context is the module's root, and
			// BuildKit reads Dockerfile.dockerignore instead.
			if a.InModule {
				return nil
			}
			out = ".dockerignore"
		case "go.mod", "Dockerfile.dockerignore":
			if a.InModule != (out == "Dockerfile.dockerignore") {
				return nil
			}
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

// enclosingModule is the module dir is in: its root and its path, from the
// nearest go.mod at or above dir.
func enclosingModule(dir string) (root, path string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if data, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return d, strings.TrimSpace(m), nil
				}
			}
			return "", "", fmt.Errorf("%s names no module", filepath.Join(d, "go.mod"))
		}
		if filepath.Dir(d) == d {
			return "", "", errors.New("--in-module: there's no go.mod here or above; run it in the module the app goes in")
		}
	}
}

// write writes a generated file, a Go one as gofmt would (a template that
// doesn't parse is written as it is, for the build to report).
func write(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(path, ".go") {
		if f, err := format.Source(body); err == nil {
			body = f
		}
	}
	mode := os.FileMode(0o644)
	if strings.HasSuffix(path, "/bin/gantry") {
		mode = 0o755
	}
	return os.WriteFile(path, body, mode)
}

// title is a name for people: "my-app" is "My app".
func title(name string) string {
	s := strings.ReplaceAll(name, "-", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// houstonFor is the houston gantry new runs in the app at dir: the one on
// PATH when it's houstonRelease or newer (or says no version: one built from
// a checkout), else the release the app pins, which its bin/gantry downloads.
// An older one doesn't know what gantry new asks of it (init --name).
func houstonFor(dir, name string, errOut io.Writer) (string, error) {
	found := houstonVersion()
	if found != "" && !older(found, houstonRelease) {
		return "houston", nil
	}
	have := "there's no houston on PATH"
	if found != "" {
		have = "the houston on PATH is " + found
	}
	path, err := pinnedHouston(dir)
	if err != nil {
		return "", fmt.Errorf("gantry new needs Houston %s or newer, %s, and downloading it failed (%v): the files are written; install it (https://github.com/scttymn/houston#install), then in %s/ run houston init --name %s, gantry exec templ generate and gantry exec go mod tidy", houstonRelease, have, err, name, name)
	}
	fmt.Fprintf(errOut, "note: gantry new needs Houston %s or newer and %s, so it's using the release the app pins (%s/.gantry/bin/houston, as bin/gantry does)\n", houstonRelease, have, name)
	return path, nil
}

// houstonVersion is the version of the houston on PATH ("v0.5.4", or
// whatever one built from a checkout says), and "" when there's none.
// Tests replace it.
var houstonVersion = func() string {
	out, err := exec.Command("houston", "--version").Output()
	fields := strings.Fields(string(out)) // "houston version v0.5.4"
	if err != nil || len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// pinnedHouston has the app's bin/gantry download the Houston release it
// pins, and is where that is. GANTRY_BIN keeps bin/gantry from downloading
// a gantry as well: this one is running. Tests replace it.
var pinnedHouston = func(dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Absolute: the steps run in the app's folder, where a path relative to
	// this one isn't the same file.
	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	}
	cmd := exec.Command(filepath.Join(dir, "bin", "gantry"), "help")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GANTRY_BIN="+self)
	if out, err := cmd.CombinedOutput(); err != nil {
		if said := strings.TrimSpace(string(out)); said != "" {
			return "", errors.New(said)
		}
		return "", err
	}
	return filepath.Join(dir, ".gantry", "bin", "houston"), nil
}

// older reports whether release tag v is before min; a v that isn't a
// release tag (a build from a checkout) is not.
func older(v, min string) bool {
	a, b := releaseTag.FindStringSubmatch(v), releaseTag.FindStringSubmatch(min)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// gantryRelease is this gantry's version when it is a release: one of a
// release's binaries (releaseVersion), or installed as one (go install
// .../cmd/gantry@v0.6.0); else "".
func gantryRelease() string {
	if releaseTag.MatchString(releaseVersion) {
		return releaseVersion
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || !strings.HasPrefix(info.Main.Version, "v") || strings.Contains(info.Main.Version, "-0.") {
		return ""
	}
	return info.Main.Version
}
