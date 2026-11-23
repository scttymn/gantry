package devcheck

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Each rule's bypass is reported where it is, at the rule's severity, its
// recommended pattern and legitimate alternatives aren't, and an exception
// for the file silences it.
func TestRules(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		files      map[string]string
		want       []string // the rule's findings, file:line
	}{
		{"models import the web", "import-boundary", map[string]string{"app/models/posts.go": `package models

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"example.com/shop/app/shared/ui"
)
`}, []string{"app/models/posts.go:4", "app/models/posts.go:7", "app/models/posts.go:9", "app/models/posts.go:11"}},
		{"services and shared import resources", "import-boundary", map[string]string{
			"app/services/photos/photos.go": "package photos\n\nimport (\n\t\"github.com/scttymn/gantry/web\"\n\t\"example.com/shop/app/posts\"\n\t\"example.com/shop/app/models\"\n)\n",
			"app/shared/ui/card.templ":      "package ui\n\nimport \"example.com/shop/app/admin/events\"\n\ntempl card(e events.Row) {\n\t<p>{ e.Name }</p>\n}\n",
		}, []string{"app/services/photos/photos.go:4", "app/services/photos/photos.go:5", "app/shared/ui/card.templ:3"}},
		{"a nested resource imports its parent, a resource the routes", "import-boundary", map[string]string{
			"app/posts/comments/controller.go": "package comments\n\nimport \"example.com/shop/app/posts\"\n",
			"app/home/controller.go":           "package home\n\nimport shop \"example.com/shop/app\"\n",
		}, []string{"app/home/controller.go:3", "app/posts/comments/controller.go:3"}},
		{"imports that go one way", "import-boundary", map[string]string{
			"app/routes.go":                  "package app\n\nimport (\n\t\"example.com/shop/app/home\"\n\t\"example.com/shop/app/posts/comments\"\n)\n",
			"app/home/controller.go":         "package home\n\nimport (\n\t\"github.com/scttymn/gantry/web\"\n\t\"example.com/shop/app/posts\"\n\t\"example.com/shop/app/models\"\n\t\"example.com/shop/app/services/photos\"\n\t\"example.com/shop/app/shared/layout\"\n)\n",
			"app/posts/controller.go":        "package posts\n\nimport \"example.com/shop/app/posts/comments\"\n",
			"app/services/photos/photos.go":  "package photos\n\nimport (\n\t\"github.com/scttymn/gantry/db\"\n\t\"example.com/shop/app/models\"\n\t\"example.com/shop/app/services/store\"\n)\n",
			"app/shared/resource/pages.go":   "package resource\n\nimport (\n\t\"github.com/a-h/templ\"\n\t\"github.com/scttymn/gantry/web\"\n\t\"example.com/shop/app/shared/layout\"\n)\n",
			"app/models/posts.go":            "package models\n\nimport (\n\t\"database/sql\"\n\t\"github.com/scttymn/gantry/db\"\n)\n",
			"cmd/shop/main.go":               "package main\n\nimport \"example.com/shop/app\"\n",
			"app/posts/index.templ":          "package posts\n\nimport \"example.com/shop/app/models\"\n\ntempl index(list []models.Post) {\n\t<ul></ul>\n}\n",
			"vendor/x/app/models/m.go":       "package models\n\nimport \"net/http\"\n",
			"app/posts/controller_test.go":   "package posts\n\nimport \"example.com/shop/app\"\n",
			"app/admin/events/controller.go": "package events\n\nimport \"example.com/shop/app/admin/staff\"\n",
			"app/services/signup/signup.go":  "package signup\n\nimport \"example.com/shop/app/mailers\"\n",
			"app/shared/ui/flash.go":         "package ui\n\nimport \"example.com/shop/app/jobs\"\n",
		}, nil},

		{"a view loads its data", "view-db", map[string]string{"app/posts/index.templ": `package posts

import (
	m "example.com/shop/app/models"
	"github.com/scttymn/gantry/db"
)

templ index(d *db.DB) {
	for _, p := range list(m.New(d.Read)) {
		<li>{ p.Title }</li>
	}
}

templ show(q *m.Queries, c Controller) {
	{ c.DB.Write }
}
`}, []string{"app/posts/index.templ:8", "app/posts/index.templ:9", "app/posts/index.templ:14", "app/posts/index.templ:15"}},
		{"a fragment that isn't templ yet", "view-db", map[string]string{
			"app/posts/draft.templ": "<p>{ models.New(c.DB.Read) }</p>\n",
			"app/posts/notes.go":    "package posts\n\nfunc notes() string { return `templ index() { models.New(x) }` }\n",
		}, nil},
		{"a view draws plain values", "view-db", map[string]string{"app/posts/index.templ": `package posts

import "example.com/shop/app/models"

// Read the newest first; New posts are marked.
templ index(list []models.Post, newest models.Post) {
	<h1>New posts: read the DB less</h1>
	for _, p := range list {
		<li>{ p.Title }</li>
	}
}
`}, nil},

		{"unescaped HTML", "raw-html", map[string]string{
			"app/posts/show.templ": "package posts\n\ntempl show(body string) {\n\t@templ.Raw(body)\n}\n",
			"app/posts/body.go":    "package posts\n\nimport t \"github.com/a-h/templ\"\n\nfunc body(s string) t.Component { return t.Raw(s) }\nfunc js(s string) t.ComponentScript { return t.JSUnsafeFuncCall(s) }\n",
		}, []string{"app/posts/body.go:5", "app/posts/body.go:6", "app/posts/show.templ:4"}},
		{"escaped HTML", "raw-html", map[string]string{
			"app/posts/show.templ": "package posts\n\n// templ.Raw would skip escaping.\ntempl show(body, link string) {\n\t<a href={ templ.URL(link) }>{ body }</a>\n}\n",
			"app/posts/body.go":    "package posts\n\nimport \"github.com/a-h/templ\"\n\n// Raw is not templ.Raw.\nfunc Raw(s string) templ.SafeURL { return templ.URL(s) }\n",
		}, nil},

		{"the environment read in a controller", "env-outside-config", map[string]string{"app/posts/controller.go": `package posts

import "os"

func key() string {
	if v, ok := os.LookupEnv("KEY"); ok {
		return v
	}
	return os.Getenv("OTHER_KEY")
}
`}, []string{"app/posts/controller.go:6", "app/posts/controller.go:9"}},
		{"the environment read in config", "env-outside-config", map[string]string{
			"config/config.go":        "package config\n\nimport \"os\"\n\nfunc FromEnv() string { return os.Getenv(\"KEY\") }\n",
			"app/posts/controller.go": "package posts\n\nimport \"example.com/shop/config\"\n\nfunc key(c config.Config) string { return c.Key }\n",
		}, nil},

		{"work outliving the request", "background-work", map[string]string{"app/posts/controller.go": `package posts

import "time"

func notify() {
	go send()
	go func() { send() }()
	time.Sleep(time.Second)
	time.AfterFunc(time.Minute, send)
}
`}, []string{"app/posts/controller.go:6", "app/posts/controller.go:7", "app/posts/controller.go:8", "app/posts/controller.go:9"}},
		{"jobs, and work the request waits for", "background-work", map[string]string{
			"app/posts/controller.go": "package posts\n\nimport \"sync\"\n\nfunc load(a, b func()) {\n\tvar wg sync.WaitGroup\n\twg.Go(a)\n\twg.Go(b)\n\twg.Wait()\n}\n",
			"cmd/shop/main.go":        "package main\n\nfunc main() {\n\tgo func() { serve() }()\n}\n",
		}, nil},

		{"requests with no timeout", "http-client", map[string]string{"app/services/feed/feed.go": `package feed

import h "net/http"

func fetch(u string, req *h.Request) {
	h.Get(u)
	h.Post(u, "text/plain", nil)
	h.DefaultClient.Do(req)
}
`}, []string{"app/services/feed/feed.go:6", "app/services/feed/feed.go:7", "app/services/feed/feed.go:8"}},
		{"a client with a timeout", "http-client", map[string]string{"app/services/feed/feed.go": "package feed\n\nimport (\n\t\"net/http\"\n\t\"time\"\n)\n\nvar client = &http.Client{Timeout: 10 * time.Second}\n\nfunc fetch(u string) { client.Get(u) }\n"}, nil},

		{"raw SQL in a controller", "sql-outside-models", map[string]string{"app/posts/controller.go": `package posts

import (
	"database/sql"

	"github.com/scttymn/gantry/db"
)

func (c Controller) Index(d *sql.DB) error {
	c.DB.Read.QueryContext(ctx, "SELECT 1")
	sql.Open("sqlite", "app.db")
	return c.DB.Tx(ctx, func(tx *db.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM posts")
		return err
	})
}
`}, []string{"app/posts/controller.go:9", "app/posts/controller.go:10", "app/posts/controller.go:11", "app/posts/controller.go:13"}},
		{"queries through models", "sql-outside-models", map[string]string{
			"app/posts/controller.go": `package posts

import (
	"database/sql"
	"errors"

	"github.com/scttymn/gantry/db"

	"example.com/shop/app/models"
)

func (c Controller) Show(on sql.NullTime) error {
	_, err := models.New(c.DB.Read).GetPost(ctx, 1)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return c.DB.Tx(ctx, func(tx *db.Tx) error { return models.New(tx).TouchPost(ctx, 1) })
}
`,
			"app/models/search.go": "package models\n\nimport \"database/sql\"\n\nfunc search(d *sql.DB) { d.QueryContext(ctx, \"SELECT 1\") }\n",
		}, nil},

		{"a cross-origin bypass", "cross-origin-bypass", map[string]string{"app/routes.go": `package app

import "github.com/scttymn/gantry/web"

func wrap(h http.Handler, patterns []string) http.Handler {
	h = web.CrossOrigin(h, deny, "POST /callback")
	return web.CrossOrigin(h, deny, patterns...)
}
`}, []string{"app/routes.go:6", "app/routes.go:7"}},
		{"a callback that checks its own proof", "cross-origin-bypass", map[string]string{"app/routes.go": "package app\n\nimport \"github.com/scttymn/gantry/web\"\n\nfunc routes(rt *web.Router, h http.Handler) http.Handler {\n\trt.CrossOriginCallback(\"POST /auth/apple\", apple)\n\treturn web.CrossOrigin(h, deny)\n}\n"}, nil},

		{"a transaction a panic leaves open", "tx-defer", map[string]string{"jobs/claim.go": `package jobs

func claim(d *db.DB) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE jobs SET state = 'claimed'"); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
`}, []string{"jobs/claim.go:4"}},
		{"transactions that always end", "tx-defer", map[string]string{"jobs/claim.go": `package jobs

func claim(d *db.DB) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return tx.Commit()
}

func settle(d *db.DB) error {
	tx, err := d.Write.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return tx.Commit()
}

func parse(p *parser) {
	n, err := p.Begin()
	use(n, err)
}

func tidy(d *db.DB) error { return d.Tx(ctx, func(tx *db.Tx) error { return nil }) }
`}, nil},

		{"read on one pool, written on the other", "read-then-write", map[string]string{"app/posts/controller.go": `package posts

import "example.com/shop/app/models"

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	post, err := models.New(c.DB.Read).GetPost(r.Context(), 1)
	if err != nil {
		return err
	}
	return models.New(c.DB.Write).UpdatePost(r.Context(), post.ID)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request) error {
	read, write := models.New(c.DB.Read), models.New(c.DB.Write)
	post, err := read.GetPost(r.Context(), 1)
	if err != nil {
		return err
	}
	return write.DeletePost(r.Context(), post.ID)
}
`}, []string{"app/posts/controller.go:10", "app/posts/controller.go:19"}},
		{"read and written in one transaction", "read-then-write", map[string]string{"app/posts/controller.go": `package posts

import "example.com/shop/app/models"

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	return c.DB.Tx(r.Context(), func(tx *db.Tx) error {
		q := models.New(tx)
		post, err := q.GetPostForUpdate(r.Context(), 1)
		if err != nil {
			return err
		}
		return q.UpdatePost(r.Context(), post.ID)
	})
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Write)
	post, err := q.GetPost(r.Context(), 1)
	if err != nil {
		return err
	}
	return q.DeletePost(r.Context(), post.ID)
}

func (c Controller) Edit(w http.ResponseWriter, r *http.Request) error {
	models.New(c.DB.Write).UpdatePost(r.Context(), 1)
	_, err := models.New(c.DB.Read).GetPost(r.Context(), 1)
	return err
}
`}, nil},

		{"a GET that writes", "get-mutates", map[string]string{
			"app/posts/controller.go": `package posts

import (
	"net/http"

	"example.com/shop/app/models"
)

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	return models.New(c.DB.Write).TouchPost(r.Context(), 1)
}
`,
			"app/routes.go": `package app

import "example.com/shop/app/models"

func (a *App) Router() *web.Router {
	rt.Handle("GET /logout", func(w http.ResponseWriter, r *http.Request) error {
		return a.DB.Tx(r.Context(), func(tx *db.Tx) error { return end(tx) })
	})
	rt.Handle("/reset", func(w http.ResponseWriter, r *http.Request) error {
		return models.New(a.DB.Write).Reset(r.Context())
	})
	return rt
}
`,
		}, []string{"app/posts/controller.go:10", "app/routes.go:7", "app/routes.go:10"}},
		{"writes on POST, reads on GET", "get-mutates", map[string]string{
			"app/posts/controller.go": `package posts

import (
	"net/http"

	"example.com/shop/app/models"
)

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	_, err := models.New(c.DB.Read).GetPost(r.Context(), 1)
	return err
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	return models.New(c.DB.Write).CreatePost(r.Context(), "")
}

func (s *Store) New(d *db.DB) error { return models.New(d.Write).Reset(ctx) }
`,
			"app/routes.go": `package app

import "example.com/shop/app/models"

func (a *App) Router() *web.Router {
	rt.Handle("POST /logout", func(w http.ResponseWriter, r *http.Request) error {
		return a.DB.Tx(r.Context(), func(tx *db.Tx) error { return end(tx) })
	})
	rt.Handle("GET /posts", func(w http.ResponseWriter, r *http.Request) error {
		return models.New(a.DB.Read).Count(r.Context())
	})
	return rt
}
`,
		}, nil},

		{"an upload served inline", "inline-download", map[string]string{"app/files/controller.go": `package files

import "net/http"

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	f, info := open(r)
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	return nil
}
`}, []string{"app/files/controller.go:7"}},
		{"an upload sent as a download", "inline-download", map[string]string{"app/files/controller.go": `package files

import (
	"net/http"

	"github.com/scttymn/gantry/web"
)

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	f, info := open(r)
	w.Header().Set("Content-Disposition", "attachment")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	return nil
}

func (c Controller) Export(w http.ResponseWriter, r *http.Request) error {
	return web.Download(w, r, "posts.csv", "text/csv", csv(r))
}
`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "go.mod", "module example.com/shop\n\ngo 1.27\n")
			for name, body := range tc.files {
				put(t, root, name, body)
			}
			rules, err := validateRules(DefaultRules())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := rules[tc.rule]; !ok {
				t.Fatalf("no rule %s in DefaultRules", tc.rule)
			}
			found := func() []Warning {
				t.Helper()
				warnings, err := Scan(root)
				if err != nil {
					t.Fatal(err)
				}
				var got []Warning
				for _, w := range warnings {
					if w.Rule == tc.rule {
						got = append(got, w)
					}
				}
				return got
			}
			got := found()
			var at []string
			for _, w := range got {
				at = append(at, fmt.Sprintf("%s:%d", w.File, w.Line))
				if w.Severity != rules[tc.rule].Severity || w.Message == "" {
					t.Errorf("%+v: want severity %s and a message", w, rules[tc.rule].Severity)
				}
			}
			if !reflect.DeepEqual(at, tc.want) {
				t.Fatalf("%s findings at %v, want %v\n%+v", tc.rule, at, tc.want, got)
			}
			if len(got) == 0 {
				return
			}
			var files []string
			for _, w := range got {
				if !slices.Contains(files, w.File) {
					files = append(files, w.File)
				}
			}
			var exceptions []exception
			for _, f := range files {
				exceptions = append(exceptions, exception{Rule: tc.rule, File: f, Reason: "Reviewed in #33."})
			}
			config, _ := json.Marshal(map[string]any{"requireIssue": true, "exceptions": exceptions})
			put(t, root, ".gantry-check.json", string(config))
			if got := found(); len(got) != 0 {
				t.Fatalf("an exception for each file left %+v", got)
			}
		})
	}
}

