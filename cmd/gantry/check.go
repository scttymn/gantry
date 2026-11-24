package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/scttymn/gantry/devcheck"
	"io"
)

var (
	checkDevelopment      = devcheck.Scan
	checkDevelopmentPaths = devcheck.ScanPaths
)

func checkFlags() (fs *flag.FlagSet, strict, rules, asJSON, generated, changed *bool, base *string) {
	fs = flagSet("check")
	strict = fs.Bool("strict", false, "fail on a violation")
	rules = fs.Bool("rules", false, "the rules, with their guidance")
	asJSON = fs.Bool("json", false, "as JSON, with stable field names (docs/development.md)")
	generated = fs.Bool("generated", false, "fail on generated Go (templ's, sqlc's) that isn't what its source makes")
	changed = fs.Bool("changed", false, "scan only files changed in git")
	base = fs.String("base", "", "base `REF` for --changed (default origin/main or main)")
	return fs, strict, rules, asJSON, generated, changed, base
}

type checkReport struct {
	Violations int            `json:"violations"`
	Advisories int            `json:"advisories"`
	Findings   []checkFinding `json:"findings"`
}

type checkFinding struct {
	File string `json:"file"`
	// 0: the file as a whole
	Line int    `json:"line"`
	Rule string `json:"rule"`
	// "error" (a violation) or "advice"
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type checkRule struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Purpose  string `json:"purpose"`
	Guidance string `json:"guidance"`
}

func developmentCheck(root string, args []string, out io.Writer) error {
	fs, strict, rules, asJSON, generated, changed, base := checkFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	paths := fs.Args()
	if (*base != "" && !*changed) ||
		(*changed && (len(paths) > 0 || *rules || *generated)) ||
		(*strict && *rules) ||
		(*generated && (*strict || *rules || *asJSON || len(paths) > 0)) ||
		(*rules && len(paths) > 0) {
		return usageError{}
	}
	if *generated {
		return generatedCheck(root, out)
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
	var (
		warnings []devcheck.Warning
		err      error
	)
	switch {
	case *changed:
		files, _, ok := gitChangedFiles(root, *base)
		if !ok {
			warnings, err = checkDevelopment(root)
		} else if len(files) > 0 {
			warnings, err = checkDevelopmentPaths(root, files)
		}
	case len(paths) > 0:
		warnings, err = checkDevelopmentPaths(root, paths)
	default:
		warnings, err = checkDevelopment(root)
	}
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
