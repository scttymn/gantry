package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"--help"}, t.TempDir(), &out, &errOut); code != 0 {
		t.Fatalf("--help: exit %d", code)
	}
	help := out.String()
	for _, want := range []string{
		"gantry new NAME [--db sqlite|postgres] [--gantry PATH] [--in-module] [--module PATH] [--skip-houston]",
		"gantry g resource [NAMESPACE/]TABLE FIELD:TYPE[:required]...",
		"gantry g auth [--email-code] [--signup] [--social]",
		"gantry check [--generated] [--json] [--rules] [--strict]",
		"gantry routes [--diagram] [--json] [--local]",
		"gantry verify [--artifacts DIR] [--browser] [--feature NAME[,NAME...]] [--json] [--local]",
		"gantry test [ARGS...] [--base REF] [--changed] [--local]",
		"gantry g features [--local]",
		"gantry secret ",
		"gantry dev|",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("no %q in\n%s", want, help)
		}
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"new"}, []string{"usage: gantry new NAME", "[--in-module]"}},
		{[]string{"new", "shop", "--in-modules"}, []string{"-in-modules", "usage: gantry new NAME", "[--in-module]"}},
		{[]string{"check", "--bogus"}, []string{"usage: gantry check [--generated] [--json] [--rules] [--strict]"}},
		{[]string{"check", "--changed"}, []string{"-changed", "usage: gantry check [--generated] [--json] [--rules] [--strict]"}},
		{[]string{"check", "--strict", "app/routes.go"}, []string{"usage: gantry check [--generated] [--json] [--rules] [--strict]"}},
		{[]string{"routes", "posts"}, []string{"usage: gantry routes [--diagram] [--json] [--local]"}},
		{[]string{"routes", "--diagram", "--json"}, []string{"usage: gantry routes [--diagram] [--json] [--local]"}},
		{[]string{"verify", "extra"}, []string{"usage: gantry verify [--artifacts DIR] [--browser] [--feature NAME[,NAME...]] [--json] [--local]"}},
		{[]string{"verify", "--artifacts", "out"}, []string{"usage: gantry verify [--artifacts DIR] [--browser] [--feature NAME[,NAME...]] [--json] [--local]"}},
		{[]string{"verify", "--changed"}, []string{"-changed", "usage: gantry verify"}},
		{[]string{"verify", "--feature", ""}, []string{"--feature names no feature", "usage: gantry verify"}},
		{[]string{"verify", "--feature", ","}, []string{"--feature names no feature", "usage: gantry verify"}},
		{[]string{"test", "--base", "main"}, []string{"usage: gantry test [ARGS...]"}},
		{[]string{"test", "--changed", "./app/..."}, []string{"usage: gantry test [ARGS...]"}},
		{[]string{"test", "--changed", "-count=1"}, []string{"-count", "usage: gantry test [ARGS...]"}},
		{[]string{"test", "--local", "--base"}, []string{"-base", "usage: gantry test [ARGS...]"}},
		{[]string{"test", "--base", ""}, []string{"usage: gantry test [ARGS...]"}},
		{[]string{"test", "--changed", "--base", " "}, []string{"--base names no ref", "usage: gantry test [ARGS...]"}},
		{[]string{"test", "--local", "./app/posts"}, []string{"usage: gantry test [ARGS...]"}},
		{[]string{"g", "features", "posts"}, []string{"usage: gantry g features [--local]"}},
		{[]string{"check", "--rules", "--strict"}, []string{"usage: gantry check"}},
		{[]string{"check", "--generated", "--strict"}, []string{"usage: gantry check"}},
		{[]string{"check", "--generated", "--json"}, []string{"usage: gantry check"}},
		{[]string{"g", "auth", "--email-codes"}, []string{"-email-codes", "usage: gantry g auth [--email-code] [--signup] [--social]"}},
		{[]string{"g", "error-pages", "--forced"}, []string{"usage: gantry g error-pages [--force]"}},
		{[]string{"g", "api-tokens", "--prefixes", "x_"}, []string{"usage: gantry g api-tokens [--prefix PREFIX_]"}},
		{[]string{"importmap", "pin"}, []string{"usage: gantry importmap pin|unpin PACKAGE[@VERSION]..."}},
		{[]string{"secret", "32"}, []string{"takes no arguments", "usage: gantry secret"}},
	} {
		var out, errOut strings.Builder
		code := run(tc.args, t.TempDir(), &out, &errOut)
		if code != 2 {
			t.Errorf("%q: exit %d, want 2 (%s)", tc.args, code, errOut.String())
		}
		for _, want := range tc.want {
			if !strings.Contains(errOut.String(), want) {
				t.Errorf("%q: no %q in\n%s", tc.args, want, errOut.String())
			}
		}
	}
	var o, e strings.Builder
	if code := run([]string{"g", "nothing"}, t.TempDir(), &o, &e); code != 2 || !strings.Contains(e.String(), "gantry g resource") {
		t.Errorf("g nothing: exit %d\n%s", code, e.String())
	}
}

