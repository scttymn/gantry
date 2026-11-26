package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/scttymn/gantry/testkit"
)

var featuresTest = []string{"go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}

const (
	featuresTestCommand     = "go test -count=1 -v -run '^TestFeatures$' ./app/"
	featuresJSONTestCommand = "GANTRY_VERIFY_JSON=1 " + featuresTestCommand
	defaultVerifyArtifacts  = ".gantry/verify-artifacts"
)

const verifyCopy = ".gantry-verify-*.yml"

func verifyFlags() (fs *flag.FlagSet, local, asJSON, browser *bool, artifacts, feature *string) {
	fs, local = appFlags()
	asJSON = fs.Bool("json", false, "a JSON report of features, visits, cache and leak checks")
	browser = fs.Bool("browser", false, "also render each feature in headless Chrome via CDP and capture screenshots")
	artifacts = fs.String("artifacts", "", "output `DIR` for --browser screenshots and manifest.json (default "+defaultVerifyArtifacts+")")
	feature = fs.String("feature", "", "only these features from test/features.yaml, `NAME[,NAME...]`")
	return fs, local, asJSON, browser, artifacts, feature
}

func verify(root string, args []string, out, errOut io.Writer) error {
	fs, local, asJSON, browser, artifacts, feature := verifyFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *artifacts != "" && !*browser {
		return usageError{}
	}
	var names []string
	for _, name := range strings.Split(*feature, ",") {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	featureGiven := false
	fs.Visit(func(f *flag.Flag) { featureGiven = featureGiven || f.Name == "feature" })
	if featureGiven && len(names) == 0 {
		return usageError{errors.New("--feature names no feature")}
	}
	selectedFeatures := strings.Join(names, ",")
	if _, err := os.Stat(filepath.Join(root, "test", "features.yaml")); err != nil {
		return errors.New("no test/features.yaml here: run gantry at the app's root; an app made before gantry verify writes one with bin/gantry g features, and adds its test (docs/development.md#an-app-made-before-the-feature-map)")
	}
	if !hasTestFeatures(filepath.Join(root, "app")) {
		return errors.New("app/ has no TestFeatures, so go test would run nothing and pass: copy a new app's app/features_test.go (docs/development.md#an-app-made-before-the-feature-map)")
	}
	artifactsDir := *artifacts
	if *browser && artifactsDir == "" {
		artifactsDir = defaultVerifyArtifacts
	}
	if *local {
		envVars := []string{"GANTRY_ENV=test"}
		if *asJSON {
			envVars = append(envVars, "GANTRY_VERIFY_JSON=1")
		}
		if selectedFeatures != "" {
			envVars = append(envVars, "GANTRY_VERIFY_FEATURES="+selectedFeatures)
		}
		if *browser {
			absArtifacts := artifactsDir
			if !filepath.IsAbs(absArtifacts) {
				if abs, err := filepath.Abs(filepath.Join(root, artifactsDir)); err == nil {
					absArtifacts = abs
				}
			}
			envVars = append(envVars, "GANTRY_VERIFY_BROWSER=1", "GANTRY_VERIFY_ARTIFACTS="+absArtifacts)
		}
		cmdArgs := append(envVars, featuresTest...)
		if *asJSON {
			return runVerifyJSON(root, "env", cmdArgs, out, errOut)
		}
		return exitError(runIn(root, "env", cmdArgs...))
	}
	compose, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		return fmt.Errorf("no compose.yml here, for Houston's test stack: %w (or run it on this machine, with --local)", err)
	}
	command := verifyTestCommand(*asJSON, *browser, artifactsDir, selectedFeatures)
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
	houstonArgs := []string{"-f", filepath.Base(f.Name()), "test"}
	if *asJSON {
		if err := needHouston(errOut); err != nil {
			return err
		}
		return runVerifyJSON(root, "houston", houstonArgs, out, errOut)
	}
	return houston(root, houstonArgs, errOut)
}

func verifyTestCommand(asJSON, browser bool, artifactsDir, features string) string {
	var envPrefix []string
	if asJSON {
		envPrefix = append(envPrefix, "GANTRY_VERIFY_JSON=1")
	}
	if features != "" {
		envPrefix = append(envPrefix, "GANTRY_VERIFY_FEATURES="+shellQuote(features))
	}
	if browser {
		envPrefix = append(envPrefix, "GANTRY_VERIFY_BROWSER=1", "GANTRY_VERIFY_ARTIFACTS="+shellQuote(artifactsDir))
		if cdpURL := strings.TrimSpace(os.Getenv("GANTRY_CDP_URL")); cdpURL != "" {
			envPrefix = append(envPrefix, "GANTRY_CDP_URL="+shellQuote(cdpURL))
		}
	}
	if len(envPrefix) == 0 {
		return featuresTestCommand
	}
	return strings.Join(envPrefix, " ") + " " + featuresTestCommand
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runVerifyJSON(root, name string, args []string, out, errOut io.Writer) error {
	raw, err := runQuietlyIn(root, name, args...)
	payload, ok := extractVerifyJSON(raw)
	if !ok {
		if len(raw) > 0 {
			fmt.Fprint(errOut, string(raw))
		}
		if err != nil {
			return exitError(err)
		}
		return errors.New("verify --json got no report from TestFeatures: update the app's gantry dependency so testkit.Verify writes it")
	}
	var report testkit.VerifyReport
	if decErr := json.Unmarshal(payload, &report); decErr != nil {
		return fmt.Errorf("verify --json: invalid report: %w", decErr)
	}
	if encErr := json.NewEncoder(out).Encode(report); encErr != nil {
		return encErr
	}
	if err != nil {
		return exitError(err)
	}
	if !report.Passed {
		return errExit(1)
	}
	return nil
}

func extractVerifyJSON(raw []byte) ([]byte, bool) {
	prefix := []byte(testkit.VerifyJSONPrefix)
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if _, after, ok := bytes.Cut(line, prefix); ok {
			trimmed := bytes.TrimSpace(after)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				return trimmed, true
			}
		}
	}
	return nil, false
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

func featuresCompose(compose []byte, command string) ([]byte, error) {
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
	*test = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(command)}
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
