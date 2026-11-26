package pins

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type pin struct {
	what   string
	value  string
	owner  string
	copies []handCopy
}

type handCopy struct {
	file string
	re   string
}

const root = "../.."

func read(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func find(t *testing.T, file, re string) string {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(read(t, file))
	if m == nil {
		t.Fatalf("%s has no %s", file, re)
	}
	return m[1]
}

func TestCopiesMatchTheirPin(t *testing.T) {
	workflows := []string{".github/workflows/ci.yml", ".github/workflows/release.yml"}
	scripts := []string{workflows[0], workflows[1], "bin/bench", "bin/ci", "CONTRIBUTING.md"}
	postgres := regexp.MustCompile(`\Apostgres:(\d+)\b`).FindStringSubmatch(PostgresImage)
	if postgres == nil {
		t.Fatalf("PostgresImage %q has no major version", PostgresImage)
	}
	all := func(files []string, re string) (copies []handCopy) {
		for _, f := range files {
			copies = append(copies, handCopy{f, re})
		}
		return copies
	}
	pins := []pin{
		{"Go", find(t, "go.mod", `(?m)^go (\S+)$`), "go.mod's go line", []handCopy{
			{"internal/pins/pins.go", `\bGo\s*=\s*"([^"]*)"`},
			{"bench/go.mod", `(?m)^go (\S+)$`},
			{"bench/sqlite3way/go.mod", `(?m)^go (\S+)$`},
		}},
		{"templ", find(t, "go.mod", `(?m)^\s*github\.com/a-h/templ (\S+)`), "go.mod's templ requirement", []handCopy{
			{"internal/pins/pins.go", `\bTempl\s*=\s*"([^"]*)"`},
			{"Dockerfile", `templ/cmd/templ@(\S+)`},
		}},
		{"golangci-lint", GolangciLint, "internal/pins/pins.go (GolangciLint)", []handCopy{
			{".github/workflows/ci.yml", `golangci-lint/v2/cmd/golangci-lint@(v[\d.]+)`},
			{"bin/ci", `golangci-lint/v2/cmd/golangci-lint@(v[\d.]+)`},
		}},
		{"Go's image", GolangImage, "internal/pins/pins.go (GolangImage)", []handCopy{
			{"Dockerfile", `(?m)^FROM (golang:\S+)`},
		}},
		{"sqlc's image", SqlcImage, "internal/pins/pins.go (SqlcImage)", []handCopy{
			{"Dockerfile", `--from=(sqlc/sqlc:\S+)`},
		}},
		{"Postgres's major version", postgres[1], "internal/pins/pins.go (PostgresImage)",
			all([]string{"Dockerfile", "cmd/gantry/templates/new/Dockerfile.tmpl"}, `postgresql-client-(\d+)`)},
		{"Postgres's image", PostgresImage, "internal/pins/pins.go (PostgresImage)", []handCopy{
			{".github/workflows/release.yml", `image: (postgres:\S+)`},
			{".github/workflows/ci.yml", `\b(postgres:\d+@sha256:[0-9a-f]+)`},
			{"bin/ci", `\b(postgres:\d+@sha256:[0-9a-f]+)`},
		}},
		{"SQLite's C flags", SQLiteCFlags, "internal/pins/pins.go (SQLiteCFlags)", all(scripts, `CGO_CFLAGS="([^"]+)"`)},
		{"SQLite's build tags", SQLiteTags, "internal/pins/pins.go (SQLiteTags)", all(scripts, `-tags[ =](?:bench,)?([\w,]+)`)},
		{"Temurin's image", TemurinImage, "internal/pins/pins.go (TemurinImage)", []handCopy{{"bin/tlc", `\b(eclipse-temurin:\S+)`}}},
		{"TLA+ version", TLAVersion, "internal/pins/pins.go (TLAVersion)", []handCopy{{"bin/tlc", `tla_version=(v[\d.]+)`}}},
		{"TLA+ jar digest", TLASHA256, "internal/pins/pins.go (TLASHA256)", []handCopy{{"bin/tlc", `tla_sha256=([0-9a-f]{64})`}}},
	}
	everyCopyMarkedBy := map[string]string{
		"SQLite's C flags":    "CGO_CFLAGS=",
		"SQLite's build tags": "sqlite_omit_load_extension",
	}
	for _, p := range pins {
		for _, c := range p.copies {
			text := read(t, c.file)
			found := regexp.MustCompile(c.re).FindAllStringSubmatchIndex(text, -1)
			if len(found) == 0 {
				t.Errorf("%s has no %s any more (%s): take it out of the table in internal/pins/pins_test.go", c.file, p.what, c.re)
			}
			if mark := everyCopyMarkedBy[p.what]; mark != "" && strings.Count(text, mark) != len(found) {
				t.Errorf("%s has %s %d times, and %s finds %d: write each copy of %s as the others are, so this test checks every one", c.file, mark, strings.Count(text, mark), c.re, len(found), p.what)
			}
			for _, m := range found {
				if got := text[m[2]:m[3]]; got != p.value {
					line := strings.Count(text[:m[2]], "\n") + 1
					t.Errorf("%s:%d has %s %s, but %s has %s: change it there, make this copy match, and run go test ./cmd/gantry -update for the generated apps", c.file, line, p.what, got, p.owner, p.value)
				}
			}
		}
	}
}

func TestImagesArePinned(t *testing.T) {
	pinned := regexp.MustCompile(`\A[\w./-]+:([\w.-]+)@sha256:[0-9a-f]{64}\z`)
	for name, image := range map[string]string{"GolangImage": GolangImage, "SqlcImage": SqlcImage, "PostgresImage": PostgresImage, "TemurinImage": TemurinImage} {
		if !pinned.MatchString(image) {
			t.Errorf("%s %q isn't name:tag@sha256:<64 hex digits> (internal/pins/pins.go)", name, image)
		}
	}
	series := regexp.MustCompile(`\A\d+\.\d+`).FindString(Go)
	tag := pinned.FindStringSubmatch(GolangImage)
	if series == "" || tag == nil || !strings.HasPrefix(tag[1], series+"-") && !strings.HasPrefix(tag[1], series+".") {
		t.Errorf("GolangImage %q isn't Go %s's series (go.mod's go line): pin a golang:%s image in internal/pins/pins.go", GolangImage, Go, series)
	}
}

func TestSQLiteTagsSelectTheCBuild(t *testing.T) {
	build := find(t, "db/sqlite_c.go", `(?m)^//go:build (.+)$`)
	tags := strings.Split(SQLiteTags, ",")
	for _, tag := range regexp.MustCompile(`\bsqlite_\w+`).FindAllString(build, -1) {
		if !slices.Contains(tags, tag) {
			t.Errorf("db/sqlite_c.go is built with %s, and SQLiteTags (internal/pins/pins.go) hasn't it: an app's build would have the translated SQLite", tag)
		}
	}
}
