package devcheck

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
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
			"app/services/signup/signup.go":  "package signup\n\nimport (\n\t\"example.com/shop/app/mailers\"\n\t\"example.com/shop/app/shared/ui\"\n)\n",
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
			"app/posts/note.templ": "package posts\n\nimport t \"github.com/a-h/templ\"\n\ntempl note(body string) {\n\t@t.Raw(body)\n}\n",
		}, []string{"app/posts/body.go:5", "app/posts/body.go:6", "app/posts/note.templ:6", "app/posts/show.templ:4"}},
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
`, "app/routes.go": `package app

import "example.com/shop/app/models"

func (a *App) Router() *web.Router {
	rt.Handle("PATCH /posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
		post, _ := models.New(a.DB.Read).GetPost(r.Context(), 1)
		return models.New(a.DB.Write).UpdatePost(r.Context(), post.ID)
	})
	return rt
}
`}, []string{"app/posts/controller.go:10", "app/posts/controller.go:19", "app/routes.go:8"}},
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

func (c Controller) Publish(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Write)
	if _, err := models.New(c.DB.Read).GetPost(r.Context(), 1); err != nil {
		return err
	}
	return c.DB.Tx(r.Context(), func(tx *db.Tx) error {
		q = models.New(tx)
		post, err := q.GetPostForUpdate(r.Context(), 1)
		if err != nil {
			return err
		}
		return q.UpdatePost(r.Context(), post.ID)
	})
}
`, "app/routes.go": `package app

import "example.com/shop/app/models"

func (a *App) Router() *web.Router {
	rt.Handle("GET /posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
		_, err := models.New(a.DB.Read).GetPost(r.Context(), 1)
		return err
	})
	rt.Handle("DELETE /posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
		return models.New(a.DB.Write).DeletePost(r.Context(), 1)
	})
	return rt
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
`, "app/routes.go": `package app

import "net/http"

func (a *App) Router() *web.Router {
	rt.Handle("GET /exports/{name}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Disposition", "attachment")
		http.ServeContent(w, r, "export.csv", a.Started, export(r))
		return nil
	})
	rt.Handle("GET /uploads/{key}", func(w http.ResponseWriter, r *http.Request) error {
		http.ServeContent(w, r, "upload", a.Started, upload(r))
		return nil
	})
	return rt
}
`}, []string{"app/files/controller.go:7", "app/routes.go:12"}},
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

		{"a cached page reads who's visiting", "cached-scope", withCachedApp(map[string]string{"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/web"

	"example.com/shop/app/home"
	"example.com/shop/app/posts"
)

type App struct{ Auth *auth.Auth }

func (a *App) Router() *web.Router {
	rt := web.NewRouter(nil, nil)
	homePage := home.Controller{}
	rt.Cached("GET /{$}", homePage.Show)
	rt.Cached("GET /about", func(w http.ResponseWriter, r *http.Request) error {
		theme, _ := r.Cookie("theme")
		return render(w, theme.Value)
	})
	rt.Scope("/docs", nil, func(s *web.Scope) {
		s.Cached("GET /{$}", a.docs)
	})
	rt.Resources("/posts", posts.Controller{}, rt.Cache.Handler)
	feeds := &web.PageCache{}
	rt.Mount("GET /feed", feeds.Handler(http.HandlerFunc(feed)))
	return rt
}

func (a *App) docs(w http.ResponseWriter, r *http.Request) error {
	if a.Auth.SignedIn(r) {
		return render(w, "Welcome back")
	}
	return render(w, "Docs")
}

func feed(w http.ResponseWriter, r *http.Request) {
	seen, _ := web.SignedCookie(r, signer, "seen")
	render(w, seen)
}
`}), []string{"app/routes.go:18", "app/routes.go:19", "app/routes.go:24", "app/routes.go:26", "app/routes.go:28"}},
		{"a cached page the same for everyone", "cached-scope", withCachedApp(map[string]string{"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/web"

	"example.com/shop/app/home"
	"example.com/shop/app/posts"
)

func (a *App) Router() *web.Router {
	rt := web.NewRouter(nil, nil)
	homePage := home.Controller{}
	rt.Cached("GET /about", homePage.About)
	rt.Handle("GET /{$}", homePage.Show)
	rt.Resources("/posts", posts.Controller{}, nil)
	rt.Cached("GET /live", a.Live.Serve)
	rt.Cached("GET /page", a.page())
	rt.Cached("GET /ping", ping)
	shelf.Cached("GET /me", func(w http.ResponseWriter, r *http.Request) error {
		_, _ = auth.Current(r)
		return nil
	})
	rt.Handle("GET /me", func(w http.ResponseWriter, r *http.Request) error {
		u, _ := auth.Current(r)
		return render(w, u.Email)
	})
	return rt
}

func ping(w http.ResponseWriter, r *http.Request) error { return pong(w, r) }

func pong(w http.ResponseWriter, r *http.Request) error { return ping(w, r) }
`}), nil},
		{"a cached page sets a cookie", "cache-set-cookie", withCachedApp(map[string]string{"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/web"

	"example.com/shop/app/posts"
	prefs "example.com/shop/app/shared/preferences"
)

type App struct{ Auth *auth.Auth }

func (a *App) Router(rt *web.Router) {
	rt.Cached("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		web.SetCookie(w, r, "visited", "1", time.Hour)
		return nil
	})
	rt.Cached("GET /theme", prefs.Theme)
	rt.Cached("GET /hello", a.hello)
	rt.Cached("GET /guest", a.guest)
	rt.Resources("/posts", posts.Controller{}, rt.Cache.Handler)
}

func (a *App) hello(w http.ResponseWriter, r *http.Request) error {
	w.Header().Add("Set-Cookie", "hello=1")
	return nil
}

func (a *App) guest(w http.ResponseWriter, r *http.Request) error {
	_, err := a.Auth.StartSession(w, r, guest)
	return err
}
`}), []string{"app/routes.go:16", "app/routes.go:20", "app/routes.go:21", "app/routes.go:22", "app/routes.go:23"}},
		// g auth's app/auth has type Auth = gantryauth.Auth; an alias's fields
		// and methods are the aliased type's.
		{"through an alias of gantry's Auth", "cached-scope", withCachedApp(map[string]string{
			"app/auth/auth.go":        "package auth\n\nimport gantryauth \"github.com/scttymn/gantry/auth\"\n\ntype Auth = gantryauth.Auth\n",
			"app/admin/controller.go": "package admin\n\nimport \"example.com/shop/app/shared/resource\"\n\ntype Controller = resource.Controller\n",
			"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/web"

	"example.com/shop/app/admin"
	"example.com/shop/app/auth"
)

type App struct{ Auth *auth.Auth }

func (a *App) Router(rt *web.Router) {
	rt.Cached("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		return render(w, a.Auth.SignedIn(r))
	})
	desk := admin.Controller{}
	rt.Cached("GET /admin", func(w http.ResponseWriter, r *http.Request) error {
		_, message, _ := desk.Flash.Take(w, r)
		return render(w, message)
	})
}
`,
		}), []string{"app/routes.go:15", "app/routes.go:19"}},
		// A method may come before its type: earlier in the file, or in a
		// file that sorts first.
		{"methods declared before their type", "cached-scope", withCachedApp(map[string]string{
			"app/docs/actions.go":    "package docs\n\nimport (\n\t\"net/http\"\n\n\t\"github.com/scttymn/gantry/auth\"\n)\n\nfunc (c Controller) Show(w http.ResponseWriter, r *http.Request) error {\n\t_, ok := auth.Current(r)\n\treturn render(w, ok)\n}\n",
			"app/docs/controller.go": "package docs\n\ntype Controller struct{}\n",
			"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/web"

	"example.com/shop/app/docs"
)

func (a *App) theme(w http.ResponseWriter, r *http.Request) error {
	c, _ := r.Cookie("theme")
	return render(w, c.Value)
}

type App struct{}

func (a *App) Router(rt *web.Router) {
	rt.Cached("GET /docs", docs.Controller{}.Show)
	rt.Cached("GET /theme", a.theme)
}
`,
		}), []string{"app/routes.go:19", "app/routes.go:20"}},
		// homePage := homePage is the outer one's value, and a range's
		// expression is read before its variables are declared.
		{"a name declared again from itself", "cached-scope", withCachedApp(map[string]string{"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/web"

	"example.com/shop/app/home"
)

func (a *App) Router(rt *web.Router) {
	homePage := home.Controller{}
	rt.Scope("/welcome", nil, func(s *web.Scope) {
		homePage := homePage
		s.Cached("GET /{$}", homePage.Show)
	})
	rt.Cached("GET /crumbs", func(w http.ResponseWriter, r *http.Request) error {
		for _, r := range r.Cookies() {
			render(w, r.Name)
		}
		return nil
	})
}
`}), []string{"app/routes.go:15", "app/routes.go:17"}},
		{"cookies set where the page isn't cached", "cache-set-cookie", withCachedApp(map[string]string{"app/routes.go": `package app

import (
	"net/http"

	"github.com/scttymn/gantry/web"

	"example.com/shop/app/notes"
	"example.com/shop/app/posts"
	prefs "example.com/shop/app/shared/preferences"
)

func (a *App) Router(rt *web.Router) {
	rt.Cached("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Cache-Control", "no-cache")
		return nil
	})
	rt.Handle("POST /theme", prefs.Theme)
	rt.Resources("/posts", posts.Controller{}, nil)
	rt.Resources("/drafts", posts.Controller{}, a.Auth.Require)
	rt.Resources("/notes", notes.Controller{}, rt.Cache.Handler)
}
`, "app/notes/controller.go": `package notes

import (
	"net/http"

	"github.com/scttymn/gantry/web"
)

type Controller struct{}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error { return render(w) }

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	web.SetCookie(w, r, "draft", "", 0)
	return nil
}
`}), nil},
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

