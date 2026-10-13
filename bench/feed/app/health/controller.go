// Package health is the API's health check, at /health: unlike /up, it asks
// the database.
package health

import (
	"net/http"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"
)

// Controller answers the health check.
type Controller struct {
	DB      *db.DB
	Started time.Time
}

// Show is GET /health.
func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	var one int
	if err := c.DB.Read.QueryRowContext(r.Context(), `SELECT 1`).Scan(&one); err != nil {
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
