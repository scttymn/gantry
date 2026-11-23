package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/scttymn/gantry/devcheck"
	"io"
)

var checkDevelopment = devcheck.Scan

// checkFlags are gantry check's: --strict or --rules, and --json.
func checkFlags() (fs *flag.FlagSet, strict, rules, asJSON *bool) {
	fs = flagSet("check")
	strict = fs.Bool("strict", false, "fail on a violation")
	rules = fs.Bool("rules", false, "the rules, with their guidance")
	asJSON = fs.Bool("json", false, "as JSON, with stable field names (docs/development.md)")
	return fs, strict, rules, asJSON
}

// checkReport is check --json's: the counts, and every finding in the
// order the text lists them. Its field names are stable.
type checkReport struct {
	Violations int            `json:"violations"`
	Advisories int            `json:"advisories"`
	Findings   []checkFinding `json:"findings"`
}

type checkFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"` // 0: the file as a whole
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // "error" (a violation) or "advice"
	Message  string `json:"message"`
}

type checkRule struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Purpose  string `json:"purpose"`
	Guidance string `json:"guidance"`
}

func developmentCheck(root string, args []string, out io.Writer) error {
	fs, strict, rules, asJSON := checkFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *strict && *rules {
		return usageError{}
	}
	if *rules {
		var list []checkRule
		for _, rule := range devcheck.DefaultRules() {
			if !*asJSON {
				fmt.Fprintf(out, "%s [%s]: %s\n  %s\n", rule.ID, rule.Severity, rule.Purpose, rule.Guidance)
			}
			list = append(list, checkRule{rule.ID, rule.Severity, rule.Purpose, rule.Guidance})
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(list)
		}
		return nil
	}
	warnings, err := checkDevelopment(root)
	if err != nil {
		return err
	}
	report := checkReport{Findings: []checkFinding{}}
	for _, w := range warnings {
		if w.Severity == devcheck.Violation {
			report.Violations++
		} else {
			report.Advisories++
		}
		report.Findings = append(report.Findings, checkFinding{w.File, w.Line, w.Rule, w.Severity, w.Message})
	}
	if *asJSON {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return err
		}
	} else {
		for _, f := range report.Findings {
			location := f.File
			if f.Line > 0 {
				location = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(out, "%s: %s [%s]: %s\n", location, f.Severity, f.Rule, f.Message)
		}
		fmt.Fprintf(out, "gantry check: %d violation(s), %d advisory finding(s)\n", report.Violations, report.Advisories)
	}
	if *strict && report.Violations > 0 {
		return errExit(1)
	}
	return nil
}
