package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"
)

//go:embed templates
var templates embed.FS

// Field is one of a resource's columns, as its form edits it.
type Field struct {
	Column   string // "hero_title"
	Type     string // string, text, int, position, date
	Required bool
	Go       string // sqlc's name for it: "HeroTitle"
	Label    string // "Hero title"
}

// Resource is what the templates are filled with.
type Resource struct {
	Module    string  // the app's module path
	Namespace string  // "admin", or "" for none
	Table     string  // "membership_options"
	Package   string  // "membershipoptions": the folder's package name
	Param     string  // "membership_option": the form's key
	Model     string  // "MembershipOption": sqlc's type
	Plural    string  // "MembershipOptions": in the query names
	Title     string  // "Membership options"
	Singular  string  // "Membership option"
	Fields    []Field // the form's, in order: columns and photos
	Columns   []Field // the table's: every field but photos
	Photos    []Field // photo slots (Active Storage attachments), not columns
	List      []Field // the list's columns: the first two that read as text
	Position  bool
	Dates     bool
	Base      string // "/admin/membership_options"
}

var columnName = regexp.MustCompile(`\A[a-z][a-z0-9_]*\z`)

// parseResource reads `[namespace/]table field:type[:required]...`.
func parseResource(root string, args []string) (Resource, error) {
	if len(args) < 2 {
		return Resource{}, errors.New("a table and at least one field")
	}
	var r Resource
	name := args[0]
	if ns, table, ok := strings.Cut(name, "/"); ok {
		r.Namespace, name = ns, table
	}
	if !columnName.MatchString(name) {
		return r, fmt.Errorf("%q isn't a table name", name)
	}
	r.Table = name
	r.Param = singularize(name)
	r.Package = strings.ReplaceAll(name, "_", "")
	renames := sqlcRenames(root)
	r.Model = goName(r.Param, renames)
	r.Plural = goName(name, renames)
	r.Title = Humanize(name)
	r.Singular = Humanize(r.Param)
	for _, spec := range args[1:] {
		parts := strings.Split(spec, ":")
		if len(parts) < 2 || !columnName.MatchString(parts[0]) {
			return r, fmt.Errorf("%q isn't column:type", spec)
		}
		f := Field{Column: parts[0], Type: parts[1], Go: goName(parts[0], renames), Label: Humanize(parts[0])}
		switch f.Type {
		case "string", "text", "int", "date", "photo":
		case "position":
			r.Position = true
		default:
			return r, fmt.Errorf("%s: type %q isn't string, text, int, position, date or photo", f.Column, f.Type)
		}
		if f.Type == "date" {
			r.Dates = true
		}
		for _, mod := range parts[2:] {
			if mod != "required" {
				return r, fmt.Errorf("%s: %q isn't a modifier (required is)", f.Column, mod)
			}
			f.Required = true
		}
		r.Fields = append(r.Fields, f)
		if f.Type == "photo" {
			r.Photos = append(r.Photos, f)
			continue
		}
		r.Columns = append(r.Columns, f)
		if len(r.List) < 2 && f.Type != "position" && f.Type != "int" {
			r.List = append(r.List, f)
		}
	}
	if r.Namespace != "admin" {
		return r, errors.New("resources are admin resources for now: admin/" + name)
	}
	r.Base = "/" + r.Namespace + "/" + r.Table
	mod, err := modulePath(root)
	if err != nil {
		return r, err
	}
	r.Module = mod
	return r, nil
}

func generateResource(root string, args []string, out io.Writer) error {
	r, err := parseResource(root, args)
	if err != nil {
		return err
	}
	folder := filepath.Join("app", r.Package)
	if r.Namespace != "" {
		folder = filepath.Join("app", r.Namespace, r.Package)
	}
	files := map[string]string{
		filepath.Join("app", "models", r.Table+".sql"): "resource/model.sql.tmpl",
		filepath.Join("app", "models", r.Table+".go"):  "resource/model.go.tmpl",
		filepath.Join(folder, "controller.go"):         "resource/controller.go.tmpl",
		filepath.Join(folder, "index.templ"):           "resource/index.templ.tmpl",
		filepath.Join(folder, "form.templ"):            "resource/form.templ.tmpl",
	}
	for path := range files {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			return fmt.Errorf("%s is there already: move it aside to generate it again", path)
		}
	}
	t := template.Must(template.New("").Funcs(funcs(r)).ParseFS(templates, "templates/resource/*.tmpl"))
	for path, name := range files {
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, filepath.Base(name), r); err != nil {
			return err
		}
		if err := write(filepath.Join(root, path), b.Bytes()); err != nil {
			return err
		}
		fmt.Fprintln(out, "  wrote", path)
	}
	// Its table, as a migration: every field but photos, which aren't
	// columns. Required is the form's rule (the model's Errors), not NOT
	// NULL: an unset number or date stays NULL, as the forms read them.
	cols := []string{"create_" + r.Table}
	for _, f := range r.Columns {
		cols = append(cols, f.Column+":"+f.Type)
	}
	if err := generateMigration(root, cols, out); err != nil {
		return err
	}
	base, pkg := r.Base, r.Package
	fmt.Fprintf(out, `
Next:
  1. In app/routes.go, import %s/%s and add:
       rt.Resources(%q, %s.Controller{Controller: adm}, requireAdmin)
  2. Migrate, then regenerate: gantry db migrate, sqlc generate, and templ generate.
  3. Finish it by hand: its rules (app/models/%s.go), labels, hints and choices.
`, r.Module, filepath.ToSlash(folder), base, pkg, r.Table)
	return nil
}

