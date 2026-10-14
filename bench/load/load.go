// Package load is the challenge's users, as requests: its mix of feed
// reads, post reads, likes and posts, due at a set rate on a set number of
// connections, as a proxy in front of an app delivers them.
package load

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// grace is how long after a run's last request was due the run goes on:
// what hasn't been answered by then is failed.
const grace = 2 * time.Second

// Options is a run.
type Options struct {
	URL         string        // the app's: "http://feed:8080"
	Rate        float64       // requests due a second
	Connections int           // at most this many at once, each on its own
	Duration    time.Duration // requests are due for this long
	Secret      string        // signs the tokens likes and posts carry
	Users       int           // the tokens are users 1 to Users'
	// Gzip asks for compressed answers, as a browser does: for an app
	// reached across the internet, where the bytes are what there's least of.
	Gzip bool
}

// Result is what became of every request that was due.
type Result struct {
	Due, Done, Failed int
	// Latencies are every due request's time, the failed ones' too (as the
	// challenge's percentiles have them): from when it was due to its
	// answer's last byte, or to when it was given up on. Shortest first.
	Latencies []time.Duration
	// Spun is how long the run's timekeeper spent spinning, waiting for the
	// next request's moment: CPU the load used on purpose, not for want of it.
	Spun time.Duration
}

// Percentile is the time within which that share (0.95, 0.99) of the
// requests due were answered or given up on; 0 when none were due.
func (r Result) Percentile(p float64) time.Duration {
	n := len(r.Latencies)
	if n == 0 {
		return 0
	}
	return r.Latencies[min(max(int(float64(n)*p+0.5)-1, 0), n-1)]
}

// Run sends the load and waits for it. Each request has its moment, the
// rate apart, whether or not the app keeps up: one sent late, for want of
// a free connection, is timed from when it was due, so an app that falls
// behind shows it. A request is done when its answer is its route's
// success, and failed otherwise, or when it wasn't answered by grace after
// the last was due.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.URL == "" || o.Rate <= 0 || o.Connections <= 0 || o.Duration <= 0 || o.Users <= 0 {
		return Result{}, fmt.Errorf("load: a run needs a URL, a rate, connections, a duration and users: %+v", o)
	}
	transport := &http.Transport{MaxConnsPerHost: o.Connections, MaxIdleConnsPerHost: o.Connections, MaxIdleConns: o.Connections, DisableCompression: !o.Gzip}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	tokens := tokens(o.Secret, o.Users)

	// A feed is read before the run, for posts to read and like: the mix is
	// the mix from the first request, at any rate. An app whose feed can't
	// be read, or has nothing in it, has no run.
	first := user{url: o.URL, client: client}
	early, stop := context.WithTimeout(ctx, 10*time.Second)
	ok := first.feed(early)
	stop()
	if !ok {
		return Result{}, fmt.Errorf("load: %s/feed, read before the run, wasn't a feed with posts in it", o.URL)
	}

	due := int(o.Rate * o.Duration.Seconds())
	start := time.Now()
	ctx, cancel := context.WithDeadline(ctx, start.Add(o.Duration+grace))
	defer cancel()

	// One goroutine keeps the time, and hands each request's moment to
	// whichever connection is free. It spins through the last of each wait:
	// a timer wakes a millisecond or more late, which would be in every
	// request's time.
	moments := make(chan time.Time, due)
	var spun atomic.Int64
	go func() {
		defer close(moments)
		for i := range due {
			at := start.Add(time.Duration(float64(i) / o.Rate * float64(time.Second)))
			if wait := time.Until(at); wait > 2*time.Millisecond {
				select {
				case <-time.After(wait - 2*time.Millisecond):
				case <-ctx.Done():
				}
			}
			// A run that's over (cancelled, or past its grace) doesn't wait
			// for the rest: they're due all the same, and failed.
			for now := time.Now(); now.Before(at) && ctx.Err() == nil; {
				runtime.Gosched()
				next := time.Now()
				spun.Add(int64(next.Sub(now)))
				now = next
			}
			moments <- at
		}
	}()

	var (
		wg     sync.WaitGroup
		times  = make([][]time.Duration, o.Connections)
		failed = make([]int, o.Connections)
	)
	for w := range o.Connections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Seeds well apart: generators seeded 0, 1, 2 ... start out alike,
			// and the mix across connections comes out skewed.
			u := user{url: o.URL, client: client, tokens: tokens, ids: slices.Clone(first.ids), rng: rand.New(rand.NewPCG((uint64(w)+1)*0x9E3779B97F4A7C15, 2026))}
			for at := range moments {
				if ctx.Err() != nil || !u.request(ctx) {
					failed[w]++
				}
				// One that was never due (the run ended first) took no time.
				times[w] = append(times[w], max(time.Since(at), 0))
			}
		}()
	}
	wg.Wait()

	res := Result{Due: due, Spun: time.Duration(spun.Load())}
	for w := range o.Connections {
		res.Latencies = append(res.Latencies, times[w]...)
		res.Failed += failed[w]
	}
	res.Done = due - res.Failed
	slices.Sort(res.Latencies)
	return res, nil
}

