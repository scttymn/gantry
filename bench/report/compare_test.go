package report

import (
	"strings"
	"testing"
	"time"
)

func side(name string, limit float64, cpus ...float64) Side {
	s := Side{Name: name, LimitRate: limit, Layers: map[string]float64{RunningSQL: cpus[0] / 2, Network: cpus[0] / 4}}
	for _, c := range cpus {
		s.Runs = append(s.Runs, Run{CPUMicros: c, Due: 100000})
	}
	return s
}

// A change is judged by a request's CPU, each side's the median of its
// runs, against what the runs themselves differ by.
func TestCompare(t *testing.T) {
	for _, c := range []struct {
		name       string
		base, head []float64
		regressed  bool
		verdict    string
	}{
		{"the same runs", []float64{40, 40.2, 39.9}, []float64{40.1, 39.8, 40}, false, "same"},
		{"2% more, steady runs: inside the 3%", []float64{40, 40.1, 39.9}, []float64{40.8, 40.9, 40.7}, false, "same"},
		{"10% more, steady runs", []float64{40, 40.1, 39.9}, []float64{44, 44.1, 43.9}, true, "more"},
		{"18% less", []float64{46.9, 47, 46.8}, []float64{38.4, 38.5, 38.3}, false, "less"},
		{"8% more, runs 15% apart: can't be told", []float64{40, 43, 37}, []float64{43.2, 46, 40}, false, "same"},
		{"30% more, runs 15% apart", []float64{40, 43, 37}, []float64{52, 55, 49}, true, "more"},
		{"3% more, to the digit: within 3% is the same", []float64{100, 100, 100}, []float64{103, 103, 103}, false, "same"},
	} {
		got, err := Compare(side("base", 20000, c.base...), side("head", 20000, c.head...))
		if err != nil || got.Regressed != c.regressed || !strings.Contains(got.Verdict, c.verdict) {
			t.Errorf("%s: regressed %v, %q, %v; want %v and %q", c.name, got.Regressed, got.Verdict, err, c.regressed, c.verdict)
		}
	}
	got, _ := Compare(side("base", 20000, 40, 44, 36), side("head", 20000, 50, 50.5, 49.5))
	if got.Base != 40 || got.Head != 50 || got.Change < 0.249 || got.Change > 0.251 || got.Noise < 0.199 || got.Noise > 0.201 {
		t.Errorf("medians %.1f and %.1f, change %.3f, noise %.3f; want 40, 50, 0.25 and 0.20", got.Base, got.Head, got.Change, got.Noise)
	}

	// A side that failed requests at a rate the other carried is slower,
	// though its CPU reads less (the requests it didn't serve cost it none).
	failing := side("head", 20000, 30, 30, 30)
	failing.Runs[1].Failed = 500
	if got, err := Compare(side("base", 20000, 40, 40, 40), failing); err != nil || !got.Regressed || !strings.Contains(got.Verdict, "failed") {
		t.Errorf("the head failing 500 requests: regressed %v, %q, %v", got.Regressed, got.Verdict, err)
	}
	// And the base failing them isn't the change's doing.
	if got, err := Compare(failing, side("head", 20000, 30, 30, 30)); err != nil || got.Regressed {
		t.Errorf("the base failing 500 requests: regressed %v, %q, %v", got.Regressed, got.Verdict, err)
	}
	// Both failing some: it's the share that's compared. The base losing a
	// request a run doesn't excuse the change losing half of them.
	few, many := side("base", 20000, 40, 40, 40), side("head", 20000, 40, 40, 40)
	for i := range 3 {
		few.Runs[i].Failed, many.Runs[i].Failed = 1, 50000
	}
	if got, err := Compare(few, many); err != nil || !got.Regressed || !strings.Contains(got.Verdict, "failed") {
		t.Errorf("the base failing 3 and the head 150,000: regressed %v, %q, %v", got.Regressed, got.Verdict, err)
	}
	if got, err := Compare(many, few); err != nil || got.Regressed {
		t.Errorf("the base failing 150,000 and the head 3: regressed %v, %q, %v", got.Regressed, got.Verdict, err)
	}
	// One request in 300,000, lost to who knows what, isn't a verdict.
	one := side("head", 20000, 40, 40, 40)
	one.Runs[0].Failed = 1
	if got, err := Compare(side("base", 20000, 40, 40, 40), one); err != nil || got.Regressed {
		t.Errorf("the head failing one request in 300,000: regressed %v, %q, %v", got.Regressed, got.Verdict, err)
	}
}

// No verdict from nothing: three runs a side, each with requests done.
func TestCompareNeedsRuns(t *testing.T) {
	good := side("base", 20000, 40, 40, 40)
	none := side("head", 20000, 40, 40, 40)
	none.Runs[2] = Run{CPUMicros: 0, Due: 100000, Failed: 100000}
	for name, head := range map[string]Side{"two runs": side("head", 20000, 40, 40), "no runs": {Name: "head"}, "a run with nothing done": none} {
		if got, err := Compare(good, head); err == nil {
			t.Errorf("%s: %+v, and no error", name, got)
		}
		if got, err := Compare(head, good); err == nil {
			t.Errorf("%s, as the base: %+v, and no error", name, got)
		}
	}
}

