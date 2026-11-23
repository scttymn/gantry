package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// verify is `gantry verify`: the app's TestFeatures (testkit.Verify), which
// checks test/features.yaml has every route and visits each feature as each
// role, the app in the test's process. It runs in the app's dev container,
// where a Postgres app's TEST_DATABASE_URL is, or with --local on this
// machine.
func verify(root string, args []string, _, errOut io.Writer) error {
	fs, local := appFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError{}
	}
	if _, err := os.Stat(filepath.Join(root, "test", "features.yaml")); err != nil {
		return errors.New("no test/features.yaml here: run gantry at the app's root; an app made before gantry verify adds one and its test (docs/development.md#the-feature-map)")
	}
	run := []string{"go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}
	if *local {
		return exitError(runIn(root, run[0], run[1:]...))
	}
	return houston(root, append([]string{"exec"}, run...), errOut)
}
