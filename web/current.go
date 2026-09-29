package web

import (
	"context"
	"net/http"
	"sync"
)

// Current is the request's own state (Rails' Current): what filters learned
// about it (who's signed in, which account it's for), set by one and read by
// the filters and the handler after it. It lives as long as the request.
//
//	var UserKey = web.NewKey[User]("user")
//
//	web.Set(r, UserKey, u)       // in a filter
//	u, ok := web.Get(r, UserKey) // later, a User
//
// It sits in the request's context and changes in place, so a filter passes
// values on without making a new request. The router gives every request an
// empty one as it arrives.

// Key is a typed name for a value in Current. Keys are pointers, so two
// packages' keys never collide, whatever their names.
type Key[T any] struct{ name string }

// NewKey is a key for values of type T; name is for people (and errors).
func NewKey[T any](name string) *Key[T] { return &Key[T]{name: name} }

func (k *Key[T]) String() string { return k.name }

type current struct {
	mu     sync.Mutex
	values map[any]any
}

type currentKey struct{}

// WithCurrent is r with an empty Current, for a request that doesn't come
// through a Router (a filter's unit test); r itself when it has one.
func WithCurrent(r *http.Request) *http.Request {
	if currentOf(r) != nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), currentKey{}, &current{values: map[any]any{}}))
}

func currentOf(r *http.Request) *current {
	c, _ := r.Context().Value(currentKey{}).(*current)
	return c
}

// withCurrent gives each request its Current.
func withCurrent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, WithCurrent(r))
	})
}

// Set puts v in the request's Current under k, for what runs after.
func Set[T any](r *http.Request, k *Key[T], v T) {
	c := currentOf(r)
	if c == nil {
		panic("web.Set " + k.name + ": the request has no Current: it didn't come through a Router (in a test, web.WithCurrent(r))")
	}
	c.mu.Lock()
	c.values[k] = v
	c.mu.Unlock()
}

// Get is the value under k in the request's Current, and whether one was
// set.
func Get[T any](r *http.Request, k *Key[T]) (T, bool) {
	var zero T
	c := currentOf(r)
	if c == nil {
		return zero, false
	}
	c.mu.Lock()
	v, ok := c.values[k]
	c.mu.Unlock()
	if !ok {
		return zero, false
	}
	return v.(T), true
}
