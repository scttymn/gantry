package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func testFlags() (fs *flag.FlagSet, local, changed *bool, base *string) {
	fs, local = appFlags()
	changed = fs.Bool("changed", false, "only the packages a change affects, and those that import them")
	base = fs.String("base", "", "compare with where the branch left `REF` (default origin/main, or main)")
	return fs, local, changed, base
}

func testCommand(root string, args []string, out, errOut io.Writer) error {
	fs, local, changed, base := testFlags()
	if len(args) > 0 && helpFlag(args[0]) {
		return flag.ErrHelp
	}
	if !slices.ContainsFunc(args, func(arg string) bool {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		return strings.HasPrefix(arg, "-") && fs.Lookup(name) != nil
	}) {
		return houston(root, append([]string{"test"}, args...), errOut)
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	baseGiven := false
	fs.Visit(func(f *flag.Flag) { baseGiven = baseGiven || f.Name == "base" })
	if fs.NArg() > 0 || baseGiven && !*changed {
		return usageError{}
	}
	if baseGiven && strings.TrimSpace(*base) == "" {
		return usageError{errors.New("--base names no ref")}
	}
	pkgs := []string{"./..."}
	if *changed {
		scope := affectedPackages(root, *base)
		switch {
		case scope.everythingBecause != "":
			fmt.Fprintf(errOut, "gantry test --changed: %s, so every package runs\n", scope.everythingBecause)
		case len(scope.pkgs) == 0:
			fmt.Fprintln(out, "gantry test --changed: no Go packages affected")
			return nil
		default:
			pkgs = scope.pkgs
		}
	}
	if *local {
		localEnv := []string{"GANTRY_ENV=test", "GANTRY_VERIFY_FEATURES="}
		if err := exitError(runIn(root, "env", slices.Concat(localEnv, []string{"go", "vet"}, pkgs)...)); err != nil {
			return err
		}
		return exitError(runIn(root, "env", slices.Concat(localEnv, []string{"go", "test"}, pkgs)...))
	}
	if slices.Equal(pkgs, []string{"./..."}) {
		return houston(root, []string{"test"}, errOut)
	}
	compose, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		return fmt.Errorf("no compose.yml here, for Houston's test stack: %w (or run it on this machine, with --local)", err)
	}
	command, ok := narrowed(houstonTestCommand(compose), pkgs)
	if !ok {
		fmt.Fprintln(errOut, "gantry test --changed: can't narrow compose.yml's x-houston.commands.test to the packages, so it runs whole")
		return houston(root, []string{"test"}, errOut)
	}
	return inTestStack(root, compose, command, func(houstonArgs []string) error { return houston(root, houstonArgs, errOut) })
}

func houstonTestCommand(compose []byte) string {
	var doc yaml.Node
	if yaml.Unmarshal(compose, &doc) != nil || len(doc.Content) != 1 {
		return ""
	}
	test := doc.Content[0]
	for _, key := range []string{"x-houston", "commands", "test"} {
		if test = value(test, key); test == nil {
			return ""
		}
	}
	if test.Kind != yaml.ScalarNode {
		return ""
	}
	return test.Value
}

var (
	goVetOrTest = regexp.MustCompile(`^\s*go (vet|test)\s`)
	plainPkg    = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+$`)
)

func narrowed(command string, pkgs []string) (string, bool) {
	if slices.ContainsFunc(pkgs, func(p string) bool { return !plainPkg.MatchString(p) }) {
		return "", false
	}
	const all = "./..."
	parts := strings.Split(command, all)
	if len(parts) < 2 {
		return "", false
	}
	for i := range parts[:len(parts)-1] {
		before := strings.Join(parts[:i+1], all)
		after := parts[i+1]
		segment := before[strings.LastIndexAny(before, "&|;\"'(\n")+1:]
		if !goVetOrTest.MatchString(segment) || !strings.HasSuffix(before, " ") || after != "" && !strings.ContainsAny(after[:1], " \"';&|)\n") {
			return "", false
		}
	}
	return strings.Join(parts, strings.Join(pkgs, " ")), true
}
