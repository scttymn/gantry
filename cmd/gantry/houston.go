package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// execCommand is a command attached to this terminal.
func execCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

// appCommand is `gantry db ...`, `gantry task ...` and `gantry tasks`: the
// app's own command (`myapp db migrate`), run in its dev container by
// houston exec, as bin/rails runs in the app's environment. With --local,
// on this machine instead (go, and the app's tools, installed here).
func appCommand(root string, args []string, errOut io.Writer) error {
	local := false
	var rest []string
	for _, a := range args {
		if a == "--local" {
			local = true
			continue
		}
		rest = append(rest, a)
	}
	name, err := appBinary(root)
	if err != nil {
		return err
	}
	run := append([]string{"go", "run", "./cmd/" + name}, rest...)
	if local {
		return exitError(runIn(root, "env", append([]string{"GANTRY_ENV=development"}, run...)...))
	}
	return houston(root, append([]string{"exec"}, run...), errOut)
}

// houston runs houston with args in root: gantry dev, test, console, deploy
// and the rest are Houston's commands.
func houston(root string, args []string, errOut io.Writer) error {
	if _, err := lookPath("houston"); err != nil {
		fmt.Fprintln(errOut, "gantry runs apps with Houston, which isn't installed (or isn't on PATH).\n"+
			"Install it: https://github.com/scttymn/houston#install, then run this again.")
		return errExit(1)
	}
	return exitError(runIn(root, "houston", args...))
}

var lookPath = exec.LookPath

// appBinary is the app's command, cmd/<name>: the one folder under cmd/.
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

// errExit is an exit code to pass on: a command that ran and failed.
type errExit int

func (e errExit) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// exitError turns a command's failure into its exit code.
func exitError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return errExit(exit.ExitCode())
	}
	return err
}
