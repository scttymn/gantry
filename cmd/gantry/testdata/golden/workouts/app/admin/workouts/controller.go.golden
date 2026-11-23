// Package workouts is the admin's workouts, at /admin/workouts.
package workouts

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
	path     = "/admin/workouts"
	param    = "workout" // the form's fields are workout[date], …
	title    = "Workouts"
	singular = "Workout"
)

// Controller is the workouts' pages: the list, the forms, and deleting.
type Controller struct{ resource.Controller }

var fields = []resource.Field{resource.F("date", resource.Type("date")), resource.F("name"), resource.F("rx", resource.Type("textarea"))}

// formValues are a workout as its form holds them: text, as typed.
func formValues(workout models.Workout) map[string]string {
	return map[string]string{"date": resource.DateText(workout.Date), "name": workout.Name, "rx": workout.Rx}
}

// fromForm reads a form's values back into a workout.
func fromForm(v map[string]string) models.Workout {
	return models.Workout{Date: resource.OptionalDate(v["date"]), Name: v["name"], Rx: v["rx"]}
}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	list, err := models.New(c.DB.Read).ListWorkouts(r.Context())
	if err != nil {
		return err
	}
	rows := make([]resource.Row, len(list))
	for i, workout := range list {
		rows[i] = resource.Row{ID: workout.ID, Cells: []string{resource.DateText(workout.Date), resource.ListText(workout.Name)}}
	}
	return c.Render(w, r, http.StatusOK, title, func(p resource.Page) templ.Component { return index(p, rows) })
}

func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	return c.form(w, r, http.StatusOK, 0, map[string]string{}, nil)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	v := resource.Overlay(r, param, map[string]string{})
	workout := fromForm(v)
	if errs := workout.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, 0, v, errs)
	}
	err := c.DB.Tx(r.Context(), func(tx *db.Tx) (err error) {
		q := models.New(tx)
		_, err = q.CreateWorkout(r.Context(), models.CreateWorkoutParams{Date: resource.DateParam(workout.Date), Name: workout.Name, Rx: workout.Rx, Now: c.Now()})
		return err
	})
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
		was, err := q.GetWorkoutForUpdate(r.Context(), web.ID(r, "id"))
		if err != nil {
			return err
		}
		id, v = was.ID, formValues(was)
		maps.Copy(v, sent)
		workout := fromForm(v)
		if errs = workout.Errors(); len(errs) > 0 {
			return nil
		}
		return q.UpdateWorkout(r.Context(), models.UpdateWorkoutParams{Date: resource.DateParam(workout.Date), Name: workout.Name, Rx: workout.Rx, Now: c.Now(), ID: id})
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
	f := resource.Form{Param: param, Values: v, Errors: errs, Fields: fields, Cancel: path, Action: path, Method: "post"}
	heading := "New " + strings.ToLower(singular)
	if id != 0 {
		f.Action, f.Method, heading = path+"/"+web.IDText(id), "patch", "Edit "+strings.ToLower(singular)
	}
	return c.Render(w, r, status, heading, func(p resource.Page) templ.Component { return form(p, f) })
}