// Each limit is found to 5%, so one a tenth under the base's is a fall, and
// less than that isn't.
func TestCompareLimits(t *testing.T) {
	runs := []float64{40, 40, 40}
	for _, c := range []struct {
		base, head float64
		regressed  bool
	}{
		{20000, 20000, false}, {20000, 18500, false}, {20000, 17900, true}, {20000, 26000, false},
		{20000, 18000, true}, // a tenth, to the digit, is a tenth
	} {
		got, err := Compare(side("base", c.base, runs...), side("head", c.head, runs...))
		if err != nil || got.Regressed != c.regressed {
			t.Errorf("limits %.0f and %.0f: regressed %v (%q), %v; want %v", c.base, c.head, got.Regressed, got.Verdict, err, c.regressed)
		}
	}
	if got, _ := Compare(side("base", 20000, runs...), side("head", 17900, runs...)); !strings.Contains(got.Verdict, "limit") {
		t.Errorf("a limit 10.5%% lower: %q", got.Verdict)
	}
	// A limit that isn't the app's says nothing of the app: one the load
	// gave out at, on either side, or one that's only "at least".
	for name, change := range map[string]func(base, head *Side){
		"the head's load gave out": func(base, head *Side) { head.LoadBound = true },
		"the base's load gave out": func(base, head *Side) { base.LoadBound = true },
		"the head's is at least":   func(base, head *Side) { head.AtLeast = true },
	} {
		base, head := side("base", 20000, runs...), side("head", 15000, runs...)
		change(&base, &head)
		if got, err := Compare(base, head); err != nil || got.Regressed {
			t.Errorf("a limit a quarter lower, but %s: regressed %v (%q), %v", name, got.Regressed, got.Verdict, err)
		}
	}
}

// The load generator out of CPU is what makes a limit the bench's own: it
// used most of the cores it had. That's told from its side, not the app's.
func TestLoadBound(t *testing.T) {
	second := time.Second
	for _, c := range []struct {
		cpu, wall time.Duration
		cores     int
		want      bool
	}{
		{80 * second, 30 * second, 3, true},    // 2.7 of 3 cores
		{40 * second, 30 * second, 3, false},   // 1.3 of 3
		{28 * second, 30 * second, 1, true},    // the one it has
		{200 * second, 30 * second, 17, false}, // 6.7 of 17
		{0, 0, 3, false},
	} {
		if got := LoadBound(c.cpu, c.wall, c.cores); got != c.want {
			t.Errorf("the load using %v of CPU in %v on %d cores: bound %v", c.cpu, c.wall, c.cores, got)
		}
	}
}

// At nine tenths of its limit an app is using most of its core. One using
// little of it was stopped by something else: its disk, a lock, or the
// load. Which, this doesn't say.
func TestIdle(t *testing.T) {
	second := time.Second
	for _, c := range []struct {
		cpu, wall time.Duration
		share     float64
		want      bool
	}{
		{25 * second, 30 * second, 1, false},
		{17 * second, 30 * second, 1, true},
		{13 * second, 30 * second, 0.5, false}, // most of the half it has
		{8 * second, 30 * second, 0.5, true},
		{0, 0, 1, false}, // nothing measured: nothing claimed
	} {
		if got := Idle(c.cpu, c.wall, c.share); got != c.want {
			t.Errorf("%v of CPU in %v, with %.1f of a core: idle %v", c.cpu, c.wall, c.share, got)
		}
	}
}

// The summary a pull request's job shows.
func TestMarkdown(t *testing.T) {
	base, head := side("main (c206c48)", 20000, 46.9, 47, 46.8), side("this change (55a4548)", 24000, 38.4, 38.5, 38.3)
	c, err := Compare(base, head)
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(base, head, 18000, c)
	for _, want := range []string{
		"main (c206c48)", "this change (55a4548)",
		"20,000 req/s", "24,000 req/s", "189,000", "227,000", "+20%",
		"46.9 µs", "38.4 µs", "−18%", "18,000 req/s",
		RunningSQL, Network,
		c.Verdict,
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the summary has no %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "failed") && !strings.Contains(md, "under 1% failed") || strings.Contains(md, "Requests failed") {
		t.Errorf("no request failed, and the summary has a row for them:\n%s", md)
	}
	head.AtLeast = true
	if md := Markdown(base, head, 18000, c); !strings.Contains(md, "at least 24,000 req/s") {
		t.Errorf("a limit that's the top rate tried doesn't say at least:\n%s", md)
	}
	// What bears on the verdict is in it: requests failed, and a limit the
	// load gave out at.
	head.Runs[0].Failed, head.LoadBound = 12, true
	if md := Markdown(base, head, 18000, c); !strings.Contains(md, "| Requests failed, of 300,000 a side | 0 | 12 |") || !strings.Contains(md, "the load gave out") {
		t.Errorf("12 requests failed and the load gave out, and the summary doesn't say:\n%s", md)
	}
}
