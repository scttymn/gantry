package main

import (
	"go/build/constraint"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bench's port (the profiler, the process's CPU time) is in a build
// with the bench tag and no other: bench.go says so in its first line, and
// nothing else in the command reaches for the profiler. An app deployed
// from this folder has no such port, whatever BENCH_ADDR is.
func TestBenchPortNeedsTheTag(t *testing.T) {
	src, err := os.ReadFile("bench.go")
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(src), "\n")
	expr, err := constraint.Parse(first)
	if err != nil {
		t.Fatalf("bench.go starts %q, not with a build constraint: %v", first, err)
	}
	if expr.Eval(func(tag string) bool { return tag == "linux" }) || !expr.Eval(func(tag string) bool { return tag == "linux" || tag == "bench" }) {
		t.Errorf("bench.go builds under %q: it must need the bench tag", first)
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if f == "bench.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		if src, _ := os.ReadFile(f); strings.Contains(string(src), "pprof") || strings.Contains(string(src), "BENCH_ADDR") {
			t.Errorf("%s reaches for the bench's port, and is in every build", f)
		}
	}
}
