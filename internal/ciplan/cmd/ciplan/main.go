// Command ciplan prints the CI checks a change needs (internal/ciplan).
//
//	ciplan -full [-format F]                  every check
//	ciplan -base REF [-worktree] [-format F]  those HEAD's changes since its merge base with REF
//	                                          need, and with -worktree the tree's
//
// F is json, sh (bin/ci's) or github (a step's outputs). When git can't say
// what changed it plans every check; any other failure exits 1.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/scttymn/gantry/internal/ciplan"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "ciplan: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ciplan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "the checkout")
	format := fs.String("format", "json", "json, sh or github")
	full := fs.Bool("full", false, "plan every check")
	base := fs.String("base", "", "plan HEAD's changes since its merge base with this ref")
	worktree := fs.Bool("worktree", false, "with -base, the tree's changes too")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *full == (*base != "") {
		return errors.New("give one of -full and -base REF")
	}
	write := map[string]func(io.Writer, ciplan.Result) error{
		"json": func(w io.Writer, p ciplan.Result) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(p)
		},
		"sh":     ciplan.WriteShell,
		"github": ciplan.WriteGitHub,
	}[*format]
	if write == nil {
		return fmt.Errorf("unknown format %q", *format)
	}

	in, err := ciplan.Load(*root)
	if err != nil {
		return err
	}
	plan := ciplan.FullPlan(in.Specs, in.Scripts)
	if !*full {
		changed, err := ciplan.Changed(*root, *base, *worktree)
		if err != nil {
			fmt.Fprintf(stderr, "ciplan: can't tell what changed since %s, so planning every check: %v\n", *base, err)
		} else {
			plan = ciplan.Plan(changed, in)
		}
	}
	return write(stdout, plan)
}
