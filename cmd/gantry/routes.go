package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/scttymn/gantry/internal/featuremap"
)

func routesFlags() (fs *flag.FlagSet, local, asJSON, diagram *bool) {
	fs, local = appFlags()
	asJSON = fs.Bool("json", false, "a JSON array of web.Route, with stable field names")
	diagram = fs.Bool("diagram", false, "a Mermaid flowchart of the app's routes and test/features.yaml")
	return fs, local, asJSON, diagram
}

func routes(root string, args []string, out, errOut io.Writer) error {
	fs, local, asJSON, diagram := routesFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *asJSON && *diagram {
		return usageError{}
	}
	if *diagram {
		line, err := appLine(root, []string{"routes", "--json"}, *local, errOut)
		if err != nil {
			return err
		}
		listed, err := outputIn(root, line[0], line[1:]...)
		if err != nil {
			return fmt.Errorf("the app didn't list its routes (%v): its routes --json said why, above", exitError(err))
		}
		var routeList []listedRoute
		if err := json.Unmarshal(listed, &routeList); err != nil {
			return fmt.Errorf("the app's routes --json isn't a list of routes: %w", err)
		}
		features, err := readDiagramFeatures(filepath.Join(root, "test", "features.yaml"))
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, renderRoutesDiagram(routeList, features))
		return err
	}
	app := []string{"routes"}
	if *asJSON {
		app = append(app, "--json")
	}
	return runApp(root, app, *local, errOut)
}

type diagramFeature struct {
	Name      string   `yaml:"name"`
	Routes    []string `yaml:"routes"`
	Role      string   `yaml:"role"`
	Cacheable bool     `yaml:"cacheable"`
}

func (f diagramFeature) covers(r listedRoute) bool {
	return slices.ContainsFunc(f.Routes, func(route string) bool { return featuremap.Covers(route, r.Method, r.Path) })
}

func readDiagramFeatures(path string) ([]diagramFeature, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m struct {
		Features []diagramFeature `yaml:"features"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("test/features.yaml: %w", err)
	}
	return m.Features, nil
}

func renderRoutesDiagram(routeList []listedRoute, features []diagramFeature) string {
	sortedFeatures := slices.Clone(features)
	slices.SortFunc(sortedFeatures, func(a, b diagramFeature) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Role, b.Role)
	})
	matched := make([][]listedRoute, len(sortedFeatures))
	var mounted, unmapped []listedRoute
	for _, r := range routeList {
		if r.Mounted {
			mounted = append(mounted, r)
			continue
		}
		covered := false
		for i, f := range sortedFeatures {
			if f.covers(r) {
				matched[i] = append(matched[i], r)
				covered = true
			}
		}
		if !covered {
			unmapped = append(unmapped, r)
		}
	}
	sortRoutes := func(list []listedRoute) {
		slices.SortFunc(list, func(a, b listedRoute) int {
			if c := strings.Compare(a.Path, b.Path); c != 0 {
				return c
			}
			return strings.Compare(a.Method, b.Method)
		})
	}
	for i := range matched {
		sortRoutes(matched[i])
	}
	sortRoutes(mounted)
	sortRoutes(unmapped)

	roleOrder := []string{"public", "signed-in", "admin"}
	roleIndex := func(role string) int {
		if i := slices.Index(roleOrder, role); i >= 0 {
			return i
		}
		return 0
	}
	highestRole := -1
	byRole := map[string][]int{}
	for i, f := range sortedFeatures {
		role := f.Role
		if !slices.Contains(roleOrder, role) {
			role = "public"
		}
		byRole[role] = append(byRole[role], i)
		if idx := roleIndex(role); idx > highestRole {
			highestRole = idx
		}
	}

	var b strings.Builder
	b.WriteString("flowchart LR\n")
	if highestRole >= 0 {
		for i := 0; i <= highestRole; i++ {
			role := roleOrder[i]
			fmt.Fprintf(&b, "  %s[\"%s\"]\n", roleNodeID(role), mermaidLabel(role))
		}
		for i := 0; i < highestRole; i++ {
			fmt.Fprintf(&b, "  %s --> %s\n", roleNodeID(roleOrder[i]), roleNodeID(roleOrder[i+1]))
		}
	}

	var edges []string
	for _, role := range roleOrder {
		indices := byRole[role]
		if len(indices) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  subgraph sg_%s[\"%s\"]\n", roleKey(role), mermaidLabel(role))
		for _, idx := range indices {
			f := sortedFeatures[idx]
			id := fmt.Sprintf("f%d", idx)
			header := f.Name
			if f.Cacheable {
				header += " [cached]"
			}
			lines := []string{header}
			for _, r := range matched[idx] {
				lines = append(lines, formatDiagramRoute(r))
			}
			fmt.Fprintf(&b, "    %s[\"%s\"]\n", id, mermaidLabel(strings.Join(lines, "\n")))
			edges = append(edges, fmt.Sprintf("  %s --> %s\n", roleNodeID(role), id))
		}
		b.WriteString("  end\n")
	}
	for _, e := range edges {
		b.WriteString(e)
	}

	if len(mounted) > 0 {
		b.WriteString("  subgraph sg_mounted[\"mounted\"]\n")
		for i, r := range mounted {
			fmt.Fprintf(&b, "    m%d[\"%s\"]\n", i, mermaidLabel(formatDiagramRoute(r)))
		}
		b.WriteString("  end\n")
	}
	if len(unmapped) > 0 {
		b.WriteString("  subgraph sg_unmapped[\"unmapped\"]\n")
		var ids []string
		for i, r := range unmapped {
			id := fmt.Sprintf("u%d", i)
			ids = append(ids, id)
			fmt.Fprintf(&b, "    %s[\"%s\"]\n", id, mermaidLabel(formatDiagramRoute(r)))
		}
		b.WriteString("  end\n")
		b.WriteString("  classDef unmapped fill:#fee,stroke:#c00,color:#900\n")
		fmt.Fprintf(&b, "  class %s unmapped\n", strings.Join(ids, ","))
	}
	return b.String()
}

func roleKey(role string) string { return strings.ReplaceAll(role, "-", "_") }

func roleNodeID(role string) string { return "role_" + roleKey(role) }

func formatDiagramRoute(r listedRoute) string {
	s := r.name()
	if r.Cached {
		s += " [cached]"
	}
	if r.Constrained {
		s += " [constrained]"
	}
	if len(r.Filters) > 0 {
		s += " (" + strings.Join(r.Filters, ", ") + ")"
	}
	return s
}

func mermaidLabel(s string) string {
	s = strings.ReplaceAll(s, "\"", "'")
	return strings.ReplaceAll(s, "\n", `\n`)
}
