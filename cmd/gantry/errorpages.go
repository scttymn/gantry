package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
)

// ErrorPage is one of the pages `g error-pages` writes, in Rails' words.
type ErrorPage struct {
	Status      int
	Title, Hint string
}

// errorPages are Rails' set: public/400.html, 404, 422 and 500.
var errorPages = []ErrorPage{
	{400, "The request couldn't be understood.", "Something in it was malformed. Try again from the page you came from."},
	{404, "The page you were looking for doesn't exist.", "You may have mistyped the address, or the page may have moved."},
	{422, "The change you wanted was rejected.", "Maybe you tried to change something you don't have access to, or the form was open too long. Go back and try again."},
	{500, "Something went wrong.", "We've been told about it. Try again in a little while."},
}

// generateErrorPages writes public/<status>.html for each of Rails' error
// pages, the app's to restyle. A page that's there is kept, unless force.
func generateErrorPages(root string, force bool, out io.Writer) error {
	t := template.Must(template.ParseFS(templates, "templates/errorpages/page.html.tmpl"))
	dir := filepath.Join(root, "public")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range errorPages {
		name := fmt.Sprintf("%d.html", p.Status)
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil && !force {
			fmt.Fprintln(out, "  kept", filepath.Join("public", name), "(--force replaces it)")
			continue
		}
		var b bytes.Buffer
		if err := t.Execute(&b, p); err != nil {
			return err
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(out, "  wrote", filepath.Join("public", name))
	}
	return nil
}
