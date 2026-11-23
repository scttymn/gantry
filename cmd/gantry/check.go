package main

import (
	"flag"
	"fmt"
	"github.com/scttymn/gantry/devcheck"
	"io"
)

var checkDevelopment = devcheck.Scan

// checkFlags are gantry check's: --strict, or --rules.
func checkFlags() (fs *flag.FlagSet, strict, rules *bool) {
	fs = flagSet("check")
	strict = fs.Bool("strict", false, "fail on a violation")
	rules = fs.Bool("rules", false, "the rules, with their guidance")
	return fs, strict, rules
}

func developmentCheck(root string, args []string, out io.Writer) error {
	fs, strict, rules := checkFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *strict && *rules {
		return usageError{}
	}
	if *rules {
		for _, rule := range devcheck.DefaultRules() {
			fmt.Fprintf(out, "%s [%s]: %s\n  %s\n", rule.ID, rule.Severity, rule.Purpose, rule.Guidance)
		}
		return nil
	}
	warnings, err := checkDevelopment(root)
	if err != nil {
		return err
	}
	failures := 0
	for _, w := range warnings {
		if w.Severity == "error" {
			failures++
		}
		location := w.File
		if w.Line > 0 {
			location = fmt.Sprintf("%s:%d", w.File, w.Line)
		}
		fmt.Fprintf(out, "%s: %s [%s]: %s\n", location, w.Severity, w.Rule, w.Message)
	}
	fmt.Fprintf(out, "gantry check: %d violation(s), %d advisory finding(s)\n", failures, len(warnings)-failures)
	if *strict && failures > 0 {
		return errExit(1)
	}
	return nil
}
