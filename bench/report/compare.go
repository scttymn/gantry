package report

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Report is what bench measured of one app (bench/out/*.json): its limit,
// unless it was given its rate, and a run at a rate.
type Report struct {
	Commit      string `json:"commit"`
	Machine     string `json:"machine"`
	Connections int    `json:"connections"`

	LimitRate  float64 `json:"limit_rate"`     // requests a second; 0 when the rate was given
	LimitUsers int     `json:"limit_users"`    // the challenge's users that is
	AtLeast    bool    `json:"limit_at_least"` // the top rate tried passed
	Starved    bool    `json:"starved"`        // the load gave out before the app: the limit reads low

	Rate      float64            `json:"rate"`               // the measured run's: nine tenths of the limit, or the one given
	CPUMicros float64            `json:"cpu_us_per_request"` // the process's, its own and the kernel's for it
	Layers    map[string]float64 `json:"cpu_us_by_layer"`
	P50       float64            `json:"p50_ms"`
	P95       float64            `json:"p95_ms"`
	P99       float64            `json:"p99_ms"`
	Due       int                `json:"due"`
	Failed    int                `json:"failed"`
	RSSPeak   int64              `json:"rss_peak_bytes"`
}

// Run is one measured run of a side: a request's CPU at the rate both sides
// were run at, and what became of the requests.
type Run struct {
	CPUMicros   float64
	Due, Failed int
}

// Side is one of the two gantrys compared: its name, its limit as bin/bench
// found it, its layers at nine tenths of that, and its runs at the rate the
// two share.
type Side struct {
	Name      string // "main (c206c48)"
	LimitRate float64
	AtLeast   bool
	Layers    map[string]float64 // µs a request
	Runs      []Run
}

// Comparison is the verdict on a change, against its base.
type Comparison struct {
	Base, Head float64 // a request's CPU: the median of each side's runs, µs
	Change     float64 // head over base, less one: 0.05 is 5% more
	Noise      float64 // the wider of the sides' spreads, as a share
	Limit      float64 // the head's limit over the base's, less one
	Regressed  bool
	Verdict    string // a sentence
}

// A change in a request's CPU smaller than this can't be told from none,
// however steady the runs; and a limit, found to 5% on each side, has
// fallen when it's this much lower.
const (
	leastChange = 0.03
	limitFall   = 0.10
)

// Compare is the verdict on head against base. Each side's cost is the
// median of its runs, and the noise is the wider of the two sides' spreads
// (highest less lowest, over the median): a change within the noise, or
// within 3%, is no change. Slower is a regression; so is failing requests
// at a rate the base carried, whatever the CPU reads (requests not served
// cost none); and so is a limit a tenth lower. It's an error, not a
// verdict, with fewer than three runs a side or a run that did nothing.
func Compare(base, head Side) (Comparison, error) {
	var c Comparison
	var spread [2]float64
	var failed [2]int
	for i, s := range []Side{base, head} {
		if len(s.Runs) < 3 {
			return c, fmt.Errorf("%s has %d runs: three, at least, to tell a change from a run's own wander", s.Name, len(s.Runs))
		}
		var costs []float64
		for _, r := range s.Runs {
			if r.Due-r.Failed <= 0 || r.CPUMicros <= 0 {
				return c, fmt.Errorf("%s has a run in which nothing was done (%d of %d failed)", s.Name, r.Failed, r.Due)
			}
			costs = append(costs, r.CPUMicros)
			failed[i] += r.Failed
		}
		slices.Sort(costs)
		median := costs[len(costs)/2]
		if len(costs)%2 == 0 {
			median = (costs[len(costs)/2-1] + median) / 2
		}
		spread[i] = (costs[len(costs)-1] - costs[0]) / median
		if i == 0 {
			c.Base = median
		} else {
			c.Head = median
		}
	}
	c.Change, c.Noise = c.Head/c.Base-1, max(spread[0], spread[1])
	if base.LimitRate > 0 {
		c.Limit = head.LimitRate/base.LimitRate - 1
	}
	told := max(leastChange, c.Noise)
	switch {
	case failed[1] > 0 && failed[0] == 0:
		c.Regressed = true
		c.Verdict = fmt.Sprintf("Slower: this change failed %s requests at a rate the base carried.", Thousands(float64(failed[1])))
	case c.Change > told:
		c.Regressed = true
		c.Verdict = fmt.Sprintf("Slower: a request costs %.1f%% more CPU (the runs themselves differ by %.1f%%).", 100*c.Change, 100*c.Noise)
	case c.Limit < -limitFall:
		c.Regressed = true
		c.Verdict = fmt.Sprintf("Slower: the limit is %.1f%% lower (each is found to 5%%).", -100*c.Limit)
	case c.Change < -told:
		c.Verdict = fmt.Sprintf("Faster: a request costs %.1f%% less CPU (the runs themselves differ by %.1f%%).", -100*c.Change, 100*c.Noise)
	default:
		c.Verdict = fmt.Sprintf("The same: a request's CPU differs by %s, inside the %.1f%% that can be told here.", signed(c.Change, 1), 100*told)
	}
	return c, nil
}

// Starved reports whether the load, not the app, gave out. At nine tenths
// of its limit an app is using most of its share of the core (cpu over
// wall); one using under six tenths of it wasn't what stopped the search,
// and its limit is the bench's own, reading low.
func Starved(cpu, wall time.Duration, share float64) bool {
	return wall > 0 && share > 0 && float64(cpu)/float64(wall) < 0.6*share
}

// Markdown is a comparison for people (a pull request's job summary): the
// verdict, each side's limit, a request's CPU at the rate both ran at, and
// each side's layers.
func Markdown(base, head Side, rate float64, c Comparison) string {
	var b strings.Builder
	limit := func(s Side) string {
		atLeast := ""
		if s.AtLeast {
			atLeast = "at least "
		}
		return atLeast + Thousands(s.LimitRate) + " req/s"
	}
	users := func(s Side) string { return Thousands(math.Round(float64(Users(s.LimitRate))/1000) * 1000) }
	fmt.Fprintf(&b, "## gantry bench: %s against %s\n\n**%s**\n\n", head.Name, base.Name, c.Verdict)
	fmt.Fprintf(&b, "| | %s | %s | |\n| --- | ---: | ---: | ---: |\n", base.Name, head.Name)
	fmt.Fprintf(&b, "| The limit | %s | %s | %s |\n", limit(base), limit(head), signed(c.Limit, 0))
	fmt.Fprintf(&b, "| in the challenge's users, about | %s | %s | |\n", users(base), users(head))
	fmt.Fprintf(&b, "| A request's CPU, both at %s req/s | %.1f µs | %.1f µs | %s |\n", Thousands(rate), c.Base, c.Head, signed(c.Change, 0))
	for _, layer := range LayerOrder {
		if base.Layers[layer] >= 0.05 || head.Layers[layer] >= 0.05 {
			fmt.Fprintf(&b, "| %s | %.1f µs | %.1f µs | |\n", layer, base.Layers[layer], head.Layers[layer])
		}
	}
	fmt.Fprintf(&b, "\nOne core, 2 GB, a new seeded database each. The limit is the highest rate that passes the $12 server challenge's line (p95 under 500 ms, p99 under 1 s, under 1%% failed); nothing caps it but the app. A request's CPU is the median of %d runs a side, turn about, which differ among themselves by %.1f%%; both ran at one rate, nine tenths of the lower limit, so the side with the higher limit had more of its core to spare there, and its gain reads smaller than its limit's. The layers are each side's own, at nine tenths of its own limit.\n", len(head.Runs), 100*c.Noise)
	return b.String()
}

// signed is a share as a percentage with its sign: +20%, −18%.
func signed(share float64, decimals int) string {
	return strings.Replace(fmt.Sprintf("%+.*f%%", decimals, 100*share), "-", "−", 1)
}

// Thousands is a whole number with its commas: 26,150.
func Thousands(n float64) string {
	s := fmt.Sprintf("%.0f", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 && s[i-1] != '-' {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
