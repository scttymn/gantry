// bench measures the feed app it's pointed at (bin/bench runs both): the
// highest rate of the challenge's requests that passes its line, then, at
// nine tenths of that, what a request costs in CPU and where the CPU goes.
//
// Nine tenths, not half: an app with time to spare sleeps and wakes between
// requests, and that's CPU too (a fifth more a request at half the limit,
// here), landing in the network's share one run and the runtime's the next.
// Close to the limit there's little of it, and three runs agree to a
// percent.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/pprof/profile"

	"github.com/scttymn/gantry/bench/load"
	"github.com/scttymn/gantry/bench/report"
)

// Report is the bench's answer, as bench/out/report.json has it.
type Report struct {
	Commit      string `json:"commit"`
	Machine     string `json:"machine"`
	Connections int    `json:"connections"`

	LimitRate  float64 `json:"limit_rate"`     // requests a second
	LimitUsers int     `json:"limit_users"`    // the challenge's users that is
	AtLeast    bool    `json:"limit_at_least"` // the top rate tried passed

	Rate      float64            `json:"rate"`               // the measured run's: nine tenths of the limit
	CPUMicros float64            `json:"cpu_us_per_request"` // the process's, its own and the kernel's for it
	Layers    map[string]float64 `json:"cpu_us_by_layer"`
	P50       float64            `json:"p50_ms"`
	P95       float64            `json:"p95_ms"`
	P99       float64            `json:"p99_ms"`
	Due       int                `json:"due"`
	Failed    int                `json:"failed"`
	RSSPeak   int64              `json:"rss_peak_bytes"`
}

func main() {
	var (
		url     = flag.String("url", "http://127.0.0.1:8080", "the app")
		side    = flag.String("bench-url", "http://127.0.0.1:6060", "the app's bench port (BENCH_ADDR)")
		out     = flag.String("o", "report.json", "write the report as JSON here, and the measured run's CPU profile beside it")
		quick   = flag.Bool("quick", false, "shorter runs: a look, not a measurement")
		conns   = flag.Int("connections", 64, "connections, as a proxy holds to an app")
		users   = flag.Int("users", 50000, "the users the app was seeded with")
		secret  = flag.String("secret", "twelve-dollar-challenge", "the app's JWT_SECRET")
		commit  = flag.String("commit", "", "the gantry measured, for the report")
		machine = flag.String("machine", "", "what it ran on, for the report")
	)
	flag.Parse()
	// A rate past what the app can hold still passes a short run, while its
	// queue is short: half a second of queue in 20 s is 2.5% past, which is
	// inside the 5% the search is good to. A quick run's limit reads high.
	trial, measured := 20*time.Second, 30*time.Second
	if *quick {
		trial, measured = 3*time.Second, 6*time.Second
	}
	ctx := context.Background()
	run := func(rate float64, d time.Duration) (load.Result, error) {
		return load.Run(ctx, load.Options{URL: *url, Rate: rate, Connections: *conns, Duration: d, Secret: *secret, Users: *users})
	}
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "bench: "+format+"\n", args...)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "waiting for the app (a new database is seeded first)")
	if err := waitFor(*url+"/up", 15*time.Minute); err != nil {
		fail("%v", err)
	}
	// A warm-up, and a look: an app that answers none of it isn't one to
	// spend the search's minutes on. One that answers some is the search's.
	if res, err := run(1000, 2*time.Second); err != nil || res.Done == 0 {
		fail("warming up, the app answered none of %d requests: %v", res.Due, err)
	}

	fmt.Fprintln(os.Stderr, "looking for the limit")
	limit, atLeast, err := report.Limit(func(rate float64) (bool, error) {
		res, err := run(rate, trial)
		if err != nil {
			return false, err
		}
		p95, p99 := res.Percentile(0.95), res.Percentile(0.99)
		ok := report.Passes(p95, p99, res.Due, res.Failed)
		fmt.Fprintf(os.Stderr, "  %7s req/s: p95 %s, p99 %s, %d failed: %s\n", thousands(rate), ms(p95), ms(p99), res.Failed, map[bool]string{true: "passes", false: "doesn't"}[ok])
		return ok, nil
	}, 4000, 400000)
	if err != nil {
		fail("%v", err)
	}
	if limit == 0 {
		fail("no rate passes the line, down to one request a second")
	}

	r := Report{Commit: *commit, Machine: *machine, Connections: *conns, LimitRate: limit, LimitUsers: report.Users(limit), AtLeast: atLeast, Rate: limit * 0.9}
	fmt.Fprintf(os.Stderr, "measuring at %s req/s\n", thousands(r.Rate))
	before, err := usage(*side)
	if err != nil {
		fail("%v", err)
	}
	// The profile is of the run's seconds and one more, for its last
	// requests to be answered in, and the CPU counted is from before both
	// to after both: the layers' shares are of the same time the total is.
	profiled := make(chan error, 1)
	var prof *profile.Profile
	go func() {
		var err error
		prof, err = cpuProfile(*side, measured+time.Second, strings.TrimSuffix(*out, ".json")+".cpu.prof")
		profiled <- err
	}()
	res, err := run(r.Rate, measured)
	if err != nil {
		fail("%v", err)
	}
	if err := <-profiled; err != nil {
		fail("the profile: %v", err)
	}
	after, err := usage(*side)
	if err != nil {
		fail("%v", err)
	}
	r.Due, r.Failed, r.RSSPeak = res.Due, res.Failed, after.RSSPeak
	r.P50, r.P95, r.P99 = millis(res.Percentile(0.5)), millis(res.Percentile(0.95)), millis(res.Percentile(0.99))
	if res.Done > 0 {
		r.CPUMicros = float64(after.CPU-before.CPU) / float64(res.Done)
	}
	r.Layers = map[string]float64{}
	for layer, share := range report.Layers(prof, "feed") {
		r.Layers[layer] = share * r.CPUMicros
	}

	write(os.Stdout, r)
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fail("%v", err)
	}
	if r.Failed > 0 {
		fail("%d of %d requests failed at nine tenths of the limit", r.Failed, r.Due)
	}
}

