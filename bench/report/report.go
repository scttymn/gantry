// Package report turns a run's numbers into the bench's answers: whether a
// rate passed, the highest that does, how many users that is, and where a
// request's CPU went.
package report

import (
	"math"
	"strings"
	"time"

	"github.com/google/pprof/profile"
)

// Passes is the challenge's line: p95 under 500 ms, p99 under 1 s (of every
// request due, the failed ones too), and under 1% of them failed. A run
// nothing was due in (no p95) didn't pass.
func Passes(p95, p99 time.Duration, due, failed int) bool {
	return due > 0 && p95 > 0 && p95 < 500*time.Millisecond && p99 < time.Second && failed*100 < due
}

// requestsAUser is the challenge's user, in requests a second: its loop is
// a feed, a post, a like 15 times in 100 and a post of its own 2 in 100,
// every 20.5 s of thinking.
const requestsAUser = 2.17 / 20.5

// Users is how many of the challenge's users make rate requests a second,
// and Rate the requests a second of that many.
func Users(rate float64) int {
	if rate <= 0 {
		return 0
	}
	return int(math.Round(rate / requestsAUser))
}

func Rate(users int) float64 { return float64(users) * requestsAUser }

// Limit is the highest rate, within 5%, that try passes, looking from
// start: doubling while it passes and halving while it doesn't, then
// closing in. It's 0 when no rate passes (down to one request a second),
// and top, atLeast, when top itself passes. An error from try ends it.
func Limit(try func(rate float64) (bool, error), start, top float64) (rate float64, atLeast bool, err error) {
	passed, failed := 0.0, 0.0
	rate = min(start, top)
	ok, err := try(rate)
	if err != nil {
		return 0, false, err
	}
	if ok {
		for passed = rate; passed < top; passed = rate {
			rate = min(passed*2, top)
			if ok, err = try(rate); err != nil {
				return 0, false, err
			} else if !ok {
				failed = rate
				break
			}
		}
		if failed == 0 {
			return top, true, nil
		}
	} else {
		for failed = rate; passed == 0; {
			if failed <= 1 {
				return 0, false, nil
			}
			rate = max(failed/2, 1)
			if ok, err = try(rate); err != nil {
				return 0, false, err
			} else if ok {
				passed = rate
			} else {
				failed = rate
			}
		}
	}
	for (failed-passed)/passed > 0.05 {
		rate = (passed + failed) / 2
		if ok, err = try(rate); err != nil {
			return 0, false, err
		} else if ok {
			passed = rate
		} else {
			failed = rate
		}
	}
	return passed, false, nil
}

// The layers a request's CPU is divided among, in the report's order.
const (
	RunningSQL   = "running queries"
	CompilingSQL = "compiling SQL"
	Database     = "database/sql and gantry's db"
	Network      = "the network"
	JSON         = "JSON"
	Web          = "gantry's web"
	Framework    = "the rest of gantry"
	App          = "the app"
	Runtime      = "Go's runtime by itself"
	Other        = "other"
)

var LayerOrder = []string{RunningSQL, CompilingSQL, Database, Network, JSON, Web, Framework, App, Runtime, Other}

// packages are the layers' own, the more particular first. What isn't here
// (the runtime, syscalls, strings, crypto and the rest of the standard
// library) is whoever called it.
var packages = []struct{ pkg, layer string }{
	{"modernc.org/sqlite", RunningSQL},
	{"modernc.org/libc", RunningSQL},
	{"modernc.org/memory", RunningSQL},
	{"database/sql", Database},
	{"github.com/scttymn/gantry/db", Database},
	{"encoding/json", JSON},
	{"github.com/scttymn/gantry/web", Web},
	{"github.com/scttymn/gantry/compress", Web},
	{"github.com/scttymn/gantry", Framework},
	{"net", Network},
	{"internal/poll", Network},
	{"crypto/tls", Network},
}

// cSQLite begins the names of gantry's db's calls into SQLite compiled from
// C (db/sqlite_c.go): the time in C is the call's.
const cSQLite = "github.com/scttymn/gantry/db._Cfunc_"

// in reports whether a function ("net/http.(*conn).serve") is in a package
// or under it.
func in(function, pkg string) bool {
	return strings.HasPrefix(function, pkg+".") || strings.HasPrefix(function, pkg+"/")
}

// Layers is each layer's share of a CPU profile, 0 to 1; app is the app's
// module ("feed"). A sample is the layer's whose frame is innermost in its
// stack: a syscall under net/http is the network's, one under SQLite is
// SQLite's, and garbage made encoding JSON is JSON's. SQLite's time is
// compiling when the driver's prepare is in the stack, and running
// otherwise; compiled from C, it's the calls from gantry's db into it. A
// stack that's the runtime's alone (the collector, the
// scheduler) is the runtime's, and one no layer has a frame in is other:
// the shares are the whole profile.
func Layers(p *profile.Profile, app string) map[string]float64 {
	sums := map[string]int64{}
	var total int64
	for _, s := range p.Sample {
		v := s.Value[len(s.Value)-1]
		total += v
		sums[layerOf(s, app)] += v
	}
	shares := map[string]float64{}
	for _, layer := range LayerOrder {
		shares[layer] = 0
		if total > 0 {
			shares[layer] = float64(sums[layer]) / float64(total)
		}
	}
	return shares
}

func layerOf(s *profile.Sample, app string) string {
	layer, compiling, runtimeOnly := "", false, true
	for _, loc := range s.Location {
		for _, line := range loc.Line {
			name := line.Function.Name
			if name == "modernc.org/sqlite.(*conn).prepareV2" || name == cSQLite+"gantry_prepare" {
				compiling = true
			}
			if !in(name, "runtime") && !in(name, "internal/runtime") {
				runtimeOnly = false
			}
			if layer != "" {
				continue
			}
			if in(name, app) {
				layer = App
			}
			if strings.HasPrefix(name, cSQLite) {
				layer = RunningSQL
			}
			for _, p := range packages {
				if layer == "" && in(name, p.pkg) {
					layer = p.layer
				}
			}
		}
	}
	switch {
	case layer == RunningSQL && compiling:
		return CompilingSQL
	case layer != "":
		return layer
	case runtimeOnly:
		return Runtime
	}
	return Other
}
