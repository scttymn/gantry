package report

import (
	"math"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

// The challenge's line, each part just inside it and just outside.
func TestPasses(t *testing.T) {
	ms := time.Millisecond
	for _, c := range []struct {
		p95, p99    time.Duration
		due, failed int
		want        bool
	}{
		{499 * ms, 999 * ms, 1000, 9, true},
		{500 * ms, 999 * ms, 1000, 0, false},
		{499 * ms, 1000 * ms, 1000, 0, false},
		{499 * ms, 999 * ms, 1000, 10, false},
		{ms, ms, 0, 0, false},  // nothing was due: nothing passed
		{0, 0, 1000, 0, false}, // no times at all
	} {
		if got := Passes(c.p95, c.p99, c.due, c.failed); got != c.want {
			t.Errorf("p95 %v, p99 %v, %d of %d failed: %v", c.p95, c.p99, c.failed, c.due, got)
		}
	}
}

// The challenge's user: 2.17 requests every 20.5 s.
func TestUsers(t *testing.T) {
	if got := Users(10585.4); got != 100000 {
		t.Errorf("10,585.4 requests a second is %d users; want 100,000", got)
	}
	if got := Rate(100000); math.Abs(got-10585.37) > 0.01 {
		t.Errorf("100,000 users are %.2f requests a second", got)
	}
	if Users(0) != 0 || Users(-1) != 0 {
		t.Error("no requests are some users")
	}
}

// An app that passes up to a rate and not past it: the search finds that
// rate within 5%, from a start below it and from one above.
func TestLimitFindsTheCeiling(t *testing.T) {
	for _, ceiling := range []float64{900, 12345, 77000} {
		for _, start := range []float64{500, 2000, 100000} {
			tries := 0
			got, atLeast, err := Limit(func(rate float64) (bool, error) {
				tries++
				return rate <= ceiling, nil
			}, start, 200000)
			if err != nil || atLeast || got > ceiling || got < ceiling*0.95 {
				t.Errorf("a ceiling of %.0f, from %.0f: %.0f (at least: %v), %v", ceiling, start, got, atLeast, err)
			}
			if tries > 24 {
				t.Errorf("a ceiling of %.0f, from %.0f: %d runs", ceiling, start, tries)
			}
		}
	}
}

// An app that never passes has no limit, not the lowest rate tried; one
// that passes at the top passes "at least" that; and a run that fails to
// run stops the search.
func TestLimitWithoutACeiling(t *testing.T) {
	if got, atLeast, err := Limit(func(float64) (bool, error) { return false, nil }, 2000, 200000); got != 0 || atLeast || err != nil {
		t.Errorf("never passing: %.0f (at least: %v), %v", got, atLeast, err)
	}
	if got, atLeast, err := Limit(func(float64) (bool, error) { return true, nil }, 2000, 200000); got != 200000 || !atLeast || err != nil {
		t.Errorf("always passing: %.0f (at least: %v), %v", got, atLeast, err)
	}
	// One request a second is tried before giving up.
	if got, _, err := Limit(func(rate float64) (bool, error) { return rate <= 1.5, nil }, 4000, 400000); got < 1 || got > 1.5 || err != nil {
		t.Errorf("an app good for 1.5 a second: %.2f, %v", got, err)
	}
	down := &profileError{}
	if got, _, err := Limit(func(float64) (bool, error) { return false, down }, 2000, 200000); got != 0 || err != down {
		t.Errorf("a run that failed: %.0f, %v", got, err)
	}
}

type profileError struct{}

func (*profileError) Error() string { return "the app is down" }

// stacks is a CPU profile of samples: each a weight and its stack, the
// innermost frame first.
func stacks(samples map[*[]string]int64) *profile.Profile {
	p := &profile.Profile{SampleType: []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}}}
	locs := map[string]*profile.Location{}
	for stack, weight := range samples {
		s := &profile.Sample{Value: []int64{1, weight}}
		for _, name := range *stack {
			if locs[name] == nil {
				f := &profile.Function{ID: uint64(len(p.Function) + 1), Name: name}
				p.Function = append(p.Function, f)
				locs[name] = &profile.Location{ID: uint64(len(p.Location) + 1), Line: []profile.Line{{Function: f}}}
				p.Location = append(p.Location, locs[name])
			}
			s.Location = append(s.Location, locs[name])
		}
		p.Sample = append(p.Sample, s)
	}
	return p
}