func write(w io.Writer, r Report) {
	fmt.Fprintf(w, "\ngantry bench: the feed app on one core with 2 GB, %d connections\n", r.Connections)
	fmt.Fprintf(w, "  gantry %s, on %s\n\n", r.Commit, r.Machine)
	fmt.Fprintf(w, "At %s req/s, nine tenths of the limit, a request cost %.1f µs of CPU:\n", thousands(r.Rate), r.CPUMicros)
	for _, layer := range report.LayerOrder {
		share := 0.0
		if r.CPUMicros > 0 {
			share = 100 * r.Layers[layer] / r.CPUMicros
		}
		fmt.Fprintf(w, "  %-30s %6.1f µs  %3.0f%%\n", layer, r.Layers[layer], share)
	}
	fmt.Fprintf(w, "  p50 %.1f ms, p95 %.1f ms, p99 %.1f ms; %d of %s failed; most memory held %d MB\n\n", r.P50, r.P95, r.P99, r.Failed, thousands(float64(r.Due)), r.RSSPeak>>20)
	atLeast := ""
	if r.AtLeast {
		atLeast = "at least "
	}
	fmt.Fprintf(w, "The limit: %s%s req/s, which is about %s of the challenge's users, on this machine\n", atLeast, thousands(r.LimitRate), thousands(math.Round(float64(r.LimitUsers)/1000)*1000))
	fmt.Fprintln(w, "  (p95 under 500 ms, p99 under 1 s, under 1% failed)")
	if target := report.Rate(100000); r.LimitRate > 0 {
		fmt.Fprintf(w, "100,000 users are %s req/s: %.0f%% of it\n", thousands(target), 100*target/r.LimitRate)
	}
}

// used is what the app's process has used (its bench port's /debug/usage).
type used struct {
	CPU     int64 `json:"cpu_us"`
	RSSPeak int64 `json:"rss_peak_bytes"`
}

// control is for the bench's own requests of the app: one that never
// answers ends the run, and doesn't hang it.
var control = &http.Client{Timeout: 10 * time.Second}

func usage(side string) (u used, err error) {
	res, err := control.Get(side + "/debug/usage")
	if err != nil {
		return u, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return u, fmt.Errorf("%s/debug/usage: %s (is the app built with the bench tag?)", side, res.Status)
	}
	return u, json.NewDecoder(res.Body).Decode(&u)
}

// cpuProfile is the app's CPU profile of the next d, kept at path too, for a
// closer look than the layers give (go tool pprof).
func cpuProfile(side string, d time.Duration, path string) (*profile.Profile, error) {
	slow := &http.Client{Timeout: d + 30*time.Second}
	res, err := slow.Get(fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", side, int(d.Seconds())))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return nil, err
	}
	return profile.Parse(bytes.NewReader(raw))
}

func waitFor(url string, d time.Duration) error {
	for end := time.Now().Add(d); ; time.Sleep(time.Second) {
		res, err := control.Get(url)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(end) {
			return fmt.Errorf("%s didn't answer in %s: %v", url, d, err)
		}
	}
}

func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func ms(d time.Duration) string      { return fmt.Sprintf("%.1f ms", millis(d)) }

// thousands is a whole number with its commas: 26,150.
func thousands(n float64) string {
	s := fmt.Sprintf("%.0f", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