// The rule's message names what was found and the pattern to use instead.
func TestRuleMessages(t *testing.T) {
	root := t.TempDir()
	put(t, root, "go.mod", "module example.com/shop\n")
	put(t, root, "app/models/posts.go", "package models\n\nimport \"github.com/scttymn/gantry/web\"\n")
	put(t, root, "app/routes.go", "package app\n\nimport \"example.com/shop/app/models\"\n\nfunc (a *App) Router() {\n\trt.Handle(\"GET /logout\", func(w http.ResponseWriter, r *http.Request) error {\n\t\treturn models.New(a.DB.Write).EndSession(r.Context())\n\t})\n}\n")
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"import-boundary": {"app/models imports github.com/scttymn/gantry/web", "controller"},
		"get-mutates":     {"GET /logout", "POST", "button"},
	}
	for _, w := range got {
		for _, part := range want[w.Rule] {
			if !strings.Contains(w.Message, part) {
				t.Errorf("%s: %q doesn't say %q", w.Rule, w.Message, part)
			}
		}
		delete(want, w.Rule)
	}
	if len(want) > 0 {
		t.Fatalf("no finding for %v in %+v", want, got)
	}
}

func TestModulePath(t *testing.T) {
	for gomod, want := range map[string]string{
		"module example.com/shop\n\ngo 1.27\n":       "example.com/shop",
		"// a comment\nmodule \"example.com/q\"\n":   "example.com/q",
		"module example.com/c // trailing\n":         "example.com/c",
		"modules example.com/x\n":                    "",
		"":                                           "",
		"go 1.27\nrequire (\n\tmodule.org/x v1\n)\n": "",
	} {
		if got := modulePath([]byte(gomod)); got != want {
			t.Errorf("modulePath(%q) = %q, want %q", gomod, got, want)
		}
	}
}
