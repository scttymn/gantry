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
	side := func(file, name string) report.Side {
		whole, err := read(filepath.Join(*dir, file+".json"))
		if err != nil {
			fail("%v", err)
		}
		s := report.Side{Name: name, LimitRate: whole.LimitRate, AtLeast: whole.AtLeast, Layers: whole.Layers}
		runs, _ := filepath.Glob(filepath.Join(*dir, file+".[0-9]*.json"))
		for _, path := range runs {
			r, err := read(path)
			if err != nil {
				fail("%v", err)
			}
			if rate != 0 && r.Rate != rate {
				fail("%s was run at %.0f req/s, and others at %.0f: the sides are compared at one rate", path, r.Rate, rate)
			}
			rate = r.Rate
			s.Runs = append(s.Runs, report.Run{CPUMicros: r.CPUMicros, Due: r.Due, Failed: r.Failed})
		}
		return s
	}
	b, h := side("base", *base), side("head", *head)
	c, err := report.Compare(b, h)
	if err != nil {
		fail("%v", err)
	}
	fmt.Print(report.Markdown(b, h, rate, c))
	if c.Regressed {
		os.Exit(1)
	}
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
