package gantry_test

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// The docs readers and agents start from: each must open with its title,
// keep its table rows in tables, link only to files and headings that exist,
// and name in a decisions table only modules go.mod requires.
func TestDocs(t *testing.T) {
	files := []string{"README.md", "CONTRIBUTING.md", "AGENTS.md", "llms.txt", "cmd/gantry/templates/new/README.md.tmpl", "cmd/gantry/templates/new/AGENTS.md.tmpl"}
	for _, glob := range []string{"docs/*.md", "docs/plans/*.md"} {
		matches, err := filepath.Glob(glob)
		if err != nil || len(matches) == 0 {
			t.Fatalf("no docs match %s: %v", glob, err)
		}
		files = append(files, matches...)
	}
	required := goModRequires(t)
	for _, file := range files {
		d, ok := readDoc(file)
		if !ok {
			t.Fatalf("%s: can't read it", file)
		}
		for _, p := range check(d, readDoc, required) {
			t.Errorf("%s:%d: %s", d.path, p.line, p.msg)
		}
	}
}

func TestDocChecksCatchProblems(t *testing.T) {
	files := map[string]doc{
		"docs/other.md": parseDoc("docs/other.md", "# Other\n\n## Reload on save\n\n## Set up `bin/gantry`: twice\n"),
		"docs":          {path: "docs"},
	}
	read := func(p string) (doc, bool) { d, ok := files[p]; return d, ok }
	cases := []struct {
		name, text, want string
	}{
		{"row above the title", "| a | b |\n# Title\n\n| H | I |\n|---|---|\n| c | d |\n", "Move it into the table at docs/x.md:4."},
		{"text above the title", "intro\n# Title\n", `the first line must be the "# " title`},
		{"row with no separator", "# Title\n\n| a | b |\n| c | d |\n", "table row outside a table"},
		{"missing file", "# Title\n\nSee [it](missing.md).\n", "docs/missing.md doesn't exist"},
		{"missing anchor", "# Title\n\nSee [it](other.md#reload).\n", "has no heading with the anchor #reload"},
		{"missing local anchor", "# Title\n\nSee [below](#later).\n", "has no heading with the anchor #later"},
		{"missing file on main", "# Title\n\n- [Gone](https://github.com/scttymn/gantry/blob/main/docs/gone.md): notes\n", "docs/gone.md doesn't exist"},
		{"module not in go.mod", "# Title\n\n## Decisions\n| Need | Choice |\n|---|---|\n| CLI | `github.com/spf13/cobra` |\n", "github.com/spf13/cobra is named in a decisions table"},
		{"replacement not in go.mod", "# Title\n\n## Decisions\n| Need | Choice |\n|---|---|\n| CLI | `github.com/spf13/cobra`. **Superseded:** `github.com/urfave/cli` |\n", "github.com/urfave/cli is named in a decisions table"},
		{"superseded outside the choice", "# Title\n\n## Decisions\n| Need | Choice | Why |\n|---|---|---|\n| CLI | `github.com/spf13/cobra` | Superseded the old one |\n", "github.com/spf13/cobra is named in a decisions table"},
		{"another major version", "# Title\n\n## Decisions\n| Need | Choice |\n|---|---|\n| Views | `github.com/a-h/templ/v2/runtime` |\n", "github.com/a-h/templ/v2/runtime is named in a decisions table"},
	}
	required := map[string]bool{"github.com/a-h/templ": true}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := parseDoc("docs/x.md", c.text)
			var got []string
			for _, p := range check(d, read, required) {
				got = append(got, fmt.Sprintf("%s:%d: %s", d.path, p.line, p.msg))
			}
			if !strings.Contains(strings.Join(got, "\n"), c.want) {
				t.Errorf("want a problem containing %q, got:\n%s", c.want, strings.Join(got, "\n"))
			}
		})
	}

	sound := parseDoc("docs/x.md", "\n# Title\n\nSee [reload](other.md#reload-on-save), [setup](other.md#set-up-bingantry-twice), [plans](https://github.com/scttymn/gantry/tree/main/docs), [code](`x](y)`) and [below](#decisions).\n\n## Decisions\n| Need | Choice | Why |\n| :--- | ---: | --- |\n| Views | `github.com/a-h/templ/runtime` | Typed, as [its docs](https://pkg.go.dev/net/http) and https://example.com/a/b say |\n| CSRF | Go 1.25/1.26's `http.CrossOriginProtection`, as in github.com/a-h/templ. | Standard |\n| CLI | `github.com/spf13/cobra`. **Superseded:** `flag` | `github.com/spf13/cobra` is what Houston used |\n\n```sh\n| not a table\n```\n")
	for _, p := range check(sound, read, required) {
		t.Errorf("a sound doc has a problem: %d: %s", p.line, p.msg)
	}

	files["cmd/new/AGENTS.md.tmpl"] = parseDoc("cmd/new/AGENTS.md.tmpl", "# Agents\n\n## Where things go\n")
	readme := parseDoc("cmd/new/README.md.tmpl", "# App\n\nSee [AGENTS.md](AGENTS.md#where-things-go), not [this](GONE.md).\n")
	if got := check(readme, read, required); len(got) != 1 || !strings.Contains(got[0].msg, "cmd/new/GONE.md doesn't exist") {
		t.Errorf("a template's link to the file its sibling template writes: %+v", got)
	}
}

