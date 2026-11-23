// Package resource is what the controllers gantry g resource writes share:
// their pages, drawn in the layout, their forms' fields, the flash a change
// leaves, and photos, kept in Active Storage. The first resource wrote it;
// it's the app's now, and g resource keeps it as it is.
package resource

import (
	"context"
	"database/sql"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/images"
	"github.com/scttymn/gantry/storage"
	"github.com/scttymn/gantry/web"
)

// Controller is what a resource's controller is built on: the database, the
// flash its changes leave for the next page, and, for a resource with
// photos, the app's Storage.
type Controller struct {
	DB     *db.DB
	Flash  web.Flash
	Photos *storage.Storage
}

// Now is when a record is saved: its created_at and updated_at.
func (c Controller) Now() time.Time { return time.Now() }

// Page is what a resource's page shows besides its own: its title, its
// address, and the message the last change left (Rails' notice).
type Page struct {
	Title  string
	Path   string
	Notice string
}

// Render draws one of the resource's pages, with the flash for it.
func (c Controller) Render(w http.ResponseWriter, r *http.Request, status int, title string, page func(Page) templ.Component) error {
	p := Page{Title: title, Path: r.URL.Path}
	if _, message, ok := c.Flash.Take(w, r); ok {
		p.Notice = message
	}
	return web.Render(w, r, status, page(p))
}

// Created, Saved and Deleted send the browser back to the list at path,
// saying what happened, in the words of Rails' scaffold.
func (c Controller) Created(w http.ResponseWriter, r *http.Request, path, singular string) error {
	c.Flash.Redirect(w, r, path, "notice", singular+" was successfully created.")
	return nil
}

func (c Controller) Saved(w http.ResponseWriter, r *http.Request, path, singular string) error {
	c.Flash.Redirect(w, r, path, "notice", singular+" was successfully updated.")
	return nil
}

func (c Controller) Deleted(w http.ResponseWriter, r *http.Request, path, singular string) error {
	c.Flash.Redirect(w, r, path, "notice", singular+" was successfully destroyed.")
	return nil
}

// SavePhoto attaches the file the form sent for a photo field to the record,
// in place of the one it had. With no file chosen, the record keeps its own.
func (c Controller) SavePhoto(r *http.Request, param, field, recordType string, id int64) error {
	f, ok := storage.FileFrom(r, param+"["+field+"]")
	if !ok {
		return nil
	}
	_, err := c.Photos.Attach(r.Context(), storage.Ref{RecordType: recordType, RecordID: id, Name: field}, f)
	return err
}

// PurgePhoto removes a record's photo, with its file and copies.
func (c Controller) PurgePhoto(ctx context.Context, recordType string, id int64, field string) error {
	return c.Photos.Purge(ctx, storage.Ref{RecordType: recordType, RecordID: id, Name: field})
}

// PhotoPreviews are a record's photos, by field, as its edit form shows them.
func (c Controller) PhotoPreviews(ctx context.Context, recordType string, id int64, fields ...string) (map[string]templ.Component, error) {
	previews := map[string]templ.Component{}
	for _, field := range fields {
		b, ok, err := c.Photos.Find(ctx, storage.Ref{RecordType: recordType, RecordID: id, Name: field})
		if err != nil {
			return nil, err
		}
		if ok {
			previews[field] = c.Photos.Img(b, images.Img{Sizes: "240px"})
		}
	}
	return previews, nil
}

// Field is one of a form's fields: a column, or a photo.
type Field struct {
	Name  string // the column: "published_on", sent as post[published_on]
	Label string // "Published on"
	Type  string // its input's type ("text", "number", "date", "file"), or "textarea"
}

// F is the field for a column, labelled as Rails' humanize would.
func F(name string, options ...func(*Field)) Field {
	label := strings.ReplaceAll(strings.TrimSuffix(name, "_id"), "_", " ")
	f := Field{Name: name, Label: strings.ToUpper(label[:1]) + label[1:], Type: "text"}
	for _, option := range options {
		option(&f)
	}
	return f
}

// Type is a field's input type, or "textarea".
func Type(t string) func(*Field) { return func(f *Field) { f.Type = t } }

// Position is a sortable list's position: a number, left empty to go last.
var Position = F("position", Type("number"))

// Form is a record's new or edit form.
type Form struct {
	Param  string            // what its fields are sent under: "post", for post[title]
	Values map[string]string // each field's, as typed
	Errors []string          // why it wasn't saved, when it's sent back
	Fields []Field
	Action string                     // where it's sent: the list's path, or the record's
	Method string                     // "post", or "patch" for an edit (sent as _method)
	Cancel string                     // where Cancel goes
	Photos map[string]templ.Component // the record's photos, by field, on its edit form
}

// Multipart reports whether the form sends a file, as multipart/form-data.
func (f Form) Multipart() bool {
	for _, field := range f.Fields {
		if field.Type == "file" {
			return true
		}
	}
	return false
}

// Row is a record in the list: its cells, and its id, for its edit link.
type Row struct {
	ID    int64
	Cells []string
}

// List is a resource's list.
type List struct {
	Path     string   // the resource's: "/admin/posts"
	Singular string   // "Post", for "New post"
	Columns  []string // the cells' headings
	Rows     []Row
}

// Overlay is values with what the form sent under param on top: a field it
// didn't send keeps its value, as with Rails' permitted params.
func Overlay(r *http.Request, param string, values map[string]string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, values)
	maps.Copy(out, web.Sent(r, param))
	return out
}

// IntText is a number as its field holds it: "" when it's unset.
func IntText(n sql.NullInt64) string {
	if !n.Valid {
		return ""
	}
	return strconv.FormatInt(n.Int64, 10)
}

// OptionalInt is a number field's value: unset when it's empty, or isn't a
// number.
func OptionalInt(s string) sql.NullInt64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return sql.NullInt64{Int64: n, Valid: err == nil}
}

// DateText is a date as its field holds it and the list shows it,
// "2006-01-02": "" when it's unset.
func DateText(d sql.NullTime) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format(time.DateOnly)
}

// OptionalDate is a date field's value: unset when it's empty, or isn't a
// date.
func OptionalDate(s string) sql.NullTime {
	d, err := time.Parse(time.DateOnly, strings.TrimSpace(s))
	return sql.NullTime{Time: d, Valid: err == nil}
}

// DateParam is a date as the queries take it: text, "2006-01-02", as SQLite
// keeps dates (a time would be written with its hours, which sort after the
// day). On Postgres the queries cast it back to a date.
func DateParam(d sql.NullTime) sql.NullString {
	return sql.NullString{String: DateText(d), Valid: d.Valid}
}

// ListText is a text column as the list shows it: its first line, cut short.
func ListText(s string) string {
	s, _, cut := strings.Cut(s, "\n")
	if utf8.RuneCountInString(s) > 60 {
		s, cut = string([]rune(s)[:60]), true
	}
	if cut {
		s = strings.TrimSpace(s) + "…"
	}
	return s
}