// withCachedApp is files with the controllers the cache rules' cases route
// to, in their own packages: home's Show reads who's signed in through a
// method of its own, posts' Index takes the flash through the shared
// resource controller it embeds, and preferences' Theme sets a cookie.
func withCachedApp(files map[string]string) map[string]string {
	app := map[string]string{
		"app/home/controller.go": `package home

import (
	"net/http"

	"github.com/scttymn/gantry/auth"
)

type Controller struct{}

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	return render(w, c.greeting(r))
}

func (c Controller) greeting(r *http.Request) string {
	if u, ok := auth.Current(r); ok {
		return "Hello, " + u.Email
	}
	return "Hello"
}

func (c Controller) About(w http.ResponseWriter, r *http.Request) error { return render(w, "About") }
`,
		"app/posts/controller.go": `package posts

import (
	"net/http"

	"example.com/shop/app/shared/resource"
)

type Controller struct{ resource.Controller }

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error { return c.Render(w, r) }
`,
		"app/shared/resource/resource.go": `package resource

import (
	"net/http"

	"github.com/scttymn/gantry/web"
)

type Controller struct {
	Flash web.Flash
}

func (c Controller) Render(w http.ResponseWriter, r *http.Request) error {
	if _, message, ok := c.Flash.Take(w, r); ok {
		return render(w, message)
	}
	return render(w, "")
}
`,
		"app/shared/preferences/preferences.go": `package preferences

import "net/http"

func Theme(w http.ResponseWriter, r *http.Request) error {
	remember(w, r.FormValue("theme"))
	return nil
}

func remember(w http.ResponseWriter, theme string) {
	http.SetCookie(w, &http.Cookie{Name: "theme", Value: theme})
}
`,
	}
	maps.Copy(app, files)
	return app
}

