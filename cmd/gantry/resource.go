package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"github.com/scttymn/gantry/text"
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
	Namespace string  // "admin", or "" for a top-level resource
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
	Postgres  bool   // the app's engine: its queries bind dates differently
	Base      string // "/admin/membership_options", or "/membership_options"
	Folder    string // "app/admin/membershipoptions", or "app/membershipoptions"
}

var columnName = regexp.MustCompile(`\A[a-z][a-z0-9_]*\z`)

// appFolders are the app's own folders in app/, which no resource's may be.
var appFolders = map[string]bool{"models": true, "shared": true}

// parseResource reads `[namespace/]table field:type[:required]...`.
func parseResource(root string, args []string) (Resource, error) {
	if len(args) < 2 {
		return Resource{}, errors.New("a table and at least one field")
	}
	var r Resource
	name := args[0]
	if ns, table, ok := strings.Cut(name, "/"); ok {
		if !columnName.MatchString(ns) {
			return r, fmt.Errorf("%q isn't a namespace", ns)
		}
		r.Namespace, name = ns, table
	}
	if !columnName.MatchString(name) {
		return r, fmt.Errorf("%q isn't a table name", name)
	}
	r.Table = name
	r.Param = text.Singularize(name)
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
	r.Base, r.Folder = "/"+r.Table, "app/"+r.Package
	top := r.Package // its folder in app/
	if r.Namespace != "" {
		r.Base = "/" + r.Namespace + r.Base
		r.Folder = "app/" + r.Namespace + "/" + r.Package
		top = r.Namespace
	}
	if appFolders[top] {
		return r, fmt.Errorf("app/%s is the app's own folder: name the resource something else", top)
	}
	r.Postgres = sqlcEngine(root) == "postgresql"
	mod, err := modulePath(root)
	if err != nil {
		return r, err
	}
	r.Module = mod
	return r, nil
}

// sharedFiles are the helpers every resource's controller uses, in the
// app's app/shared/resource: written by the first resource and the app's
// from then on, so they're never written over.
var sharedFiles = map[string]string{
	"app/shared/resource/resource.go": "resource.go.tmpl",
	"app/shared/resource/pages.templ": "pages.templ.tmpl",
}

func generateResource(root string, args []string, out io.Writer) error {
	r, err := parseResource(root, args)
	if err != nil {
		return err
	}
	files := map[string]string{
		"app/models/" + r.Table + ".sql": "model.sql.tmpl",
		"app/models/" + r.Table + ".go":  "model.go.tmpl",
		r.Folder + "/controller.go":      "controller.go.tmpl",
		r.Folder + "/index.templ":        "index.templ.tmpl",
		r.Folder + "/form.templ":         "form.templ.tmpl",
	}
	for path := range files {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			return fmt.Errorf("%s is there already: move it aside to generate it again", path)
		}
	}
	t := template.Must(template.New("").Funcs(funcs(r)).ParseFS(templates, "templates/resource/*.tmpl", "templates/resource/shared/*.tmpl"))
	render := func(path, name string) error {
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, name, r); err != nil {
			return err
		}
		if err := write(filepath.Join(root, path), b.Bytes()); err != nil {
			return err
		}
		fmt.Fprintln(out, "  wrote", path)
		return nil
	}
	for path, name := range files {
		if err := render(path, name); err != nil {
			return err
		}
	}
	for path, name := range sharedFiles {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			fmt.Fprintln(out, "  kept", path)
			continue
		}
		if err := render(path, name); err != nil {
			return err
		}
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
	fmt.Fprint(out, nextSteps(r))
	return nil
}

