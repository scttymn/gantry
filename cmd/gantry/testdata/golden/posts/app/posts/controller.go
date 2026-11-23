// Package posts is the app's posts, at /posts.
package posts

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/web"

	"example.com/gym/app/models"
	"example.com/gym/app/shared/resource"
)

const (
	path     = "/posts"
	param    = "post" // the form's fields are post[title], …
	title    = "Posts"
	singular = "Post"
)

// Controller is the posts' pages: the list, the forms, and deleting.
type Controller struct{ resource.Controller }

var fields = []resource.Field{resource.F("title"), resource.F("body", resource.Type("textarea")), resource.F("published_on", resource.Type("date"))}

// formValues are a post as its form holds them: text, as typed.
func formValues(post models.Post) map[string]string {
	return map[string]string{"title": post.Title, "body": post.Body, "published_on": resource.DateText(post.PublishedOn)}
}

// fromForm reads a form's values back into a post.
func fromForm(v map[string]string) models.Post {
	return models.Post{Title: v["title"], Body: v["body"], PublishedOn: resource.OptionalDate(v["published_on"])}
}

func (c Controller) Index(w http.ResponseWriter, r *http.Request) error {
	list, err := models.New(c.DB.Read).ListPosts(r.Context())
	if err != nil {
		return err
	}
	rows := make([]resource.Row, len(list))
	for i, post := range list {
		rows[i] = resource.Row{ID: post.ID, Cells: []string{resource.ListText(post.Title), resource.ListText(post.Body)}}
	}
	return c.Render(w, r, http.StatusOK, title, func(p resource.Page) templ.Component { return index(p, rows) })
}

func (c Controller) New(w http.ResponseWriter, r *http.Request) error {
	return c.form(w, r, http.StatusOK, 0, map[string]string{}, nil)
}

func (c Controller) Create(w http.ResponseWriter, r *http.Request) error {
	v := resource.Overlay(r, param, map[string]string{})
	post := fromForm(v)
	if errs := post.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, 0, v, errs)
	}
	q := models.New(c.DB.Write)
	_, err := q.CreatePost(r.Context(), models.CreatePostParams{Title: post.Title, Body: post.Body, PublishedOn: resource.DateParam(post.PublishedOn), Now: c.Now()})
	if err != nil {
		return err
	}
	return c.Created(w, r, path, singular)
}

func (c Controller) Edit(w http.ResponseWriter, r *http.Request) error {
	post, err := models.New(c.DB.Read).GetPost(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	return c.form(w, r, http.StatusOK, post.ID, formValues(post), nil)
}

func (c Controller) Update(w http.ResponseWriter, r *http.Request) error {
	was, err := models.New(c.DB.Read).GetPost(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	v := resource.Overlay(r, param, formValues(was))
	post := fromForm(v)
	if errs := post.Errors(); len(errs) > 0 {
		return c.form(w, r, http.StatusUnprocessableEntity, was.ID, v, errs)
	}
	err = models.New(c.DB.Write).UpdatePost(r.Context(), models.UpdatePostParams{Title: post.Title, Body: post.Body, PublishedOn: resource.DateParam(post.PublishedOn), Now: c.Now(), ID: was.ID})
	if err != nil {
		return err
	}
	return c.Saved(w, r, path, singular)
}

func (c Controller) Delete(w http.ResponseWriter, r *http.Request) error {
	q := models.New(c.DB.Write)
	post, err := q.GetPost(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	if err := q.DeletePost(r.Context(), post.ID); err != nil {
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
