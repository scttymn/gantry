package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"
)

var binstubReleaseBase = []byte("\ngantry_base=${GANTRY_RELEASE_BASE:-https://github.com/scttymn/gantry/releases/download}\n")

func binstub() ([]byte, error) {
	raw, err := fs.ReadFile(templates, "templates/new/bin/gantry.tmpl")
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

func refreshBinstub(root string, errOut io.Writer) {
	path := filepath.Join(root, "bin", "gantry")
	have, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(have, []byte("#!/bin/sh\n")) || !bytes.Contains(have, binstubReleaseBase) {
		return
	}
	want, err := binstub()
	if err != nil || bytes.Equal(have, want) {
		return
	}
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
