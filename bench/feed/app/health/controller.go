package health

import (
	"net/http"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"feed/app/models"
)

type Controller struct {
	DB      *db.DB
	Started time.Time
}

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	if err := models.New(c.DB.Read).Ping(r.Context()); err != nil {
		return web.JSON(w, http.StatusServiceUnavailable, struct {
			Status string `json:"status"`
			DB     string `json:"db"`
			Error  string `json:"error"`
		}{"degraded", "unreachable", err.Error()})
	}
	return web.JSON(w, http.StatusOK, struct {
		Status string `json:"status"`
		DB     string `json:"db"`
		Uptime int64  `json:"uptime_s"`
	}{"ok", "ok", int64(time.Since(c.Started).Seconds())})
}
