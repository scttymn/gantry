// compare is the verdict on one gantry against another (bin/bench against):
// it reads the reports bench wrote of each (its limit and layers, and its
// runs at the rate the two shared), writes the comparison as Markdown, and
// exits 1 when the change is a regression.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/scttymn/gantry/bench/report"
)

func main() {
	var (
		dir  = flag.String("dir", ".", "where the reports are: base.json, head.json, base.1.json ...")
		base = flag.String("base", "the base", "the base's name")
		head = flag.String("head", "this change", "the change's name")
	)
	flag.Parse()
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "compare: "+format+"\n", args...)
		os.Exit(1)
	}
	var rate float64
	b, err := side(*dir, "base", *base, &rate)
	if err != nil {
		fail("%v", err)
	}
	h, err := side(*dir, "head", *head, &rate)
	if err != nil {
		fail("%v", err)
	}
	c, err := report.Compare(b, h)
	if err != nil {
		fail("%v", err)
	}
	fmt.Print(report.Markdown(b, h, rate, c))
	if c.Regressed {
		os.Exit(1)
	}
}

// side is one side's reports in dir: file.json, its limit and layers, and
// its runs, file.1.json and on (those, and no other file that starts the
// same). Every run was at one rate, and at the other side's: rate is that
// rate, set by the first run read.
func side(dir, file, name string, rate *float64) (report.Side, error) {
	whole, err := read(filepath.Join(dir, file+".json"))
	if err != nil {
		return report.Side{}, err
	}
	s := report.Side{Name: name, LimitRate: whole.LimitRate, AtLeast: whole.AtLeast, LoadBound: whole.LoadBound, Layers: whole.Layers}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s, err
	}
	numbered := regexp.MustCompile(`\A` + regexp.QuoteMeta(file) + `\.[0-9]+\.json\z`)
	for _, e := range entries {
		if !numbered.MatchString(e.Name()) {
			continue
		}
		r, err := read(filepath.Join(dir, e.Name()))
		if err != nil {
			return s, err
		}
		if r.Rate <= 0 {
			return s, fmt.Errorf("%s has no rate: a run is at one", e.Name())
		}
		if *rate != 0 && r.Rate != *rate {
			return s, fmt.Errorf("%s was run at a rate of %.0f req/s, and others at %.0f: the sides are compared at one", e.Name(), r.Rate, *rate)
		}
		*rate = r.Rate
		s.Runs = append(s.Runs, report.Run{CPUMicros: r.CPUMicros, Due: r.Due, Failed: r.Failed})
	}
	return s, nil
}

func read(path string) (r report.Report, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
