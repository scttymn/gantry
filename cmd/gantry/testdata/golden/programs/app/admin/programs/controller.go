// Package programs is the admin's programs, at /admin/programs.
package programs

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/web"

	"example.com/gym/app/admin"
	"example.com/gym/app/models"
)

const (
	path     = "/admin/programs"
	param    = "program" // the form's fields are program[name], …
	title    = "Programs"
	singular = "Program"
	// recordType is the program's photos' owner, as Active Storage names it.
	recordType = "Program"
)

// Controller is the programs' pages: the list, the forms, and deleting.
type Controller struct{ admin.Controller }

var fields = []admin.Field{admin.F("name"), admin.F("blurb", admin.Type("text")), admin.F("photo", admin.Type("file")), admin.F("cta"), admin.Position}

// formValues are a program as its form holds them: text, as typed.
func formValues(program models.Program) map[string]string {
	return map[string]string{"name": program.Name, "blurb": program.Blurb, "cta": program.CTA, "position": admin.IntText(program.Position)}
}

// fromForm reads a form's values back into a program.
func fromForm(v map[string]string) models.Program {
	return models.Program{Name: v["name"], Blurb: v["blurb"], CTA: v["cta"], Position: admin.OptionalInt(v["position"])}
}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	list, err := models.New(c.DB.Read).ListPrograms(r.Context())
	if err != nil {
		return err
	}
	rows := make([]admin.Row, len(list))
	for i, program := range list {
		rows[i] = admin.Row{ID: program.ID, Cells: []string{admin.ListText(program.Name), admin.ListText(program.Blurb)}}
	}
	return c.Render(w, r, http.StatusOK, title, func(p admin.AdminPage) templ.Component { return index(p, rows) })
}

func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	return c.form(w, r, http.StatusOK, 0, map[string]string{}, nil)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	v := admin.Overlay(r, param, map[string]string{})
	program := fromForm(v)
	if errs := program.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, 0, v, errs)
	}
	q := models.New(c.DB.Write)
	if !program.Position.Valid {
		next, err := q.NextProgramPosition(r.Context())
		if err != nil {
			return err
		}
		program.Position.Int64, program.Position.Valid = next, true
	}
	id, err := q.CreateProgram(r.Context(), models.CreateProgramParams{Name: program.Name, Blurb: program.Blurb, CTA: program.CTA, Position: program.Position, Now: c.Now()})
	if err != nil {
		return err
	}
	if err := c.SavePhoto(r, param, "photo", recordType, id); err != nil {
		return err
	}
	return c.Created(w, r, path, singular)
}

func (c Controller) Edit(w http.ResponseWriter, r *http.Request) error {
	program, err := models.New(c.DB.Read).GetProgram(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	return c.form(w, r, http.StatusOK, program.ID, formValues(program), nil)
}

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	was, err := models.New(c.DB.Read).GetProgram(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	v := admin.Overlay(r, param, formValues(was))
	program := fromForm(v)
	if errs := program.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, was.ID, v, errs)
	}
	err = models.New(c.DB.Write).UpdateProgram(r.Context(), models.UpdateProgramParams{Name: program.Name, Blurb: program.Blurb, CTA: program.CTA, Position: program.Position, Now: c.Now(), ID: was.ID})
	if err != nil {
		return err
	}
	if err := c.SavePhoto(r, param, "photo", recordType, was.ID); err != nil {
		return err
	}
	return c.Saved(w, r, path, singular)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Write)
	program, err := q.GetProgram(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	if err := c.Photos.Purge(r.Context(), recordType, program.ID, "photo"); err != nil {
		return err
	}
	if err := q.DeleteProgram(r.Context(), program.ID); err != nil {
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
		f.Photos = c.PhotoPreviews(r.Context(), recordType, id, "photo")
	}
	return c.Render(w, r, status, heading, func(p admin.AdminPage) templ.Component { return form(p, heading, f) })
}