func check(d doc, read func(string) (doc, bool), required map[string]bool) []problem {
	var problems []problem
	problems = append(problems, titleProblems(d)...)
	problems = append(problems, tableProblems(d)...)
	problems = append(problems, linkProblems(d, read)...)
	return append(problems, decisionProblems(d, required)...)
}

type doc struct {
	path string
	// lines, with fenced code blanked so that no check reads code as prose
	lines []string
}

type problem struct {
	line int
	msg  string
}

func readDoc(file string) (doc, bool) {
	info, err := os.Stat(file)
	if err != nil {
		return doc{}, false
	}
	if info.IsDir() {
		return doc{path: file}, true
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return doc{}, false
	}
	return parseDoc(file, string(data)), true
}

func parseDoc(file, text string) doc {
	lines := strings.Split(text, "\n")
	fenced := false
	for i, l := range lines {
		if trimmed := strings.TrimSpace(l); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		} else if fenced {
			lines[i] = ""
		}
	}
	return doc{path: file, lines: lines}
}

func titleProblems(d doc) []problem {
	for i, l := range d.lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(l, "# ") {
			return nil
		}
		return []problem{{i + 1, `the first line must be the "# " title. Move this line below the title, into its section.`}}
	}
	return []problem{{1, `the file is empty. Start it with a "# " title.`}}
}

func isRow(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "|") }

func cells(l string) []string {
	return strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|")
}

