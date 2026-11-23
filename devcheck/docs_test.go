package devcheck

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/development.md's rule table from DefaultRules")

func TestDevelopmentGuideRuleTable(t *testing.T) {
	const (
		path  = "../docs/development.md"
		begin = "<!-- rules: generated from devcheck.DefaultRules() by go test ./devcheck -update -->\n"
		end   = "<!-- /rules -->"
	)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	before, rest, ok := strings.Cut(doc, begin)
	if !ok {
		t.Fatalf("docs/development.md: no %q line to put the rule table after", strings.TrimSpace(begin))
	}
	_, after, ok := strings.Cut(rest, end)
	if !ok {
		t.Fatalf("docs/development.md: no %q after the rule table", end)
	}
	want := before + begin + "\n" + ruleTable(DefaultRules()) + "\n" + end + after
	if *update {
		if err := os.WriteFile(path, []byte(want), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if doc != want {
		t.Fatal("docs/development.md: the Strict rule table is stale; run go test ./devcheck -update")
	}
}

func ruleTable(rules []Rule) string {
	cell := strings.NewReplacer("|", `\|`).Replace
	severity := map[string]string{Violation: "Violation", Advice: "Advice"}
	var b strings.Builder
	b.WriteString("| Rule | Severity | Purpose | Guidance |\n| --- | --- | --- | --- |\n")
	for _, r := range rules {
		b.WriteString("| `" + r.ID + "` | " + severity[r.Severity] + " | " + cell(r.Purpose) + " | " + cell(r.Guidance) + " |\n")
	}
	return b.String()
}
