package main

import (
	"fmt"
	"github.com/scttymn/gantry/devcheck"
	"io"
)

var checkDevelopment = devcheck.Scan

func developmentCheck(root string, args []string, out io.Writer) error {
	if len(args) == 1 && args[0] == "--rules" {
		for _, rule := range devcheck.DefaultRules() {
			fmt.Fprintf(out, "%s [%s]: %s\n  %s\n", rule.ID, rule.Severity, rule.Purpose, rule.Guidance)
		}
		return nil
	}
	strict := len(args) == 1 && args[0] == "--strict"
	if len(args) > 0 && !strict {
		return fmt.Errorf("usage: gantry check [--strict|--rules]")
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
		fmt.Fprintf(out, "%s:%d: %s [%s]: %s\n", w.File, w.Line, w.Severity, w.Rule, w.Message)
	}
	fmt.Fprintf(out, "gantry check: %d violation(s), %d advisory finding(s)\n", failures, len(warnings)-failures)
	if strict && failures > 0 {
		return errExit(1)
	}
	return nil
}
