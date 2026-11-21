package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
)

// An app tracks one version, gantry's (go.mod). bin/gantry runs the Houston
// that gantry release names (gantry houston-release), and gantry keeps
// bin/gantry itself current: bumping gantry, as Dependabot does, is the
// whole upgrade.

// binstubSignature is how bin/gantry starts when gantry wrote it.
var binstubSignature = []byte("#!/bin/sh\n# Downloads this app's gantry")

// binstub is bin/gantry as this gantry writes it.
func binstub() ([]byte, error) {
	raw, err := templates.ReadFile("templates/new/bin/gantry.tmpl")
	if err != nil {
		return nil, err
	}
	tpl, err := template.New("bin/gantry").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, NewApp{Houston: houstonRelease}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// refreshBinstub rewrites root's bin/gantry when gantry wrote it and it's
// not this gantry's, and says so: the change is the app's to commit. A
// bin/gantry gantry didn't write, or none, is left alone. It never fails
// the command it runs before.
func refreshBinstub(root string, errOut io.Writer) {
	path := filepath.Join(root, "bin", "gantry")
	have, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(have, binstubSignature) {
		return
	}
	want, err := binstub()
	if err != nil || bytes.Equal(have, want) {
		return
	}
	// A new file renamed over the old: the shell running the old one has
	// already exec'd gantry, and never reads a half-written script.
	tmp := path + ".new"
	if err := os.WriteFile(tmp, want, 0o755); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return
	}
	fmt.Fprintf(errOut, "gantry: updated bin/gantry to gantry's current one (Houston %s); commit it with the gantry bump\n", houstonRelease)
}
