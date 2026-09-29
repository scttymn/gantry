package testkit

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// ErrUnscripted is what code gets for a request the test didn't script.
var ErrUnscripted = errors.New("testkit: a request the test didn't script")

// FakeHTTP answers the outbound requests a test scripts, and nothing else
// (WebMock's default): code that calls another service is handed its
// Client, and a request the test didn't expect fails the test.
type FakeHTTP struct {
	t        testing.TB
	mu       sync.Mutex
	scripts  []script
	requests []*http.Request
}

type script struct {
	method, url string
	answer      func(*http.Request) (*http.Response, error)
}

// HTTP is a FakeHTTP with nothing scripted.
func HTTP(t testing.TB) *FakeHTTP { return &FakeHTTP{t: t} }

// On answers method url with status and body. A url without a query
// matches any query.
func (f *FakeHTTP) On(method, url string, status int, body string) {
	f.Handle(method, url, func(*http.Request) (*http.Response, error) { return f.Response(status, body), nil })
}

// Handle answers method url with answer, which sees the request.
func (f *FakeHTTP) Handle(method, url string, answer func(*http.Request) (*http.Response, error)) {
	f.mu.Lock()
	f.scripts = append(f.scripts, script{method: method, url: url, answer: answer})
	f.mu.Unlock()
}

// Response is a response with status and body, for a Handle func.
func (f *FakeHTTP) Response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

// Client is an HTTP client that goes to the script, not the network.
func (f *FakeHTTP) Client() *http.Client { return &http.Client{Transport: f} }

// Requests are the requests made so far, in order.
func (f *FakeHTTP) Requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

// RoundTrip answers r from the script.
func (f *FakeHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	var answer func(*http.Request) (*http.Response, error)
	for _, s := range f.scripts {
		if s.method == r.Method && matches(s.url, r) {
			answer = s.answer
			break
		}
	}
	f.mu.Unlock()
	if answer == nil {
		where := r.Method + " " + r.URL.String()
		f.t.Errorf("testkit: unscripted request %s", where)
		return nil, fmt.Errorf("%w: %s", ErrUnscripted, where)
	}
	resp, err := answer(r)
	if resp != nil && resp.Request == nil {
		resp.Request = r
	}
	return resp, err
}

// matches reports whether r is for url: the same URL, or, when url has no
// query, the same URL but for the query.
func matches(url string, r *http.Request) bool {
	if strings.Contains(url, "?") {
		return url == r.URL.String()
	}
	u := *r.URL
	u.RawQuery, u.Fragment = "", ""
	return url == u.String()
}
