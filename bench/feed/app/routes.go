// Package app is the app: every route, and the controllers behind them,
// built from what they depend on.
package app

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/jobs"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/mail"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/web"

	"feed/app/health"
	"feed/app/home"
	"feed/app/posts"
	"feed/app/shared/bearer"
	"feed/assets"
)

// App is what the controllers share.
type App struct {
	Mail   *mail.Mailer // shared by auth, controllers and jobs
	DB     *db.DB
	Log    *slog.Logger
	Signer sign.Signer
	Jobs   *jobs.Queue // the background jobs, defined in app/jobs.go
	// Live sends pages their changes as they happen: a page draws
	// @a.Live.Source("post:" + id) to listen, and a controller or job calls
	// a.Live.Broadcast(ctx, "post:"+id, turbo.Replace(...)), or
	// a.Live.Refresh("post:"+id, turbo.RequestID(r)).
	Live *live.Hub
	// Proxies are the proxies in front of the app (TRUSTED_PROXIES), whose
	// word on a visitor's address and scheme it takes.
	Proxies web.Proxies
	// Production has browsers keep to https for the site (HSTS).
	Production bool

	Secret  []byte    // signs the API's bearer tokens
	Started time.Time // for /health's uptime
}

// Handler is every route, in gantry's middleware.
func (a *App) Handler() http.Handler {
	// The app is an API: a path with no route is answered as JSON too.
	rt := web.NewRouter(a.Log, func(w http.ResponseWriter, r *http.Request, status int) {
		web.JSON(w, status, map[string]string{"error": strings.ToLower(http.StatusText(status))})
	})
	rt.Public = assets.All
	rt.Proxies = a.Proxies
	// The Content-Security-Policy lets in scripts and styles from the app
	// itself, and inline only its own (by hash), so one slipped into a page
	// doesn't run. It's reported in the browser's console, not enforced:
	// once pages report nothing, drop CSPReportOnly to enforce it.
	rt.Security = web.Security{
		CSP:           web.ContentSecurityPolicy(assets.ImportMap.Hashes(), assets.Styles.Hash()),
		CSPReportOnly: true,
	}
	if a.Production {
		// Once a browser has seen the site over https, it keeps to https for
		// a year.
		rt.Security.HSTS = 365 * 24 * time.Hour
	}

	homePage := home.Controller{DB: a.DB}
	rt.Handle("GET /{$}", homePage.Show)

	feed := posts.Controller{DB: a.DB}
	tokens := bearer.Tokens{Secret: a.Secret}
	rt.Scope("", web.Pipeline{web.AcceptJSON}, func(s *web.Scope) {
		s.Handle("GET /health", health.Controller{DB: a.DB, Started: a.Started}.Show)
		s.Handle("GET /feed", feed.Index)
		s.Handle("GET /posts/{id}", feed.Show)
		s.Scope("", web.Pipeline{tokens.Require}, func(s *web.Scope) {
			s.Handle("POST /posts", feed.Create)
			s.Handle("POST /posts/{id}/like", feed.Like)
		})
	})

	// The pages' live streams: put it behind the filters that decide who
	// may listen, as the pages it serves are.
	rt.Handle("GET /live", a.Live.Serve)

	if a.Mail != nil && a.Mail.Inbox != nil {
		a.Mail.Inbox.Routes(rt.Mount, "/dev/inbox")
	}
	assets.Routes(rt.Mount)
	return rt.Handler()
}
