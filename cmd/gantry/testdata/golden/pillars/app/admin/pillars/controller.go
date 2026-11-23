// Package pillars is the admin's pillars, at /admin/pillars.
package pillars

import (
	"maps"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"example.com/gym/app/models"
	"example.com/gym/app/shared/resource"
)

const (
	path     = "/admin/pillars"
	param    = "pillar" // the form's fields are pillar[title], …
	title    = "Pillars"
	singular = "Pillar"
)

// Controller is the pillars' pages: the list, the forms, and deleting.
type Controller struct{ resource.Controller }

var fields = []resource.Field{resource.F("title"), resource.F("body"), resource.Position}

// formValues are a pillar as its form holds them: text, as typed.
func formValues(pillar models.Pillar) map[string]string {
	return map[string]string{"title": pillar.Title, "body": pillar.Body, "position": resource.IntText(pillar.Position)}
}

// fromForm reads a form's values back into a pillar.
func fromForm(v map[string]string) models.Pillar {
	return models.Pillar{Title: v["title"], Body: v["body"], Position: resource.OptionalInt(v["position"])}
}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	list, err := models.New(c.DB.Read).ListPillars(r.Context())
	if err != nil {
		return err
	}
	rows := make([]resource.Row, len(list))
	for i, pillar := range list {
		rows[i] = resource.Row{ID: pillar.ID, Cells: []string{resource.ListText(pillar.Title), resource.ListText(pillar.Body)}}
	}
	return c.Render(w, r, http.StatusOK, title, func(p resource.Page) templ.Component { return index(p, rows) })
}

func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	return c.form(w, r, http.StatusOK, 0, map[string]string{}, nil)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	v := resource.Overlay(r, param, map[string]string{})
	pillar := fromForm(v)
	if errs := pillar.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, 0, v, errs)
	}
	err := c.DB.Tx(r.Context(), func(tx *db.Tx) (err error) {
		q := models.New(tx)
		// On Postgres two at once can still take the same position; the
		// list orders by position, then id.
		if !pillar.Position.Valid {
			next, err := q.NextPillarPosition(r.Context())
			if err != nil {
				return err
			}
			pillar.Position.Int64, pillar.Position.Valid = next, true
		}
		_, err = q.CreatePillar(r.Context(), models.CreatePillarParams{Title: pillar.Title, Body: pillar.Body, Position: pillar.Position, Now: c.Now()})
		return err
	})
	if err != nil {
		return err
	}
	return c.Created(w, r, path, singular)
}

func (c Controller) Edit(w http.ResponseWriter, r *http.Request) error {
	pillar, err := models.New(c.DB.Read).GetPillar(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	return c.form(w, r, http.StatusOK, pillar.ID, formValues(pillar), nil)
}

// Update reads, checks and writes the row in one transaction, so an edit
// saved meanwhile isn't overwritten with what this one read. The form is
// read first: a PATCH's body may still be arriving.
func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	sent := resource.Overlay(r, param, map[string]string{})
	var (
		id   int64
		v    map[string]string
		errs []string
	)
	err := c.DB.Tx(r.Context(), func(tx *db.Tx) error {
		q := models.New(tx)
		was, err := q.GetPillarForUpdate(r.Context(), web.ID(r, "id"))
		if err != nil {
			return err
		}
		id, v = was.ID, formValues(was)
		maps.Copy(v, sent)
		pillar := fromForm(v)
		if errs = pillar.Errors(); len(errs) > 0 {
			return nil
		}
		return q.UpdatePillar(r.Context(), models.UpdatePillarParams{Title: pillar.Title, Body: pillar.Body, Position: pillar.Position, Now: c.Now(), ID: id})
	})
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, id, v, errs)
	}
	return c.Saved(w, r, path, singular)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Write)
	pillar, err := q.GetPillar(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	if err := q.DeletePillar(r.Context(), pillar.ID); err != nil {
		return err
	}
	return c.Deleted(w, r, path, singular)
}

// form is the new or edit form, with what's wrong when it's sent back.
func (c Controller) form(w http.ResponseWriter, r *http.Request, status int, id int64, v map[string]string, errs []string) error {
	f := resource.Form{Param: param, Values: v, Errors: errs, Fields: fields, Cancel: path, Action: path, Method: "post"}
	heading := "New " + strings.ToLower(singular)
	if id != 0 {
		f.Action, f.Method, heading = path+"/"+web.IDText(id), "patch", "Edit "+strings.ToLower(singular)
	}
	return c.Render(w, r, status, heading, func(p resource.Page) templ.Component { return form(p, f) })
}
