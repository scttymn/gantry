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
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"text/template"

	"github.com/scttymn/gantry/internal/gomod"
	"github.com/scttymn/gantry/internal/pins"
)

// NewApp is what `gantry new` fills its templates with.
type NewApp struct {
	// "myapp": the command, and Houston's project
	Name string
	// "Myapp": for people
	Title string
	// "myapp", or --module's
	Module   string
	Postgres bool
	// sqlc's: "sqlite" or "postgresql"
	Engine string
	// the gantry release go.mod requires
	Gantry string
	// the Houston release bin/gantry downloads
	Houston string
	// The versions and build flags gantry builds with (internal/pins).
	Templ, Go, GolangciLint, GolangImage, SqlcImage, PostgresImage, SQLiteCFlags, SQLiteTags string
	// With --gantry: the checkout, relative to the app (go.work and the dev
	// container's mount), and where the container sees it.
	GantryPath, GantryMount string
	// With --in-module: the app is Dir in the module whose root is Up from
	// it, mounted at /app, so its own folder is Work there.
	InModule bool
	Dir, Up  string
	// "/app", or "/app/<Dir>"
	Work string
	// Where the dev container has the app: Work, or with --gantry, under /app
	// beside the checkout (gantryMounts).
	DevWork string
}

const latestRelease = "v0.17.0"

var releaseVersion string

var releaseTag = regexp.MustCompile(`\Av(\d+)\.(\d+)\.(\d+)\z`)

const houstonRelease = "v0.5.11"

const houstonSHA256SUMS = `b4e55360b44347fa7ab2c5d61a6e58bc6f5c75dccdc38339a8511798119127c7  houston-darwin-amd64
43ef77c9b22b088abcb6a6e71a4727bc77293c0a8ed9f2b6db73e5c277bf98f0  houston-darwin-arm64
182c451d07db313dd28c3e370fafd72fa35484c34bb4563d35168f1703edc4d8  houston-linux-amd64
03bd4046a5042c7fa502fe93cbd578160650abed5455d8f466a6ef4d00aa018e  houston-linux-arm64
`

var appName = regexp.MustCompile(`\A[a-z][a-z0-9-]{0,62}\z`)