// user is one connection's worth of the challenge's users: it reads the
// feed, then reads and likes posts that feed had, and writes its own.
type user struct {
	url    string
	client *http.Client
	tokens []string
	rng    *rand.Rand
	ids    []string // the posts of the feed it last read
}

// request makes the next request of the mix, 100 feed : 100 post : 15 like
// : 2 create, and reports whether it was answered as its route answers.
func (u *user) request(ctx context.Context) bool {
	switch x := u.rng.IntN(217); {
	case x < 100:
		return u.feed(ctx)
	case x < 200:
		_, status, err := u.do(ctx, "GET", "/posts/"+u.ids[u.rng.IntN(len(u.ids))], "", nil)
		return err == nil && status == http.StatusOK
	case x < 215:
		_, status, err := u.do(ctx, "POST", "/posts/"+u.ids[u.rng.IntN(len(u.ids))]+"/like", u.token(), nil)
		return err == nil && (status == http.StatusCreated || status == http.StatusOK)
	default:
		post := []byte(`{"body":"says hi at ` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)
		_, status, err := u.do(ctx, "POST", "/posts", u.token(), post)
		return err == nil && status == http.StatusCreated
	}
}

// feed reads the feed, and keeps its posts to read and like. A feed with no
// posts in it isn't the API's answer, whatever its status: the posts it
// had before are kept.
func (u *user) feed(ctx context.Context) bool {
	body, status, err := u.do(ctx, "GET", "/feed", "", nil)
	if err != nil || status != http.StatusOK {
		return false
	}
	var feed struct {
		Posts []struct {
			ID json.Number `json:"id"`
		} `json:"posts"`
	}
	if json.Unmarshal(body, &feed) != nil || len(feed.Posts) == 0 {
		return false
	}
	u.ids = u.ids[:0]
	for _, p := range feed.Posts {
		u.ids = append(u.ids, p.ID.String())
	}
	return true
}

func (u *user) token() string { return u.tokens[u.rng.IntN(len(u.tokens))] }

func (u *user) do(ctx context.Context, method, path, token string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.url+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := u.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, 0, err
	}
	return answer, res.StatusCode, nil
}

// tokens are the challenge's bearer tokens (HS256, a day to live) for up to
// 2,000 of the users, spread across them.
func tokens(secret string, users int) []string {
	enc := base64.RawURLEncoding.EncodeToString
	head := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now().Unix()
	n := min(users, 2000)
	out := make([]string, n)
	for i := range n {
		id := strconv.Itoa(i*users/n + 1)
		signed := head + "." + enc([]byte(`{"sub":"`+id+`","username":"user_`+id+`","iat":`+strconv.FormatInt(now, 10)+`,"exp":`+strconv.FormatInt(now+86400, 10)+`}`))
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(signed))
		out[i] = signed + "." + enc(mac.Sum(nil))
	}
	return out
}
