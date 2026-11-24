package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func execCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

func appFlags() (fs *flag.FlagSet, local *bool) {
	fs = flagSet("app")
	local = fs.Bool("local", false, "on this machine, not in the app's container")
	return fs, local
}

func appCommand(command string) func(root string, args []string, out, errOut io.Writer) error {
	return func(root string, args []string, _, errOut io.Writer) error {
		if len(args) > 0 && helpFlag(args[0]) {
			return flag.ErrHelp
		}
		fs, local := appFlags()
		rest := []string{command}
		for _, a := range args {
			if name, ok := strings.CutPrefix(a, "--"); ok && fs.Lookup(name) != nil {
				if err := fs.Set(name, "true"); err != nil {
					return err
				}
				continue
			}
			rest = append(rest, a)
		}
		return runApp(root, rest, *local, errOut)
	}
}

func runApp(root string, args []string, local bool, errOut io.Writer) error {
	name, err := appBinary(root)
	if err != nil {
		return err
	}
	run := append([]string{"go", "run", "./cmd/" + name}, args...)
	if local {
		return exitError(runIn(root, "env", append([]string{"GANTRY_ENV=development"}, run...)...))
	}
	return houston(root, append([]string{"exec"}, run...), errOut)
}

func houston(root string, args []string, errOut io.Writer) error {
	if _, err := lookPath("houston"); err != nil {
		fmt.Fprintln(errOut, "gantry runs apps with Houston, which isn't installed (or isn't on PATH).\n"+
			"Install it: https://github.com/scttymn/houston#install, then run this again.")
		return errExit(1)
	}
	return exitError(runIn(root, "houston", args...))
}

var lookPath = exec.LookPath

func appBinary(root string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		return "", errors.New("no cmd/ here: run gantry at the app's root")
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		return "", fmt.Errorf("cmd/ has %s: gantry runs the app's one command there", strings.Join(dirs, ", "))
	}
	return dirs[0], nil
}

type errExit int

func (e errExit) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func exitError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return errExit(exit.ExitCode())
	}
	return err
}
