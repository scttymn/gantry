package web

import "net/http"

// The actions a resource's controller may have, Rails' seven. Resources
// routes each one the controller implements.
type (
	Indexer interface {
		Index(http.ResponseWriter, *http.Request) error
	}
	Newer interface {
		New(http.ResponseWriter, *http.Request) error
	}
	Creator interface {
		Create(http.ResponseWriter, *http.Request) error
	}
	Shower interface {
		Show(http.ResponseWriter, *http.Request) error
	}
	Editor interface {
		Edit(http.ResponseWriter, *http.Request) error
	}
	Updater interface {
		Update(http.ResponseWriter, *http.Request) error
	}
	Deleter interface {
		Delete(http.ResponseWriter, *http.Request) error
	}
)

// Resources routes a controller's actions under base ("/admin/pillars"), as
// Rails' resources does:
//
//	GET    base              Index
//	GET    base/new          New
//	POST   base              Create
//	GET    base/{id}         Show
//	GET    base/{id}/edit    Edit
//	PATCH  base/{id}         Update (PUT too)
//	DELETE base/{id}         Delete
//
// wrap, when set, goes around each (auth.Require, say).
func (rt *Router) Resources(base string, controller any, wrap func(http.Handler) http.Handler) {
	route := func(pattern string, h Handler) {
		var handler http.Handler = rt.Wrap(h)
		if wrap != nil {
			handler = wrap(handler)
		}
		rt.mux.Handle(pattern, handler)
	}
	if c, ok := controller.(Indexer); ok {
		route("GET "+base, c.Index)
		route("GET "+base+"/{$}", c.Index)
	}
	if c, ok := controller.(Newer); ok {
		route("GET "+base+"/new", c.New)
	}
	if c, ok := controller.(Creator); ok {
		route("POST "+base, c.Create)
		route("POST "+base+"/{$}", c.Create)
	}
	if c, ok := controller.(Shower); ok {
		route("GET "+base+"/{id}", c.Show)
	}
	if c, ok := controller.(Editor); ok {
		route("GET "+base+"/{id}/edit", c.Edit)
	}
	if c, ok := controller.(Updater); ok {
		route("PATCH "+base+"/{id}", c.Update)
		route("PUT "+base+"/{id}", c.Update)
	}
	if c, ok := controller.(Deleter); ok {
		route("DELETE "+base+"/{id}", c.Delete)
	}
}
