// Package app is the app: every route, and the controllers behind them,
// built from what they depend on.
package app

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"twelve-dollar/app/health"
	"twelve-dollar/app/home"
	"twelve-dollar/app/posts"
	"twelve-dollar/app/shared/bearer"
	"twelve-dollar/assets"
)

// App is what the controllers share.
type App struct {
	DB      *db.DB
	Log     *slog.Logger
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

	assets.Routes(rt.Mount)
	return rt.Handler()
}