var runIn = func(dir, name string, args ...string) error {
	cmd := execCommand(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

var runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

type newOptions struct {
	db, module, gantry    string
	skipHouston, inModule bool
}

func (o *newOptions) flags(name string) *flag.FlagSet {
	fs := flagSet("new")
	fs.StringVar(&o.db, "db", "sqlite", "the database: `sqlite|postgres`")
	fs.StringVar(&o.module, "module", name, "the Go module's `PATH` (github.com/you/"+name+")")
	fs.StringVar(&o.gantry, "gantry", "", "a gantry checkout's `PATH`, to build against with go.work (for developing both)")
	fs.BoolVar(&o.skipHouston, "skip-houston", false, "write the files only: no houston init, go mod tidy, templ generate or git init")
	fs.BoolVar(&o.inModule, "in-module", false, "an app in a folder of the module you're in (a monorepo): no go.mod of its own")
	return fs
}

func newApp(root string, args []string, out, errOut io.Writer) error {
	if len(args) > 0 && helpFlag(args[0]) {
		return flag.ErrHelp
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageError{}
	}
	name := args[0]
	var o newOptions
	if err := parseFlags(o.flags(name), args[1:]); err != nil {
		return err
	}
	if !appName.MatchString(name) || strings.HasSuffix(name, "-") {
		return fmt.Errorf("%q: an app's name is lowercase letters, digits and dashes, starting with a letter", name)
	}
	if o.db != "sqlite" && o.db != "postgres" {
		return fmt.Errorf("--db %s: sqlite or postgres", o.db)
	}
	dir := filepath.Join(root, name)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is there already, and isn't empty", name)
	}

	a := NewApp{Name: name, Title: title(name), Module: o.module, Postgres: o.db == "postgres", Engine: "sqlite", Work: "/app",
		Templ: pins.Templ, Go: pins.Go, GolangciLint: pins.GolangciLint, GolangImage: pins.GolangImage, SqlcImage: pins.SqlcImage, PostgresImage: pins.PostgresImage,
		SQLiteCFlags: pins.SQLiteCFlags, SQLiteTags: pins.SQLiteTags}
	if o.inModule {
		if o.gantry != "" {
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
	a.DevWork = a.Work
	if a.Postgres {
		a.Engine = "postgresql"
	}
	a.Houston = houstonRelease
	release := gantryRelease()
	a.Gantry = release
	if release == "" {
		a.Gantry = latestRelease
	}
	if o.gantry != "" {
		abs, err := filepath.Abs(o.gantry)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			return fmt.Errorf("--gantry %s: no gantry checkout there (no go.mod)", o.gantry)
		}
		appAbs, _ := filepath.Abs(dir)
		rel, err := filepath.Rel(appAbs, abs)
		if err != nil || !strings.HasPrefix(rel, "..") {
			return fmt.Errorf("--gantry %s: the checkout must be outside the app", o.gantry)
		}
		a.GantryPath = filepath.ToSlash(rel)
		a.DevWork, a.GantryMount = gantryMounts(appAbs, abs)
	} else if release == "" {
		fmt.Fprintf(errOut, "note: this gantry isn't a release, so the app requires %s, the newest before it; --gantry PATH builds it against this checkout instead\n", latestRelease)
	}

	if err := writeApp(dir, a); err != nil {
		return err
	}
	fmt.Fprintf(out, "  wrote %s/ (%s, %s)\n", name, a.Module, o.db)
	if err := generateErrorPages(dir, false, io.Discard); err != nil {
		return err
	}
	if o.skipHouston {
		fmt.Fprintf(out, "\nNext, in %s/: houston init --name %s, then gantry exec templ generate, gantry exec go mod tidy, and bin/gantry dev.\n", name, name)
		return nil
	}
	houston, err := houstonFor(dir, name, errOut)
	if err != nil {
		return err
	}
	steps := [][]string{
		{houston, "init", "--name", name},
		{houston, "exec", "templ", "generate"},
		{houston, "exec", "go", "mod", "tidy"},
	}
	if !a.InModule {
		steps = append(steps, []string{"git", "init", "-q"})
	}
	for _, step := range steps {
		shown := strings.Join(append([]string{filepath.Base(step[0])}, step[1:]...), " ")
		fmt.Fprintln(out, "  run", shown)
		var err error
		if step[1] == "init" {
			var output []byte
			if output, err = runQuietlyIn(dir, step[0], step[1:]...); err != nil {
				fmt.Fprint(errOut, string(output))
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

func writeApp(dir string, a NewApp) error {
	sub, _ := fs.Sub(templates, "templates/new")
	err := fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out := strings.Replace(strings.TrimSuffix(path, ".tmpl"), "cmd/NAME/", "cmd/"+a.Name+"/", 1)
		switch out {
		case "gitignore":
			out = ".gitignore"
		case "gantry-check.json":
			out = ".gantry-check.json"
		case "golangci.yml":
			if a.InModule {
				return nil
			}
			out = ".golangci.yml"
		case "github/workflows/ci.yml":
			if a.InModule {
				return nil
			}
			out = ".github/workflows/ci.yml"
		case "assets/built/gitkeep":
			out = "assets/built/.gitkeep"
		case "dockerignore":
			if a.InModule {
				return nil
			}
			out = ".dockerignore"
		case "go.mod", "Dockerfile.dockerignore":
			if a.InModule != (out == "Dockerfile.dockerignore") {
				return nil
			}
		}
		body, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".tmpl") {
			return write(filepath.Join(dir, out), body)
		}
		t, err := template.New(path).Parse(string(body))
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, a); err != nil {
			return err
		}
		return write(filepath.Join(dir, out), b.Bytes())
	})
	if err != nil {
		return err
	}
	if err := write(filepath.Join(dir, "db", "schema.sql"), nil); err != nil {
		return err
	}
	if a.GantryPath != "" {
		work := fmt.Sprintf("go %s\n\nuse (\n\t.\n\t%s\n)\n", a.Go, a.GantryPath)
		if err := write(filepath.Join(dir, "go.work"), []byte(work)); err != nil {
			return err
		}
	}
	env := "SECRET_KEY=" + secretKey() + "\n"
	if a.Postgres {
		raw := make([]byte, 16)
		rand.Read(raw)
		env += "POSTGRES_PASSWORD=" + hex.EncodeToString(raw) + "\n"
	}
	return write(filepath.Join(dir, ".env"), []byte(env))
}

func secretKey() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return hex.EncodeToString(raw)
}

func printSecret(_ string, args []string, out, _ io.Writer) error {
	fs := flagSet("secret")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Errorf("secret takes no arguments (%q)", fs.Arg(0))}
	}
	fmt.Fprintln(out, secretKey())
	return nil
}

func enclosingModule(dir string) (root, path string, err error) {
	root, path, err = gomod.Enclosing(dir)
	if errors.Is(err, gomod.ErrNoModule) {
		err = errors.New("--in-module: there's no go.mod here or above; run it in the module the app goes in")
	}
	return root, path, err
}

func gantryMounts(app, checkout string) (appAt, checkoutAt string) {
	shared := app
	rel, _ := filepath.Rel(app, checkout)
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != ".." {
			break
		}
		shared = filepath.Dir(shared)
	}
	below := func(dir string) string {
		rel, _ := filepath.Rel(shared, dir)
		return path.Join("/app", filepath.ToSlash(rel))
	}
	return below(app), below(checkout)
}

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

func title(name string) string {
	s := strings.ReplaceAll(name, "-", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

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

var houstonVersion = func() string {
	out, err := exec.Command("houston", "--version").Output()
	fields := strings.Fields(string(out))
	if err != nil || len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

var pinnedHouston = func(dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
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
