// Package pillars is the admin's pillars, at /admin/pillars.
package pillars

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/web"

	"example.com/gym/app/admin"
	"example.com/gym/app/models"
)

const (
	path     = "/admin/pillars"
	param    = "pillar" // the form's fields are pillar[title], …
	title    = "Pillars"
	singular = "Pillar"
)

// Controller is the pillars' pages: the list, the forms, and deleting.
type Controller struct{ admin.Controller }

var fields = []admin.Field{admin.F("title"), admin.F("body"), admin.Position}

// formValues are a pillar as its form holds them: text, as typed.
func formValues(pillar models.Pillar) map[string]string {
	return map[string]string{"title": pillar.Title, "body": pillar.Body, "position": admin.IntText(pillar.Position)}
}

// fromForm reads a form's values back into a pillar.
func fromForm(v map[string]string) models.Pillar {
	return models.Pillar{Title: v["title"], Body: v["body"], Position: admin.OptionalInt(v["position"])}
}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	list, err := models.New(c.DB.Read).ListPillars(r.Context())
	if err != nil {
		return err
	}
	rows := make([]admin.Row, len(list))
	for i, pillar := range list {
		rows[i] = admin.Row{ID: pillar.ID, Cells: []string{admin.ListText(pillar.Title), admin.ListText(pillar.Body)}}
	}
	return c.Render(w, r, http.StatusOK, title, func(p admin.AdminPage) templ.Component { return index(p, rows) })
}

func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	return c.form(w, r, http.StatusOK, 0, map[string]string{}, nil)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	v := admin.Overlay(r, param, map[string]string{})
	pillar := fromForm(v)
	if errs := pillar.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, 0, v, errs)
	}
	q := models.New(c.DB.Write)
	if !pillar.Position.Valid {
		next, err := q.NextPillarPosition(r.Context())
		if err != nil {
			return err
		}
		pillar.Position.Int64, pillar.Position.Valid = next, true
	}
	_, err := q.CreatePillar(r.Context(), models.CreatePillarParams{Title: pillar.Title, Body: pillar.Body, Position: pillar.Position, Now: c.Now()})
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

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	was, err := models.New(c.DB.Read).GetPillar(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	v := admin.Overlay(r, param, formValues(was))
	pillar := fromForm(v)
	if errs := pillar.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, was.ID, v, errs)
	}
	err = models.New(c.DB.Write).UpdatePillar(r.Context(), models.UpdatePillarParams{Title: pillar.Title, Body: pillar.Body, Position: pillar.Position, Now: c.Now(), ID: was.ID})
	if err != nil {
		return err
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
	f := admin.Form{Param: param, Values: v, Errors: errs, Fields: fields, Cancel: path, Action: path, Method: "post"}
	heading := "New " + strings.ToLower(singular)
	if id != 0 {
		f.Action, f.Method, heading = path+"/"+web.IDText(id), "patch", "Edit "+strings.ToLower(singular)
	}
	return c.Render(w, r, status, heading, func(p admin.AdminPage) templ.Component { return form(p, heading, f) })
}
