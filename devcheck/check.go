// Package devcheck checks adoption of Gantry's built-in performance and
// development patterns. Rules are shared by CLI, CI and browser diagnostics.
package devcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	Violation = "error"
	Advice    = "advice"
)

type Warning struct {
	File                    string
	Line                    int
	Rule, Message, Severity string
}

// Rule owns one convention. Check reads source and reports zero-based byte
// offsets. It must not mutate files or access the network. An empty report
// message uses Guidance. Rules run sequentially; their ordering is not a dependency.
type Rule struct {
	ID, Severity, Purpose, Guidance string
	Extensions                      []string
	Check                           func(*Source, func(offset int, message string)) error
}

// Source is one project-relative file. A scan shares its lazy Go parse across
// applicable rules, avoiding repeated parsing. It is scoped to this scan only.
type Source struct {
	Path, Text string
	parsed     bool
	set        *token.FileSet
	file       *ast.File
	parseErr   error
}

func (s *Source) GoAST() (*token.FileSet, *ast.File, error) {
	if !s.parsed {
		s.parsed = true
		s.set = token.NewFileSet()
		s.file, s.parseErr = parser.ParseFile(s.set, s.Path, s.Text, 0)
	}
	return s.set, s.file, s.parseErr
}

var ruleID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validateRules(rules []Rule) (map[string]Rule, error) {
	byID := make(map[string]Rule, len(rules))
	for _, r := range rules {
		if !ruleID.MatchString(r.ID) || (r.Severity != Violation && r.Severity != Advice) || strings.TrimSpace(r.Purpose) == "" || strings.TrimSpace(r.Guidance) == "" || r.Check == nil || len(r.Extensions) == 0 {
			return nil, fmt.Errorf("invalid development rule %q: need ID, severity, purpose, guidance, extensions and check", r.ID)
		}
		if _, ok := byID[r.ID]; ok {
			return nil, fmt.Errorf("duplicate development rule %q", r.ID)
		}
		for _, ext := range r.Extensions {
			if len(ext) < 2 || ext[0] != '.' || strings.ContainsAny(ext, "/\\*?") {
				return nil, fmt.Errorf("invalid extension %q for rule %s", ext, r.ID)
			}
		}
		byID[r.ID] = r
	}
	return byID, nil
}

// Scan runs Gantry's registered defaults. Source checks cannot prove absence of
// bottlenecks; contextual recommendations remain advisory.
func Scan(root string) ([]Warning, error) { return ScanWithRules(root, DefaultRules()) }

// ScanWithRules allows additional compiled rules without changing scanning,
// exception handling or output. Append extensions to DefaultRules to retain
// Gantry's checks. No dynamic code is loaded from a project's config file.
func ScanWithRules(root string, rules []Rule) ([]Warning, error) {
	byID, err := validateRules(rules)
	if err != nil {
		return nil, err
	}
	extensions := map[string][]Rule{}
	for _, r := range rules {
		for _, ext := range r.Extensions {
			extensions[ext] = append(extensions[ext], r)
		}
	}
	var warnings []Warning
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".gantry", ".cache", "node_modules", "vendor", "built", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		selected := extensions[filepath.Ext(path)]
		if len(selected) == 0 || !entry.Type().IsRegular() || strings.HasSuffix(path, "_templ.go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, _ := filepath.Rel(root, path)
		source := &Source{Path: filepath.ToSlash(name), Text: string(data)}
		for _, rule := range selected {
			var reportErr error
			report := func(offset int, message string) {
				if offset < 0 || offset > len(source.Text) {
					reportErr = fmt.Errorf("rule %s reported invalid offset %d in %s", rule.ID, offset, source.Path)
					return
				}
				if message == "" {
					message = rule.Guidance
				}
				warnings = append(warnings, Warning{File: source.Path, Line: 1 + strings.Count(source.Text[:offset], "\n"), Rule: rule.ID, Message: message, Severity: rule.Severity})
			}
			if err := rule.Check(source, report); err != nil {
				return fmt.Errorf("rule %s (%s): %w", rule.ID, source.Path, err)
			}
			if reportErr != nil {
				return reportErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(warnings, func(i, j int) bool {
		a, b := warnings[i], warnings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	return applyExceptions(root, warnings, byID)
}