// modulePath is the app's module, from its go.mod.
func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", errors.New("no go.mod here: run gantry at the app's root")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", errors.New("go.mod names no module")
}

// sqlcRenames are sqlc.yaml's rename entries ("cta: CTA"), which change the
// names sqlc gives fields, so the generated code uses them too.
func sqlcRenames(root string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(filepath.Join(root, "sqlc.yaml"))
	if err != nil {
		return out
	}
	in := false
	indent := -1
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		lead := len(line) - len(strings.TrimLeft(line, " "))
		if trimmed == "rename:" {
			in, indent = true, lead
			continue
		}
		if in {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if lead <= indent {
				in = false
				continue
			}
			if k, v, ok := strings.Cut(trimmed, ":"); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// goName is sqlc's name for a column or table: CamelCase, "id" as ID, unless
// sqlc.yaml renames it.
func goName(s string, renames map[string]string) string {
	if r, ok := renames[s]; ok {
		return r
	}
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "id" {
			b.WriteString("ID")
			continue
		}
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// singularize is enough of Rails' for table names: "categories" is
// "category", "steps" is "step", "addresses" is "address".
func singularize(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "shes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "xes"):
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "ss"):
		return s
	case strings.HasSuffix(s, "s"):
		return strings.TrimSuffix(s, "s")
	}
	return s
}

// Humanize is Rails' String#humanize: underscores to spaces, the first
// letter capitalized.
func Humanize(s string) string {
	s = strings.ReplaceAll(strings.TrimSuffix(s, "_id"), "_", " ")
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// funcs are the templates' helpers: how each type of field is read, shown,
// listed and saved.
func funcs(r Resource) template.FuncMap {
	v := lowerFirst(goName(r.Param, nil)) // "membershipOption": the record's variable
	if r.Model != goName(r.Param, nil) {
		v = lowerFirst(r.Model)
	}
	return template.FuncMap{
		"lower": strings.ToLower,
		"recv":  func() string { return strings.ToLower(r.Model[:1]) },
		"var":   func() string { return v },
		"field": func(f Field) string {
			switch f.Type {
			case "text":
				return fmt.Sprintf("admin.F(%q, admin.Type(\"text\"))", f.Column)
			case "int":
				return fmt.Sprintf("admin.F(%q, admin.Type(\"number\"))", f.Column)
			case "position":
				return "admin.Position"
			case "date":
				return fmt.Sprintf("admin.F(%q, admin.Type(\"date\"))", f.Column)
			case "photo":
				return fmt.Sprintf("admin.F(%q, admin.Type(\"file\"))", f.Column)
			}
			return fmt.Sprintf("admin.F(%q)", f.Column)
		},
		"toText": func(f Field) string {
			switch f.Type {
			case "int", "position":
				return "admin.IntText(" + v + "." + f.Go + ")"
			case "date":
				return "admin.DateValue(" + v + "." + f.Go + ")"
			}
			return v + "." + f.Go
		},
		"fromText": func(f Field) string {
			switch f.Type {
			case "int", "position":
				return fmt.Sprintf("admin.OptionalInt(v[%q])", f.Column)
			case "date":
				return fmt.Sprintf("admin.OptionalDate(v[%q])", f.Column)
			}
			return fmt.Sprintf("v[%q]", f.Column)
		},
		"cell": func(f Field) string {
			if f.Type == "date" {
				return "admin.DateText(" + v + "." + f.Go + ")"
			}
			return "admin.ListText(" + v + "." + f.Go + ")"
		},
		"param": func(f Field) string {
			if f.Type == "date" {
				return "admin.DateParam(" + v + "." + f.Go + ")"
			}
			return v + "." + f.Go
		},
		// Dates are text on SQLite ("2006-01-02"), so they're bound as text.
		"arg": func(f Field) string {
			if f.Type == "date" {
				return "CAST(sqlc.narg(" + f.Column + ") AS TEXT)"
			}
			return "sqlc.arg(" + f.Column + ")"
		},
	}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