// A sample is the layer's whose frame is innermost, the Go runtime's and
// the standard library's helpers aside: compiling SQL apart from running
// it, a syscall the network's or SQLite's by who made it, garbage made by
// JSON JSON's. A stack no layer has a frame in is "other", not left out:
// the shares are the whole profile.
func TestLayers(t *testing.T) {
	handler := []string{"feed/app/posts.Controller.Index", "github.com/scttymn/gantry/web.(*Router).Wrap.func1", "net/http.(*conn).serve"}
	on := func(frames ...string) *[]string { s := append(frames, handler...); return &s }
	got := Layers(stacks(map[*[]string]int64{
		on("modernc.org/sqlite/lib._sqlite3VdbeExec", "modernc.org/sqlite/lib.Xsqlite3_step", "modernc.org/sqlite.(*stmt).query", "database/sql.(*DB).queryDC", "feed/app/models.(*Queries).Feed"):                                   30,
		on("internal/runtime/syscall/linux.Syscall6", "modernc.org/libc.Xpread", "modernc.org/sqlite/lib._unixRead", "modernc.org/sqlite.(*rows).Next", "database/sql.(*Rows).Next"):                                                 5,
		on("modernc.org/sqlite/lib._yy_reduce", "modernc.org/sqlite/lib.Xsqlite3_prepare_v2", "modernc.org/sqlite.(*conn).prepareV2", "modernc.org/sqlite.newStmt", "database/sql.(*DB).queryDC", "feed/app/models.(*Queries).Feed"): 20,
		on("runtime.mapaccess1_faststr", "database/sql.(*DB).conn", "feed/app/models.(*Queries).Feed"):                                                                                                                               4,
		on("sync.(*Mutex).Lock", "github.com/scttymn/gantry/db.(*cachingConn).stmt", "database/sql.(*DB).queryDC"):                                                                                                                   1,
		{"internal/runtime/syscall/linux.Syscall6", "syscall.write", "internal/poll.(*FD).Write", "net.(*conn).Write", "bufio.(*Writer).Flush", "net/http.(*response).finishRequest", "net/http.(*conn).serve"}:                      12,
		{"runtime.memmove", "bufio.(*Reader).fill", "net/http.(*conn).readRequest", "net/http.(*conn).serve"}:                                                                                                                        3,
		on("runtime.mallocgc", "runtime.growslice", "encoding/json/v2.marshalEncode", "encoding/json.Marshal", "github.com/scttymn/gantry/web.JSON"):                                                                                 8,
		on("strings.EqualFold", "github.com/scttymn/gantry/web.BearerToken", "feed/app/shared/bearer.Tokens.Require"):                                                                                                                2,
		{"compress/flate.(*compressor).deflate", "github.com/scttymn/gantry/compress.(*writer).Write", "net/http.(*conn).serve"}:                                                                                                     1,
		{"time.Now", "github.com/scttymn/gantry/jobs.(*Queue).Run"}:                                                                                                                                                                  3,
		on("crypto/internal/fips140/sha256.blockSHA2", "crypto/hmac.(*hmac).Sum", "feed/app/shared/bearer.Tokens.verify", "feed/app/shared/bearer.Tokens.Require"):                                                                   4,
		{"runtime.scanobject", "runtime.gcDrain", "runtime.gcBgMarkWorker"}:                                                                                                                                                          5,
		{"runtime.futex", "runtime.notesleep", "runtime.schedule", "runtime.mcall"}:                                                                                                                                                  1,
		{"strings.Index", "main.main", "runtime.main"}: 1,
	}), "feed")
	want := map[string]float64{
		RunningSQL: 0.35, CompilingSQL: 0.20, Database: 0.05, Network: 0.15, JSON: 0.08,
		Web: 0.03, Framework: 0.03, App: 0.04, Runtime: 0.06, Other: 0.01,
	}
	total := 0.0
	for _, layer := range LayerOrder {
		total += got[layer]
		if math.Abs(got[layer]-want[layer]) > 1e-9 {
			t.Errorf("%s: %.2f; want %.2f", layer, got[layer], want[layer])
		}
	}
	if math.Abs(total-1) > 1e-9 || len(got) != len(LayerOrder) {
		t.Errorf("the shares are %.4f of the profile, in %d layers; want all of it, in %d", total, len(got), len(LayerOrder))
	}
	// An empty profile is no shares, not a division by nothing.
	if got := Layers(stacks(nil), "feed"); len(got) != len(LayerOrder) || got[Other] != 0 {
		t.Errorf("an empty profile: %v", got)
	}
}

// With SQLite compiled from C, its time is in the calls from gantry's db
// into it: compiling when the call is the one that compiles, running for
// any other. The Go around those calls is gantry's db, as before.
func TestLayersCompiledSQLite(t *testing.T) {
	db := func(frames ...string) *[]string {
		s := append(frames, "database/sql.(*DB).queryDC", "feed/app/models.(*Queries).Feed", "net/http.(*conn).serve")
		return &s
	}
	got := Layers(stacks(map[*[]string]int64{
		db("runtime.cgocall", "github.com/scttymn/gantry/db._Cfunc_gantry_step", "github.com/scttymn/gantry/db.(*cRows).step", "github.com/scttymn/gantry/db.(*cStmt).first"): 6,
		db("runtime.cgocall", "github.com/scttymn/gantry/db._Cfunc_sqlite3_reset", "github.com/scttymn/gantry/db.(*cStmt).reset"):                                             1,
		db("runtime.cgocall", "github.com/scttymn/gantry/db._Cfunc_gantry_prepare", "github.com/scttymn/gantry/db.(*cConn).compile"):                                          2,
		db("runtime.mallocgc", "github.com/scttymn/gantry/db.(*cRows).Next"):                                                                                                  1,
	}), "feed")
	for layer, want := range map[string]float64{RunningSQL: 0.7, CompilingSQL: 0.2, Database: 0.1} {
		if math.Abs(got[layer]-want) > 1e-9 {
			t.Errorf("%s: %.2f; want %.2f", layer, got[layer], want)
		}
	}
}
