package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var featuresTest = []string{"go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}

const featuresTestCommand = "go test -count=1 -v -run '^TestFeatures$' ./app/"

const verifyCopy = ".gantry-verify-*.yml"

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
	if !hasTestFeatures(filepath.Join(root, "app")) {
		return errors.New("app/ has no TestFeatures, so go test would run nothing and pass: copy a new app's app/features_test.go (docs/development.md#an-app-made-before-the-feature-map)")
	}
	if *local {
		return exitError(runIn(root, "env", append([]string{"GANTRY_ENV=test"}, featuresTest...)...))
	}
	compose, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		return fmt.Errorf("no compose.yml here, for Houston's test stack: %w (or run it on this machine, with --local)", err)
	}
	copied, err := featuresCompose(compose)
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

func hasTestFeatures(dir string) bool {
	tests, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	for _, path := range tests {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "TestFeatures" {
				return true
			}
		}
	}
	return false
}

func featuresCompose(compose []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, fmt.Errorf("compose.yml: %w", err)
	}
	test := &doc
	if len(doc.Content) == 1 {
		test = doc.Content[0]
	}
	for _, key := range []string{"x-houston", "commands", "test"} {
		if test = value(test, key); test == nil {
			return nil, errors.New("compose.yml has no x-houston.commands.test, which Houston's test stack runs: bin/gantry verify runs TestFeatures there in its place (or run it on this machine, with --local)")
		}
	}
	*test = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: featuresTestCommand}
	return yaml.Marshal(&doc)
}

func value(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; mapping.Kind == yaml.MappingNode && i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
