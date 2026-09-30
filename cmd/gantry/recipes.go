package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"text/template"
)

// Recipe is what a recipe's templates are filled with.
type Recipe struct {
	Module   string // the app's import path: "example.com/shop", or a module's folder
	Postgres bool
	Prefix   string // api-tokens: what every token starts with
}

var tokenPrefix = regexp.MustCompile(`\A[a-z][a-z0-9]{0,15}_\z`)

// generateAPITokens is `gantry g api-tokens [--prefix PREFIX]`: named API
// tokens, written into the app (the api_tokens table, its queries, the
// apitokens package with Issue and the Require filter, and their tests).
func generateAPITokens(root string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("api-tokens", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	prefix := fs.String("prefix", "api_", "what every token starts with, so a leaked one is recognisable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !tokenPrefix.MatchString(*prefix) {
		return fmt.Errorf("--prefix %q: lowercase letters and digits, starting with a letter, ending in _ (hou_)", *prefix)
	}
	mod, err := appModule(root)
	if err != nil {
		return err
	}
	rc := Recipe{Module: mod, Postgres: sqlcEngine(root) == "postgresql", Prefix: *prefix}
	if existing, _ := filepath.Glob(filepath.Join(root, "db", "migrations", "*_create_api_tokens.sql")); len(existing) > 0 {
		return fmt.Errorf("%s is there already: the recipe was applied", relTo(root, existing[0]))
	}
	files := map[string]string{
		filepath.Join("db", "migrations", now().UTC().Format("20060102150405")+"_create_api_tokens.sql"): "migration.sql.tmpl",
		filepath.Join("app", "models", "api_tokens.sql"):                                                 "queries.sql.tmpl",
		filepath.Join("app", "apitokens", "apitokens.go"):                                                "apitokens.go.tmpl",
		filepath.Join("app", "apitokens", "apitokens_test.go"):                                           "apitokens_test.go.tmpl",
	}
	if err := writeRecipe(root, "apitokens", files, rc, out); err != nil {
		return err
	}
	fmt.Fprintf(out, `
Next:
  1. gantry db migrate, then gantry exec sqlc generate (the queries in app/models).
  2. In app/routes.go, put the API behind it:
       api := web.Pipeline{web.AcceptJSON, apitokens.Require(a.DB, time.Now)}
       rt.Scope("/api/v1", api, func(s *web.Scope) { ... })
  3. Issue and revoke tokens on a settings page of the app's own:
     apitokens.Issue, models.ListAPITokens, models.DeleteAPIToken.
`)
	return nil
}

// writeRecipe renders a recipe's templates (templates/<dir>) to files,
// refusing any that's there.
func writeRecipe(root, dir string, files map[string]string, data any, out io.Writer) error {
	for path := range files {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			return fmt.Errorf("%s is there already: move it aside to apply the recipe", path)
		}
	}
	t := template.Must(template.ParseFS(templates, "templates/"+dir+"/*.tmpl"))
	for path, name := range files {
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, name, data); err != nil {
			return err
		}
		if err := write(filepath.Join(root, path), b.Bytes()); err != nil {
			return err
		}
		fmt.Fprintln(out, "  wrote", filepath.ToSlash(path))
	}
	return nil
}

// appModule is the app's import path: its go.mod's module, or, for an app
// in a folder of a module (gantry new --in-module), the module's path and
// the folder.
func appModule(root string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, "cmd")); err != nil {
		return "", errors.New("no cmd/ here: run gantry at the app's root")
	}
	modRoot, path, err := enclosingModule(root)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(root)
	rel, err := filepath.Rel(modRoot, abs)
	if err != nil || rel == "." {
		return path, err
	}
	return path + "/" + filepath.ToSlash(rel), nil
}

func relTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// generateAuth is `gantry g auth`: sign-in with email and password, written
// into the app (Rails 8's authentication generator): the users and sessions
// tables, their queries, the auth package (sessions with Idle and Lifetime,
// the Fetch and Require filters, sign in and out, password reset by email),
// its pages in the app's layout, and its tests.
func generateAuth(root string, args []string, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("g auth takes no arguments (%q)", args[0])
	}
	mod, err := appModule(root)
	if err != nil {
		return err
	}
	if existing, _ := filepath.Glob(filepath.Join(root, "db", "migrations", "*_create_users_and_sessions.sql")); len(existing) > 0 {
		return fmt.Errorf("%s is there already: the recipe was applied", relTo(root, existing[0]))
	}
	rc := Recipe{Module: mod, Postgres: sqlcEngine(root) == "postgresql"}
	files := map[string]string{
		filepath.Join("db", "migrations", now().UTC().Format("20060102150405")+"_create_users_and_sessions.sql"): "migration.sql.tmpl",
		filepath.Join("app", "models", "users.sql"):                                                              "users.sql.tmpl",
		filepath.Join("app", "models", "sessions.sql"):                                                           "sessions.sql.tmpl",
		filepath.Join("app", "auth", "auth.go"):                                                                  "auth.go.tmpl",
		filepath.Join("app", "auth", "controller.go"):                                                            "controller.go.tmpl",
		filepath.Join("app", "auth", "pages.templ"):                                                              "pages.templ.tmpl",
		filepath.Join("app", "auth", "auth_test.go"):                                                             "auth_test.go.tmpl",
	}
	if err := writeRecipe(root, "auth", files, rc, out); err != nil {
		return err
	}
	fmt.Fprintf(out, `
Next:
  1. gantry db migrate, then gantry exec sqlc generate, gantry exec templ generate
     and gantry exec go mod tidy (it uses bcrypt).
  2. In app/routes.go, give App an Auth and mount it:
       a.Auth = &auth.Auth{DB: a.DB, Signer: a.Signer, Mail: mail.Log{Logger: a.Log},
           From: "app@example.com", URL: func(p string) string { return "https://example.com" + p },
           Limits: &web.Limits{}, Log: a.Log}  // Idle, Lifetime: when sessions end
       a.Auth.Routes(rt)
       rt.Scope("/admin", web.Pipeline{a.Auth.Require}, func(s *web.Scope) { ... })
  3. The first user, from a task in app/tasks.go: a.Auth.CreateUser(ctx, email, password).
`)
	return nil
}

// generateStorage is `gantry g storage`: Active Storage's tables, as `rails
// active_storage:install` writes them, for gantry's storage package.
func generateStorage(root string, args []string, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("g storage takes no arguments (%q)", args[0])
	}
	if _, err := appModule(root); err != nil {
		return err
	}
	if existing, _ := filepath.Glob(filepath.Join(root, "db", "migrations", "*_create_active_storage_tables.sql")); len(existing) > 0 {
		return fmt.Errorf("%s is there already: the recipe was applied", relTo(root, existing[0]))
	}
	rc := Recipe{Postgres: sqlcEngine(root) == "postgresql"}
	files := map[string]string{
		filepath.Join("db", "migrations", now().UTC().Format("20060102150405")+"_create_active_storage_tables.sql"): "migration.sql.tmpl",
	}
	if err := writeRecipe(root, "storage", files, rc, out); err != nil {
		return err
	}
	fmt.Fprintf(out, `
Next:
  1. gantry db migrate.
  2. In cmd/<app>/main.go, before anything else, let the binary be its own
     resizing child (pictures' copies are made in one, out of the server's way):
       pictures := &images.Pipeline{}
       if images.IsChild(os.Args) {
           os.Exit(pictures.RunChild(os.Args))
       }
       exe, _ := os.Executable()
       pictures.Maker = images.Child{Exe: exe}
  3. Give App a Storage, on the data volume, and serve it:
       a.Storage = &storage.Storage{DB: a.DB, Root: filepath.Join(dataDir, "storage"),
           Images: pictures, Log: a.Log}
       a.Storage.WarmLater()  // the copies not made yet
       rt.Mount("GET /storage/", a.Storage)
  4. Attach a form's file to a record, and draw it:
       f, ok := storage.FileFrom(r, "clip[thumbnail]")
       a.Storage.Attach(ctx, storage.Ref{RecordType: "Clip", RecordID: id, Name: "thumbnail"}, f)
       @a.Storage.Img(blob, images.Img{Alt: "…", Sizes: "240px"})
`)
	return nil
}
