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

type App struct {
	Mail   *mail.Mailer
	DB     *db.DB
	Log    *slog.Logger
	Signer sign.Signer
	Jobs   *jobs.Queue
	Live   *live.Hub

	Secret  []byte
	Started time.Time
}

func (a *App) Handler() http.Handler { return a.Router().Handler() }

func (a *App) Router() *web.Router {
	rt := web.NewRouter(a.Log, func(w http.ResponseWriter, r *http.Request, status int) {
		web.JSON(w, status, map[string]string{"error": strings.ToLower(http.StatusText(status))})
	})
	rt.Public = assets.All

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

	rt.Handle("GET /live", a.Live.Serve)

	if a.Mail != nil && a.Mail.Inbox != nil {
		a.Mail.Inbox.Routes(rt.Mount, "/dev/inbox")
	}
	assets.Routes(rt.Mount)
	return rt
}