func isSeparator(l string) bool {
	for _, c := range cells(l) {
		c = strings.Trim(strings.TrimSpace(c), ":")
		if c == "" || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return isRow(l)
}

// tables returns each run of "|" lines as [start, end) line indexes, and
// whether it is a table: a header row, then a separator row.
func tables(d doc) (runs [][2]int, isTable func([2]int) bool) {
	for i := 0; i < len(d.lines); i++ {
		if !isRow(d.lines[i]) {
			continue
		}
		j := i
		for j < len(d.lines) && isRow(d.lines[j]) {
			j++
		}
		runs = append(runs, [2]int{i, j})
		i = j
	}
	return runs, func(r [2]int) bool {
		return r[1]-r[0] >= 2 && !isSeparator(d.lines[r[0]]) && isSeparator(d.lines[r[0]+1])
	}
}

func tableProblems(d doc) []problem {
	runs, isTable := tables(d)
	var problems []problem
	for _, r := range runs {
		if isTable(r) {
			continue
		}
		fix := `Add a header row and a |---| separator row above it, or don't start the line with "|".`
		for _, table := range runs {
			if isTable(table) && len(cells(d.lines[table[0]])) == len(cells(d.lines[r[0]])) {
				fix = fmt.Sprintf("Move it into the table at %s:%d.", d.path, table[0]+1)
				break
			}
		}
		problems = append(problems, problem{r[0] + 1, "table row outside a table: a table starts with a header row and a |---| separator row. " + fix})
	}
	return problems
}

var (
	codeSpan = regexp.MustCompile("`[^`]*`")
	link     = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	heading  = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	linkText = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	onMain   = regexp.MustCompile(`^https://github\.com/scttymn/gantry/(?:blob|tree)/main/(.+)$`)
)

// linkProblems checks relative links, and links to this repo's main branch
// (llms.txt's and new apps' READMEs), against the files in this checkout.
func linkProblems(d doc, read func(string) (doc, bool)) []problem {
	var problems []problem
	for i, l := range d.lines {
		for _, m := range link.FindAllStringSubmatch(codeSpan.ReplaceAllString(l, ""), -1) {
			target := m[1]
			file, anchor, _ := strings.Cut(target, "#")
			linked, ok := d, true
			switch main := onMain.FindStringSubmatch(file); {
			case main != nil:
				file = main[1]
				linked, ok = read(file)
			case strings.Contains(file, ":"):
				continue
			case file == "":
				file = d.path
			default:
				file = path.Join(path.Dir(d.path), file)
				linked, ok = read(file)
				if !ok && strings.HasSuffix(d.path, ".tmpl") {
					// The generator writes X.tmpl as X beside it.
					linked, ok = read(file + ".tmpl")
				}
			}
			if !ok {
				problems = append(problems, problem{i + 1, fmt.Sprintf("link to %q: %s doesn't exist. Fix the path, or remove the link.", target, file)})
				continue
			}
			if anchor != "" && !anchors(linked)[anchor] {
				problems = append(problems, problem{i + 1, fmt.Sprintf("link to %q: %s has no heading with the anchor #%s. Use the heading's GitHub anchor: its text lowercased, punctuation dropped, spaces as hyphens.", target, file, anchor)})
			}
		}
	}
	return problems
}

// anchors are GitHub's: a heading's text, lowercased, with spaces as hyphens
// and other punctuation dropped; a repeated one gets -1, -2, and so on.
func anchors(d doc) map[string]bool {
	seen := map[string]int{}
	out := map[string]bool{}
	for _, l := range d.lines {
		m := heading.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		var slug strings.Builder
		for _, r := range strings.ToLower(linkText.ReplaceAllString(m[1], "$1")) {
			switch {
			case r == ' ':
				slug.WriteRune('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				slug.WriteRune(r)
			}
		}
		s := slug.String()
		if n := seen[s]; n > 0 {
			out[fmt.Sprintf("%s-%d", s, n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

var (
	modulePath   = regexp.MustCompile(`\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}(?:/[A-Za-z0-9._~-]+)+`)
	addresses    = regexp.MustCompile(`\]\([^)]*\)|https?://\S+`)
	superseded   = regexp.MustCompile(`(?i)superseded`)
	majorVersion = regexp.MustCompile(`^v(?:[2-9]|[1-9][0-9]+)$`)
)

// decisionProblems holds a plan's decisions table to go.mod: a module it
// names is required there. A Choice marked Superseded is history up to the
// marker, and only what replaced it, after the marker, is checked.
func decisionProblems(d doc, required map[string]bool) []problem {
	runs, isTable := tables(d)
	var problems []problem
	for _, r := range runs {
		if !isTable(r) || !strings.Contains(strings.ToLower(headingAbove(d, r[0])), "decisions") {
			continue
		}
		choice := slices.IndexFunc(cells(d.lines[r[0]]), func(c string) bool { return strings.EqualFold(strings.TrimSpace(c), "choice") })
		for i := r[0] + 2; i < r[1]; i++ {
			text := d.lines[i]
			if row := cells(text); choice >= 0 && choice < len(row) {
				if m := superseded.FindStringIndex(row[choice]); m != nil {
					text = row[choice][m[1]:]
				}
			}
			for _, mod := range modulePath.FindAllString(addresses.ReplaceAllString(text, ""), -1) {
				if mod = strings.TrimRight(mod, "."); !requires(required, mod) {
					problems = append(problems, problem{i + 1, fmt.Sprintf(`%s is named in a decisions table but go.mod doesn't require it. Require it, or, if it's no longer used, end its Choice with "**Superseded:**" and what replaced it.`, mod)})
				}
			}
		}
	}
	return problems
}

func headingAbove(d doc, line int) string {
	for i := line - 1; i >= 0; i-- {
		if m := heading.FindStringSubmatch(d.lines[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

// requires reports whether a module go.mod requires holds pkg. Its path's
// enclosing modules are tried in turn, but not across a major version:
// m/v2 is a module of its own, not a package of m.
func requires(required map[string]bool, pkg string) bool {
	for p := pkg; p != "."; p = path.Dir(p) {
		if required[p] {
			return true
		}
		if majorVersion.MatchString(path.Base(p)) && required[path.Dir(p)] {
			return false
		}
	}
	return false
}

// goModRequires is this module and what it requires directly; an indirect
// requirement is another module's choice, not a decision of this one.
func goModRequires(t *testing.T) map[string]bool {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	inRequire := false
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 0 || strings.Contains(l, "// indirect"):
		case f[0] == "module" && len(f) == 2:
			required[f[1]] = true
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			inRequire = true
		case f[0] == ")":
			inRequire = false
		case f[0] == "require" && len(f) >= 3:
			required[f[1]] = true
		case inRequire:
			required[f[0]] = true
		}
	}
	return required
}
