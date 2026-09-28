// Package workouts is the admin's workouts, at /admin/workouts.
package workouts

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/web"

	"example.com/gym/app/admin"
	"example.com/gym/app/models"
)

const (
	path     = "/admin/workouts"
	param    = "workout" // the form's fields are workout[date], …
	title    = "Workouts"
	singular = "Workout"
)

// Controller is the workouts' pages: the list, the forms, and deleting.
type Controller struct{ admin.Controller }

var fields = []admin.Field{admin.F("date", admin.Type("date")), admin.F("name"), admin.F("rx", admin.Type("text"))}

// formValues are a workout as its form holds them: text, as typed.
func formValues(workout models.Workout) map[string]string {
	return map[string]string{"date": admin.DateValue(workout.Date), "name": workout.Name, "rx": workout.Rx}
}

// fromForm reads a form's values back into a workout.
func fromForm(v map[string]string) models.Workout {
	return models.Workout{Date: admin.OptionalDate(v["date"]), Name: v["name"], Rx: v["rx"]}
}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	list, err := models.New(c.DB.Read).ListWorkouts(r.Context())
	if err != nil {
		return err
	}
	rows := make([]admin.Row, len(list))
	for i, workout := range list {
		rows[i] = admin.Row{ID: workout.ID, Cells: []string{admin.DateText(workout.Date), admin.ListText(workout.Name)}}
	}
	return c.Render(w, r, http.StatusOK, title, func(p admin.AdminPage) templ.Component { return index(p, rows) })
}

func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	return c.form(w, r, http.StatusOK, 0, map[string]string{}, nil)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	v := admin.Overlay(r, param, map[string]string{})
	workout := fromForm(v)
	if errs := workout.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, 0, v, errs)
	}
	q := models.New(c.DB.Write)
	_, err := q.CreateWorkout(r.Context(), models.CreateWorkoutParams{Date: admin.DateParam(workout.Date), Name: workout.Name, Rx: workout.Rx, Now: c.Now()})
	if err != nil {
		return err
	}
	return c.Created(w, r, path, singular)
}

func (c Controller) Edit(w http.ResponseWriter, r *http.Request) error {
	workout, err := models.New(c.DB.Read).GetWorkout(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	return c.form(w, r, http.StatusOK, workout.ID, formValues(workout), nil)
}

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	was, err := models.New(c.DB.Read).GetWorkout(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	v := admin.Overlay(r, param, formValues(was))
	workout := fromForm(v)
	if errs := workout.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, was.ID, v, errs)
	}
	err = models.New(c.DB.Write).UpdateWorkout(r.Context(), models.UpdateWorkoutParams{Date: admin.DateParam(workout.Date), Name: workout.Name, Rx: workout.Rx, Now: c.Now(), ID: was.ID})
	if err != nil {
		return err
	}
	return c.Saved(w, r, path, singular)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Write)
	workout, err := q.GetWorkout(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	if err := q.DeleteWorkout(r.Context(), workout.ID); err != nil {
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