func TestCommandHelp(t *testing.T) {
	for _, c := range commands {
		if c.flags == nil && c.name != "new" {
			continue
		}
		for _, help := range []string{"--help", "-h"} {
			args := append(strings.Fields(c.name), help)
			if c.name == "g fonts" {
				args = append(strings.Fields(c.name), "Work Sans", help)
			}
			var out, errOut strings.Builder
			want := "usage: " + c.synopsis() + "\n"
			if c.help != "" {
				want += c.help + "\n"
			}
			code := run(args, t.TempDir(), &out, &errOut)
			if code != 0 || out.String() != want || errOut.String() != "" {
				t.Errorf("%q: exit %d\nstdout: %s\nstderr: %s", args, code, out.String(), errOut.String())
			}
		}
	}
}

var docsWithCommands = []string{"README.md", "docs/*.md", "llms.txt", "cmd/gantry/templates/new/README.md.tmpl"}

var (
	codeSpan   = regexp.MustCompile("`([^`]+)`")
	invocation = regexp.MustCompile(`(?:^|[\s(;&|$])(?:bin/)?gantry ([^;&|#]*)`)
	flagName   = regexp.MustCompile(`--[a-z][a-z0-9-]*`)
)

func TestDocsCommands(t *testing.T) {
	var files []string
	for _, pattern := range docsWithCommands {
		matches, _ := filepath.Glob(filepath.Join("..", "..", pattern))
		files = append(files, matches...)
	}
	checked := 0
	for _, file := range files {
		for _, inv := range invocations(t, file) {
			checked++
			if msg := checkInvocation(inv.text); msg != "" {
				t.Errorf("%s:%d: gantry %s: %s", strings.TrimPrefix(file, "../../"), inv.line, inv.text, msg)
			}
		}
	}
	if checked < 40 {
		t.Errorf("checked %d invocations: the docs moved, or the extraction broke", checked)
	}
}

func TestDocsCodeSpansCloseOnTheirLine(t *testing.T) {
	for _, pattern := range docsWithCommands {
		files, _ := filepath.Glob(filepath.Join("..", "..", pattern))
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			fenced := false
			for n, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "```") {
					fenced = !fenced
					continue
				}
				if !fenced && strings.Count(line, "`")%2 == 1 {
					t.Errorf("%s:%d: a code span runs onto the next line, where TestDocsCommands can't read it: %s", strings.TrimPrefix(file, "../../"), n+1, line)
				}
			}
		}
	}
}

type docInvocation struct {
	line int
	text string
}

func invocations(t *testing.T, file string) []docInvocation {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var found []docInvocation
	fenced := false
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		line := s.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		var code []string
		if fenced {
			code = []string{line}
		} else {
			for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
				code = append(code, m[1])
			}
		}
		for _, c := range code {
			for _, m := range invocation.FindAllStringSubmatch(c, -1) {
				if text := strings.TrimSpace(m[1]); text != "" {
					found = append(found, docInvocation{n, text})
				}
			}
		}
	}
	return found
}

func checkInvocation(text string) string {
	words := strings.Fields(text)
	if helpArgs[words[0]] {
		return ""
	}
	c, rest, ok := lookup(words)
	if !ok {
		return "unknown command (see the table in cmd/gantry/main.go; a Houston command goes there with houston: true)"
	}
	if c.houston || c.flags == nil && len(flagName.FindAllString(strings.Join(rest, " "), -1)) == 0 {
		return ""
	}
	known := map[string]bool{}
	if c.flags != nil {
		c.flags().VisitAll(func(f *flag.Flag) { known["--"+f.Name] = true })
	}
	for _, name := range flagName.FindAllString(strings.Join(rest, " "), -1) {
		if !known[name] {
			return fmt.Sprintf("unknown flag %s%s", name, didYouMean(name, known))
		}
	}
	return ""
}

func didYouMean(name string, known map[string]bool) string {
	best, bestDistance := "", 3
	for k := range known {
		if d := distance(name, k); d < bestDistance {
			best, bestDistance = k, d
		}
	}
	if best == "" {
		return ""
	}
	return " (did you mean " + best + "?)"
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func TestCheckInvocation(t *testing.T) {
	for text, want := range map[string]string{
		"g auth --email-codes":             "unknown flag --email-codes (did you mean --email-code?)",
		"g resource posts title:string":    "",
		"g migration create_posts --force": "unknown flag --force",
		"new shop --in-module":             "",
		"deploy --server --follow":         "",
		"frobnicate":                       "unknown command",
		"--help":                           "",
	} {
		if got := checkInvocation(text); !strings.HasPrefix(got, want) || (want == "" && got != "") {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}