// nextSteps are what wires a resource into the app, as code to paste: a
// top-level one in Handler (Rails' resources :posts), a namespaced one in
// its namespace's scope (namespace :admin), behind the filter that guards
// it, which the generator can't know.
func nextSteps(r Resource) string {
	shared := "DB: a.DB, Flash: web.Flash{Signer: a.Signer}"
	if len(r.Photos) > 0 {
		shared += ", Photos: a.Storage"
	}
	route := fmt.Sprintf(`     and route it, in Handler:
       rt.Resources(%q, %s.Controller{Controller: resource.Controller{
           %s}}, nil)
`, r.Base, r.Package, shared)
	if r.Namespace != "" {
		route = fmt.Sprintf(`     and route it in Handler, in /%[1]s's scope (one for every %[1]s resource):
       rt.Scope("/%[1]s", web.Pipeline{}, func(s *web.Scope) {
           s.Resources("/%[2]s", %[3]s.Controller{Controller: resource.Controller{
               %[4]s}}, nil)
       })
     The scope's pipeline is who may use /%[1]s: until it has a filter (g auth's
     a.Auth.Required, once App has an Auth), anyone can.
`, r.Namespace, r.Table, r.Package, shared)
	}
	if len(r.Photos) > 0 {
		route += "     Its photos are kept in App's Storage, a.Storage: g storage and its next\n     steps give App one.\n"
	}
	return fmt.Sprintf(`
Next:
  1. In app/routes.go, import
       "%s/%s"
       "%s/app/shared/resource"
%s  2. gantry db migrate, then gantry exec sqlc generate, gantry exec templ generate
     and gantry exec go mod tidy.
  3. Finish it by hand: its rules (app/models/%s.go), labels, hints and choices.
`, r.Module, r.Folder, r.Module, route, r.Table)
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

// controllerNames are the names a resource's controller declares or
// imports, which its record's variable would shadow (a resources table's
// "resource" is the shared package's name).
var controllerNames = map[string]bool{
	"resource": true, "models": true, "web": true, "templ": true, "http": true, "strings": true,
	"path": true, "param": true, "title": true, "singular": true, "recordType": true, "fields": true,
}

// funcs are the templates' helpers: how each type of field is read, shown,
// listed and saved.
func funcs(r Resource) template.FuncMap {
	v := lowerFirst(goName(r.Param, nil)) // "membershipOption": the record's variable
	if r.Model != goName(r.Param, nil) {
		v = lowerFirst(r.Model)
	}
	if controllerNames[v] {
		v = "record"
	}
	return template.FuncMap{
		"lower": strings.ToLower,
		"recv":  func() string { return strings.ToLower(r.Model[:1]) },
		"var":   func() string { return v },
		"field": func(f Field) string {
			switch f.Type {
			case "text":
				return fmt.Sprintf("resource.F(%q, resource.Type(\"textarea\"))", f.Column)
			case "int":
				return fmt.Sprintf("resource.F(%q, resource.Type(\"number\"))", f.Column)
			case "position":
				return "resource.Position"
			case "date":
				return fmt.Sprintf("resource.F(%q, resource.Type(\"date\"))", f.Column)
			case "photo":
				return fmt.Sprintf("resource.F(%q, resource.Type(\"file\"))", f.Column)
			}
			return fmt.Sprintf("resource.F(%q)", f.Column)
		},
		"toText": func(f Field) string {
			switch f.Type {
			case "int", "position":
				return "resource.IntText(" + v + "." + f.Go + ")"
			case "date":
				return "resource.DateText(" + v + "." + f.Go + ")"
			}
			return v + "." + f.Go
		},
		"fromText": func(f Field) string {
			switch f.Type {
			case "int", "position":
				return fmt.Sprintf("resource.OptionalInt(v[%q])", f.Column)
			case "date":
				return fmt.Sprintf("resource.OptionalDate(v[%q])", f.Column)
			}
			return fmt.Sprintf("v[%q]", f.Column)
		},
		"cell": func(f Field) string {
			if f.Type == "date" {
				return "resource.DateText(" + v + "." + f.Go + ")"
			}
			return "resource.ListText(" + v + "." + f.Go + ")"
		},
		"param": func(f Field) string {
			if f.Type == "date" {
				return "resource.DateParam(" + v + "." + f.Go + ")"
			}
			return v + "." + f.Go
		},
		// Dates are bound as text ("2006-01-02"), as SQLite keeps them (its
		// CAST AS DATE would make "2026-11-22" the number 2026). Postgres
		// won't put text in a date column, so there the text is cast to one.
		"arg": func(f Field) string {
			if f.Type != "date" {
				return "sqlc.arg(" + f.Column + ")"
			}
			if r.Postgres {
				return "CAST(CAST(sqlc.narg(" + f.Column + ") AS TEXT) AS DATE)"
			}
			return "CAST(sqlc.narg(" + f.Column + ") AS TEXT)"
		},
	}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