// The rule's message names what was found and the pattern to use instead.
func TestRuleMessages(t *testing.T) {
	root := t.TempDir()
	put(t, root, "go.mod", "module example.com/shop\n")
	put(t, root, "app/models/posts.go", "package models\n\nimport \"github.com/scttymn/gantry/web\"\n")
	put(t, root, "app/routes.go", "package app\n\nimport \"example.com/shop/app/models\"\n\nfunc (a *App) Router() {\n\trt.Handle(\"GET /logout\", func(w http.ResponseWriter, r *http.Request) error {\n\t\treturn models.New(a.DB.Write).EndSession(r.Context())\n\t})\n}\n")
	for name, body := range withCachedApp(map[string]string{"app/pages.go": `package app

import (
	"github.com/scttymn/gantry/web"

	"example.com/shop/app/home"
	"example.com/shop/app/posts"
)

func (a *App) pages(rt *web.Router) {
	rt.Cached("GET /{$}", home.Controller{}.Show)
	rt.Cached("GET /posts", posts.Controller{}.Index)
}
`}) {
		put(t, root, name, body)
	}
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"import-boundary":  {"app/models imports github.com/scttymn/gantry/web", "controller"},
		"get-mutates":      {"GET /logout", "POST", "button"},
		"cached-scope":     {"GET /{$}", "home.Controller.Show → home.Controller.greeting → auth.Current at app/home/controller.go:16", "every visitor", "Handle"},
		"cache-set-cookie": {"GET /posts", "posts.Controller.Index → resource.Controller.Render → web.Flash.Take at app/shared/resource/resource.go:14", "Handle"},
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

// An app made with gantry new --in-module has no go.mod of its own: its
// packages are under the enclosing module's path, and the rules that tell
// the app's own packages still do.
func TestInModuleApp(t *testing.T) {
	root := t.TempDir()
	put(t, root, "go.mod", "module example.com/big\n\ngo 1.27\n")
	app := filepath.Join(root, "apps", "control")
	put(t, app, "app/models/posts.go", "package models\n\nimport \"example.com/big/apps/control/app/posts\"\n")
	put(t, app, "app/posts/index.templ", "package posts\n\nimport \"example.com/big/apps/control/app/models\"\n\ntempl index(c Controller) {\n\t{ models.New(c.Read) }\n}\n")
	put(t, app, "app/posts/controller.go", `package posts

import "example.com/big/apps/control/app/models"

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	post, _ := models.New(c.DB.Read).GetPost(r.Context(), 1)
	return models.New(c.DB.Write).UpdatePost(r.Context(), post.ID)
}
`)
	got, err := Scan(app)
	if err != nil {
		t.Fatal(err)
	}
	var at []string
	for _, w := range got {
		at = append(at, fmt.Sprintf("%s:%d %s", w.File, w.Line, w.Rule))
	}
	want := []string{"app/models/posts.go:3 import-boundary", "app/posts/controller.go:7 read-then-write", "app/posts/index.templ:6 view-db"}
	if !reflect.DeepEqual(at, want) {
		t.Fatalf("got %v, want %v", at, want)
	}
}
