package home

import (
	"net/http"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"
)

type Controller struct {
	DB *db.DB
}

func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, index())
}
