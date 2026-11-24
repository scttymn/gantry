package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/scttymn/gantry/internal/featuremap"
)

var signInFeature = featureYAML("sign-in", "public", []string{"/login", "/login/", "/logout", "/passwords", "/passwords/"}, "/login", "form")

func featureYAML(name, role string, routes []string, visit, selector string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "       - name: %s\n         routes:\n", name)
	for _, r := range routes {
		fmt.Fprintf(&b, "           - %s\n", r)
	}
	fmt.Fprintf(&b, "         role: %s\n         visit:\n           - path: %s\n             selector: %s\n", role, visit, selector)
	return b.String()
}

type listedRoute struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Filters     []string `json:"filters"`
	Cached      bool     `json:"cached"`
	Mounted     bool     `json:"mounted"`
	Constrained bool     `json:"constrained"`
}

func (r listedRoute) name() string { return strings.TrimSpace(r.Method + " " + r.Path) }

func (r listedRoute) visit() string {
	if r.Constrained || r.Method != "" && r.Method != "GET" {
		return ""
	}
	path := strings.TrimSuffix(r.Path, "{$}")
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "{") {
		return ""
	}
	return path
}

type mappedFeature struct {
	Name   string   `yaml:"name"`
	Routes []string `yaml:"routes"`
}

func (f mappedFeature) covers(r listedRoute) bool {
	return slices.ContainsFunc(f.Routes, func(route string) bool { return featuremap.Covers(route, r.Method, r.Path) })
}

type seededFeature struct {
	Name      string        `yaml:"name"`
	Routes    []string      `yaml:"routes"`
	Role      string        `yaml:"role"`
	Cacheable bool          `yaml:"cacheable"`
	Visit     []seededVisit `yaml:"visit,omitempty"`
}

type seededVisit struct {
	Path     string `yaml:"path"`
	Selector string `yaml:"selector,omitempty"`
}

func newAppFeatures() ([]seededFeature, error) {
	raw, err := fs.ReadFile(templates, "templates/new/test/features.yaml.tmpl")
	if err != nil {
		return nil, err
	}
	var m struct {
		Features []seededFeature `yaml:"features"`
	}
	return m.Features, yaml.Unmarshal(raw, &m)
}

func generateFeatures(root string, args []string, out, errOut io.Writer) error {
	flags, local := appFlags()
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return usageError{}
	}
	path := filepath.Join(root, "test", "features.yaml")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var mapped struct {
		Features []mappedFeature `yaml:"features"`
	}
	if err := yaml.Unmarshal(existing, &mapped); err != nil {
		return fmt.Errorf("test/features.yaml: %w", err)
	}
	listed, err := appOutput(root, []string{"routes", "--json"}, *local, errOut)
	if err != nil {
		return fmt.Errorf("the app didn't list its routes (%v), so test/features.yaml is as it was: its routes --json said why, above. An app made before gantry routes adds that command first (docs/development.md#an-app-made-before-the-feature-map)", err)
	}
	var routes []listedRoute
	if err := json.Unmarshal(listed, &routes); err != nil {
		return fmt.Errorf("the app's routes --json isn't a list of routes: %w", err)
	}
	starters, err := newAppFeatures()
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, f := range mapped.Features {
		names[f.Name] = true
	}
	var added []seededFeature
	var report strings.Builder
	for _, r := range routes {
		if r.Mounted || slices.ContainsFunc(mapped.Features, func(f mappedFeature) bool { return f.covers(r) }) {
			continue
		}
		if i := slices.IndexFunc(starters, func(s seededFeature) bool {
			return !names[s.Name] && s.Cacheable == r.Cached && mappedFeature{s.Name, s.Routes}.covers(r)
		}); i >= 0 {
			s := starters[i]
			names[s.Name] = true
			mapped.Features = append(mapped.Features, mappedFeature{s.Name, s.Routes})
			added = append(added, s)
			fmt.Fprintf(&report, "    %s: %s, as a new app's map has it\n", r.name(), s.Name)
			continue
		}
		name := r.name()
		for n := 2; names[name]; n++ {
			name = fmt.Sprintf("%s %d", r.name(), n)
		}
		names[name] = true
		f := seededFeature{Name: name, Routes: []string{r.name()}, Role: "public", Cacheable: r.Cached}
		var about []string
		if visit := r.visit(); visit != "" {
			f.Visit = []seededVisit{{Path: visit}}
			about = append(about, "visited at "+visit)
		}
		if len(r.Filters) > 0 {
			about = append(about, "filtered by "+strings.Join(r.Filters, ", "))
		}
		added = append(added, f)
		fmt.Fprintf(&report, "    %s", r.name())
		if len(about) > 0 {
			fmt.Fprintf(&report, ": %s", strings.Join(about, ", "))
		}
		report.WriteString("\n")
	}
	if len(added) == 0 {
		fmt.Fprintln(out, "  every route is in a feature in test/features.yaml")
		return nil
	}
	written, err := appendFeatures(existing, added)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, written, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "  wrote test/features.yaml: a feature for each route in none\n%s", &report)
	fmt.Fprint(out, `
Each is public, cacheable only if its route is Cached, and visited signed out
where it's a GET of a path with no wildcard. Next, set each one's role, give
its visits a selector (and a status, where it isn't 200), and merge features
as you like; bin/gantry test checks the map (docs/development.md#the-feature-map).
`)
	return nil
}

func appendFeatures(existing []byte, added []seededFeature) ([]byte, error) {
	var entries bytes.Buffer
	e := yaml.NewEncoder(&entries)
	e.SetIndent(2)
	if err := e.Encode(added); err != nil {
		return nil, err
	}
	if err := e.Close(); err != nil {
		return nil, err
	}
	var written bytes.Buffer
	if len(bytes.TrimSpace(existing)) == 0 {
		written.WriteString("features:\n")
	} else {
		written.Write(existing)
		if !bytes.HasSuffix(existing, []byte("\n")) {
			written.WriteByte('\n')
		}
	}
	pad := strings.Repeat(" ", listIndent(existing))
	for line := range strings.Lines(entries.String()) {
		written.WriteString(pad + line)
	}
	var before, after struct {
		Features []any `yaml:"features"`
	}
	var seeded []any
	if err := yaml.Unmarshal(existing, &before); err != nil {
		return nil, err
	}
	errAfter := yaml.Unmarshal(written.Bytes(), &after)
	errSeeded := yaml.Unmarshal(entries.Bytes(), &seeded)
	if errAfter != nil || errSeeded != nil || !reflect.DeepEqual(after.Features, append(before.Features, seeded...)) {
		return nil, fmt.Errorf("test/features.yaml's features aren't a list at its end that these can go after: add them by hand, under features:\n%s", entries.String())
	}
	return written.Bytes(), nil
}

func listIndent(existing []byte) int {
	var doc yaml.Node
	if yaml.Unmarshal(existing, &doc) != nil || len(doc.Content) == 0 {
		return 2
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if list := top.Content[i+1]; top.Content[i].Value == "features" && list.Kind == yaml.SequenceNode && len(list.Content) > 0 {
			lines := strings.Split(string(existing), "\n")
			line := lines[list.Content[0].Line-1]
			return len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	return 2
}
