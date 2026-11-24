package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

func verify(root string, args []string, _, errOut io.Writer) error {
	fs, local := appFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError{}
	}
	if _, err := os.Stat(filepath.Join(root, "test", "features.yaml")); err != nil {
		return errors.New("no test/features.yaml here: run gantry at the app's root; an app made before gantry verify writes one with bin/gantry g features, and adds its test (docs/development.md#an-app-made-before-the-feature-map)")
	}
	run := []string{"go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}
	if *local {
		return exitError(runIn(root, run[0], run[1:]...))
	}
	return houston(root, append([]string{"exec"}, run...), errOut)
}
