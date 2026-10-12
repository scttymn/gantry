package devcheck

import (
	"fmt"
	"strings"
	"testing"
)

func extensionRule() Rule {
	return Rule{ID: "example-feature", Severity: Advice, Purpose: "Reuse a built-in feature", Guidance: "Use the provided helper", Extensions: []string{".example"}, Check: func(s *Source, report func(int, string)) error {
		if offset := strings.Index(s.Text, "manual"); offset >= 0 {
			report(offset, "")
		}
		return nil
	}}
}
func TestExtendedRegistry(t *testing.T) {
	root := t.TempDir()
	put(t, root, "feature.example", "header\nmanual implementation")
	put(t, root, "app/page.html", `<script src="/manual.js"></script>`)
	rules := append(DefaultRules(), extensionRule())
	got, err := ScanWithRules(root, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Rule != "asset-url" || got[1].Rule != "example-feature" || got[1].Line != 2 || got[1].Message != "Use the provided helper" {
		t.Fatal(got)
	}
	put(t, root, ".gantry-check.json", `{"exceptions":[{"rule":"example-feature","file":"feature.example","reason":"Migration example"}]}`)
	got, err = ScanWithRules(root, rules)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if _, err := Scan(root); err == nil {
		t.Fatal("exception for an unregistered rule should fail")
	}
}
func TestInvalidRegistry(t *testing.T) {
	for _, change := range []func(*Rule){
		func(r *Rule) { r.ID = "bad id" }, func(r *Rule) { r.Severity = "maybe" }, func(r *Rule) { r.Purpose = "" }, func(r *Rule) { r.Guidance = "" }, func(r *Rule) { r.Check = nil }, func(r *Rule) { r.Extensions = nil },
	} {
		r := extensionRule()
		change(&r)
		if _, err := ScanWithRules(t.TempDir(), []Rule{r}); err == nil {
			t.Fatal("invalid registry accepted", r.ID)
		}
	}
	r := extensionRule()
	if _, err := ScanWithRules(t.TempDir(), []Rule{r, r}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}
func TestRuleFailureAndInvalidOffset(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.example", "text")
	r := extensionRule()
	r.Check = func(s *Source, report func(int, string)) error { report(100, ""); return nil }
	if _, err := ScanWithRules(root, []Rule{r}); err == nil || !strings.Contains(err.Error(), "example-feature") {
		t.Fatal(err)
	}
	r.Check = func(s *Source, report func(int, string)) error { return fmt.Errorf("failed") }
	if _, err := ScanWithRules(root, []Rule{r}); err == nil || !strings.Contains(err.Error(), "a.example") {
		t.Fatal(err)
	}
}
func TestDefaultRegistryIsIndependent(t *testing.T) {
	rules := DefaultRules()
	rules[0].Severity = Advice
	rules[0].Extensions[0] = ".bad"
	fresh := DefaultRules()
	if fresh[0].Severity != Violation || fresh[0].Extensions[0] != ".html" {
		t.Fatal("mutable defaults leaked across callers")
	}
}
