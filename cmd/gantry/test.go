package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
)

func testFlags() (fs *flag.FlagSet, local, changed *bool, base *string) {
	fs, local = appFlags()
	changed = fs.Bool("changed", false, "run vet and tests only for Go packages affected by git changes")
	base = fs.String("base", "", "base `REF` for --changed (default origin/main or main)")
	return fs, local, changed, base
}

func testCommand(root string, args []string, out, errOut io.Writer) error {
	if len(args) > 0 && helpFlag(args[0]) {
		return flag.ErrHelp
	}
	_, local, changed, base := testFlags()
	var pass []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pass = append(pass, args[i+1:]...)
			break
		}
		switch {
		case a == "--local":
			*local = true
		case a == "--changed":
			*changed = true
		case a == "--base" && i+1 < len(args):
			i++
			*base = args[i]
		case strings.HasPrefix(a, "--base="):
			*base = strings.TrimPrefix(a, "--base=")
		default:
			pass = append(pass, a)
		}
	}
	if *base != "" && !*changed {
		return usageError{}
	}
	if *changed && len(pass) > 0 {
		return usageError{}
	}
	if !*local && !*changed && len(pass) == 0 {
		return houston(root, []string{"test"}, errOut)
	}

	pkgs := pass
	runVet := false
	if *changed {
		files, _, ok := gitChangedFiles(root, *base)
		if !ok {
			pkgs = []string{"./..."}
			runVet = true
		} else {
			affected, all, err := affectedGoPackages(root, files)
			if err != nil {
				return err
			}
			if all {
				pkgs = []string{"./..."}
				runVet = true
			} else if len(affected) == 0 {
				fmt.Fprintln(out, "gantry test --changed: no Go packages affected")
				return nil
			} else {
				pkgs = affected
				runVet = true
			}
		}
	} else if len(pkgs) == 0 {
		pkgs = []string{"./..."}
		runVet = true
	}

	if *local {
		if runVet {
			vetArgs := append([]string{"GANTRY_ENV=test", "go", "vet"}, pkgs...)
			if err := exitError(runIn(root, "env", vetArgs...)); err != nil {
				return err
			}
		}
		testArgs := append([]string{"GANTRY_ENV=test", "go", "test"}, pkgs...)
		return exitError(runIn(root, "env", testArgs...))
	}

	var cmdParts []string
	quoted := make([]string, len(pkgs))
	for i, p := range pkgs {
		quoted[i] = shellQuote(p)
	}
	joined := strings.Join(quoted, " ")
	if runVet {
		cmdParts = append(cmdParts, "go vet "+joined)
	}
	cmdParts = append(cmdParts, "go test "+joined)
	command := "sh -c " + shellQuote(strings.Join(cmdParts, " && "))
	return runInHoustonTestStack(root, command, errOut)
}

func runInHoustonTestStack(root, command string, errOut io.Writer) error {
	compose, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		return fmt.Errorf("no compose.yml here, for Houston's test stack: %w (or run it on this machine, with --local)", err)
	}
	copied, err := featuresCompose(compose, command)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, verifyCopy)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(copied); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	defer signal.Stop(interrupted)
	return houston(root, []string{"-f", filepath.Base(f.Name()), "test"}, errOut)
}
