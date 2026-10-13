package load

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// server is an app that counts what it's sent.
type server struct {
	*httptest.Server
	mu    sync.Mutex
	hits  map[string]int // by route
	conns map[string]bool
	wrong []string // requests that aren't the challenge's
}

func newServer(t *testing.T, handle func(route string, w http.ResponseWriter, r *http.Request) bool) *server {
	t.Helper()
	s := &server{hits: map[string]int{}, conns: map[string]bool{}}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		switch {
		case strings.HasSuffix(r.URL.Path, "/like"):
			route = "POST /posts/:id/like"
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/posts/"):
			route = "GET /posts/:id"
		}
		s.mu.Lock()
		s.hits[route]++
		authed := strings.Count(r.Header.Get("Authorization"), ".") == 2 && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Method == "POST" && !authed {
			s.wrong = append(s.wrong, route+" with no token")
		}
		s.mu.Unlock()
		if handle != nil && handle(route, w, r) {
			return
		}
		switch route {
		case "GET /feed":
			fmt.Fprint(w, `{"posts":[{"id":41,"body":"a"},{"id":42,"body":"b"}]}`)
		case "GET /posts/:id":
			if id := strings.TrimPrefix(r.URL.Path, "/posts/"); id != "41" && id != "42" {
				s.mu.Lock()
				s.wrong = append(s.wrong, "a post the feed didn't have: "+id)
				s.mu.Unlock()
			}
			fmt.Fprint(w, `{"post":{"id":41}}`)
		case "POST /posts/:id/like":
			fmt.Fprint(w, `{"liked":true}`)
		case "POST /posts":
			var in struct{ Body string }
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Body == "" {
				s.mu.Lock()
				s.wrong = append(s.wrong, "a post with no body")
				s.mu.Unlock()
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"post":{"id":43}}`)
		default:
			s.mu.Lock()
			s.wrong = append(s.wrong, route)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	s.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			s.mu.Lock()
			s.conns[c.RemoteAddr().String()] = true
			s.mu.Unlock()
		}
	}
	s.Start()
	t.Cleanup(s.Close)
	return s
}

// The load is the rate asked for, paced (not as fast as it will go), in
// the challenge's mix (100 feed : 100 post : 15 like : 2 create), on no
// more connections than asked, and every request is one the API has.
func TestRunHoldsRateAndMix(t *testing.T) {
	s := newServer(t, nil)
	start := time.Now()
	res, err := Run(context.Background(), Options{URL: s.URL, Rate: 4000, Connections: 8, Duration: time.Second, Secret: "secret", Users: 100})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Due != 4000 || res.Done != 4000 || res.Failed != 0 || len(res.Latencies) != 4000 {
		t.Errorf("due %d, done %d, failed %d, %d latencies; want 4000, 4000, 0 and 4000", res.Due, res.Done, res.Failed, len(res.Latencies))
	}
	if took < 900*time.Millisecond || took > 1500*time.Millisecond {
		t.Errorf("a second of load took %v", took)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for route, want := range map[string]float64{"GET /feed": 100.0 / 217, "GET /posts/:id": 100.0 / 217, "POST /posts/:id/like": 15.0 / 217, "POST /posts": 2.0 / 217} {
		if got := float64(s.hits[route]) / 4000; got < want-0.03 || got > want+0.03 {
			t.Errorf("%s: %.3f of the requests; want %.3f", route, got, want)
		}
	}
	if len(s.conns) == 0 || len(s.conns) > 8 {
		t.Errorf("%d connections; want 8 at most", len(s.conns))
	}
	if len(s.wrong) > 0 {
		t.Errorf("requests the API doesn't have: %v", s.wrong[:min(len(s.wrong), 5)])
	}
	if p50, p99 := res.Percentile(0.5), res.Percentile(0.99); p50 <= 0 || p99 < p50 || p99 > time.Second {
		t.Errorf("p50 %v, p99 %v", p50, p99)
	}
}

// A request that waited for a free connection is as late as it was: its
// time runs from when it was due. And a run ends: what was never sent,
// well after it was due, is failed.
func TestRunCountsTheWait(t *testing.T) {
	s := newServer(t, func(route string, w http.ResponseWriter, r *http.Request) bool {
		time.Sleep(20 * time.Millisecond)
		return false
	})
	start := time.Now()
	// One connection answers 50 a second; 400 are due in half a second.
	res, err := Run(context.Background(), Options{URL: s.URL, Rate: 800, Connections: 1, Duration: 500 * time.Millisecond, Secret: "secret", Users: 100})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Due != 400 || res.Done+res.Failed != res.Due || res.Done == 0 || res.Failed == 0 {
		t.Errorf("due %d, done %d, failed %d; want 400 due, some done, the rest failed", res.Due, res.Done, res.Failed)
	}
	if p50 := res.Percentile(0.5); p50 < 300*time.Millisecond {
		t.Errorf("p50 %v of requests that queued for seconds behind a 20 ms answer", p50)
	}
	if took > 500*time.Millisecond+grace+time.Second {
		t.Errorf("half a second of load took %v", took)
	}
}

// An answer that isn't the route's success, and a connection that fails,
// are failed, and nothing is dropped: due is done and failed.
func TestRunCountsFailures(t *testing.T) {
	s := newServer(t, func(route string, w http.ResponseWriter, r *http.Request) bool {
		switch route {
		case "GET /posts/:id":
			w.WriteHeader(http.StatusInternalServerError)
			return true
		case "POST /posts/:id/like":
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return true
		case "POST /posts":
			fmt.Fprint(w, `{"post":{"id":43}}`) // 200, where a post made is 201
			return true
		}
		return false
	})
	res, err := Run(context.Background(), Options{URL: s.URL, Rate: 2000, Connections: 4, Duration: 500 * time.Millisecond, Secret: "secret", Users: 100})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	feeds := s.hits["GET /feed"]
	s.mu.Unlock()
	if feeds--; res.Due != 1000 || res.Done+res.Failed != 1000 || res.Done != feeds { // one feed was read before the run, for its posts
		t.Errorf("due %d, done %d, failed %d, with %d feeds served; only the feeds are done", res.Due, res.Done, res.Failed, feeds)
	}
	// A failed request took its time too, and the percentiles are of every
	// request that was due, as the challenge's are: not of the done alone.
	if len(res.Latencies) != res.Due {
		t.Errorf("%d times for %d requests due", len(res.Latencies), res.Due)
	}
}

// Failures that were slow are in the percentiles: a run whose answered
// requests were quick, with one in ten failing after 300 ms, has a p95 of
// 300 ms, not of the quick ones.
func TestRunTimesFailures(t *testing.T) {
	var n atomic.Int64
	s := newServer(t, func(route string, w http.ResponseWriter, r *http.Request) bool {
		if n.Add(1)%10 != 0 {
			return false
		}
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	})
	res, err := Run(context.Background(), Options{URL: s.URL, Rate: 200, Connections: 64, Duration: time.Second, Secret: "secret", Users: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed < 15 || res.Failed > 25 || res.Percentile(0.5) > 100*time.Millisecond || res.Percentile(0.95) < 300*time.Millisecond {
		t.Errorf("%d of %d failed, p50 %v, p95 %v; want a tenth failed, and their 300 ms in the p95", res.Failed, res.Due, res.Percentile(0.5), res.Percentile(0.95))
	}
}

// A feed with no posts in it isn't the API's answer, whatever its status:
// with nothing to read or like the load would be feed reads alone, and
// still look like the mix. Before the run, it's why there's no run; in
// one, it's a failed request.
func TestRunNeedsPostsInTheFeed(t *testing.T) {
	for name, feed := range map[string]string{"empty": `{"posts":[]}`, "not the API's": `{"items":[{"id":1}]}`, "not JSON": `<html>`} {
		var served atomic.Int64
		s := newServer(t, func(route string, w http.ResponseWriter, r *http.Request) bool {
			if route == "GET /feed" && (name != "empty" || served.Add(1) > 1) {
				fmt.Fprint(w, feed)
				return true
			}
			return false
		})
		res, err := Run(context.Background(), Options{URL: s.URL, Rate: 200, Connections: 4, Duration: 500 * time.Millisecond, Secret: "secret", Users: 100})
		if name != "empty" {
			if err == nil || !strings.Contains(err.Error(), "feed") {
				t.Errorf("a feed that's %s, before the run: %+v, %v; want no run, and why", name, res, err)
			}
			continue
		}
		// Good before the run, empty in it: those reads are failed.
		s.mu.Lock()
		feeds := s.hits["GET /feed"] - 1
		s.mu.Unlock()
		if err != nil || feeds == 0 || res.Failed != feeds {
			t.Errorf("a feed that's empty in the run: %d failed of %d feed reads, %v", res.Failed, feeds, err)
		}
	}
	// And posts are found however their JSON is laid out.
	s := newServer(t, func(route string, w http.ResponseWriter, r *http.Request) bool {
		if route == "GET /feed" {
			fmt.Fprint(w, `{ "posts": [ {"body": "a {\"id\":7}", "id": 41}, {"like_count": 0, "id": 42} ] }`)
			return true
		}
		return false
	})
	res, err := Run(context.Background(), Options{URL: s.URL, Rate: 400, Connections: 4, Duration: 500 * time.Millisecond, Secret: "secret", Users: 100})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || res.Failed != 0 || s.hits["GET /posts/:id"] == 0 || len(s.wrong) > 0 {
		t.Errorf("ids after other fields: %d failed, %d post reads, wrong %v, %v", res.Failed, s.hits["GET /posts/:id"], s.wrong, err)
	}
}

// A run that's cancelled ends then, not when its last request would have
// been due, and what was due is still counted: failed.
func TestRunStopsWhenCancelled(t *testing.T) {
	s := newServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	res, err := Run(ctx, Options{URL: s.URL, Rate: 500, Connections: 4, Duration: 10 * time.Second, Secret: "secret", Users: 100})
	if took := time.Since(start); err != nil || took > 2*time.Second {
		t.Errorf("a run cancelled at 200 ms ended at %v: %v", took, err)
	}
	if res.Due != 5000 || res.Done+res.Failed != 5000 || res.Done == 0 || res.Done > 200 {
		t.Errorf("due %d, done %d, failed %d; want 5000 due, the first tenth of a second's done, the rest failed", res.Due, res.Done, res.Failed)
	}
	// What was never due had no time to take: none, not less than none.
	if len(res.Latencies) != 5000 || res.Latencies[0] < 0 {
		t.Errorf("%d times, the shortest %v", len(res.Latencies), res.Latencies[0])
	}
}

// The mix is the mix from the first request: at a rate so low that each
// connection sends one request or none, they aren't all feed reads.
func TestRunMixesFromTheStart(t *testing.T) {
	s := newServer(t, nil)
	res, err := Run(context.Background(), Options{URL: s.URL, Rate: 100, Connections: 64, Duration: 600 * time.Millisecond, Secret: "secret", Users: 100})
	s.mu.Lock()
	defer s.mu.Unlock()
	if posts := s.hits["GET /posts/:id"]; err != nil || res.Failed != 0 || posts < 15 || posts > 45 {
		t.Errorf("60 requests on 64 connections: %d post reads, %d failed, %v; want about 28", posts, res.Failed, err)
	}
}

// A run that can't be made says why.
func TestRunNeedsItsOptions(t *testing.T) {
	for _, o := range []Options{
		{URL: "http://x", Rate: 0, Connections: 1, Duration: time.Second, Users: 1},
		{URL: "http://x", Rate: 1, Connections: 0, Duration: time.Second, Users: 1},
		{URL: "http://x", Rate: 1, Connections: 1, Duration: 0, Users: 1},
		{URL: "http://x", Rate: 1, Connections: 1, Duration: time.Second, Users: 0},
		{URL: "", Rate: 1, Connections: 1, Duration: time.Second, Users: 1},
	} {
		if _, err := Run(context.Background(), o); err == nil {
			t.Errorf("%+v ran", o)
		}
	}
}
