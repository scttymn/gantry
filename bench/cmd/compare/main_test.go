package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func files(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A side is its whole report (its limit, its layers) and its numbered runs,
// and nothing else that happens to be lying there; and its runs were all at
// the rate the other side's were.
func TestSide(t *testing.T) {
	const run = `{"rate": 18000, "cpu_us_per_request": 40, "due": 100000}`
	dir := files(t, map[string]string{
		"base.json":   `{"limit_rate": 20000, "cpu_us_by_layer": {"the network": 9}}`,
		"base.1.json": run, "base.2.json": run, "base.10.json": run,
		"base.1.backup.json": `{"rate": 18000, "cpu_us_per_request": 400, "due": 100000}`,
		"base.x.json":        `{"rate": 18000, "cpu_us_per_request": 400, "due": 100000}`,
	})
	var rate float64
	s, err := side(dir, "base", "main", &rate)
	if err != nil || len(s.Runs) != 3 || s.LimitRate != 20000 || s.Layers["the network"] != 9 || rate != 18000 {
		t.Fatalf("%+v, at %.0f, %v; want the three numbered runs at 18,000", s, rate, err)
	}
	for _, r := range s.Runs {
		if r.CPUMicros != 40 {
			t.Errorf("a run of %.0f µs: a file that isn't a run was read as one", r.CPUMicros)
		}
	}

	for name, body := range map[string]string{
		"at another rate": `{"rate": 20000, "cpu_us_per_request": 40, "due": 100000}`,
		"at no rate":      `{"cpu_us_per_request": 40, "due": 100000}`,
		"not JSON":        `nothing`,
	} {
		dir := files(t, map[string]string{"head.json": `{"limit_rate": 20000}`, "head.1.json": run, "head.2.json": body})
		rate := 18000.0
		if s, err := side(dir, "head", "this change", &rate); err == nil {
			t.Errorf("a run %s: %+v, and no error", name, s)
		}
	}
	// The first side's runs at no rate don't let the second's set it.
	dir = files(t, map[string]string{"base.json": `{}`, "base.1.json": `{"cpu_us_per_request": 40, "due": 100000}`})
	rate = 0
	if _, err := side(dir, "base", "main", &rate); err == nil || !strings.Contains(err.Error(), "rate") {
		t.Errorf("runs at no rate: %v", err)
	}
	if _, err := side(t.TempDir(), "base", "main", &rate); err == nil {
		t.Error("no report at all, and no error")
	}
}
