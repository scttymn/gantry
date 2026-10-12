package devcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
func TestDefaultsAndGuards(t *testing.T) {
	root := t.TempDir()
	put(t, root, "app/page.html", "<html><body>Custom page</body></html>")
	put(t, root, "config/config.go", `package config;func env(){_ = or("GANTRY_ENV","development")}`)
	put(t, root, "cmd/shop/main.go", `package main
 import w "github.com/scttymn/gantry/web"
 import "net/http"
 func main(){
 if cfg.Development(){w.Development(h,".",log)}
 if !cfg.Development(){w.Timing(h,log)}
 if cfg.Development(){}else{w.Development(h,".",log)}
 if cfg.Development() || other {w.Timing(h,log)}
 http.FileServer(files)
 }`)
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	rules, _ := validateRules(DefaultRules())
	for _, w := range got {
		counts[w.Rule]++
		if w.Severity != rules[w.Rule].Severity {
			t.Fatal(w)
		}
	}
	if counts["development-guard"] != 3 || counts["environment-default"] != 1 || counts["shared-layout"] != 1 || counts["raw-assets"] != 1 {
		t.Fatal(got)
	}
}
func TestDocumentedExceptions(t *testing.T) {
	root := t.TempDir()
	put(t, root, "app/page.html", `<script src="/custom.js"></script>`)
	put(t, root, ".gantry-check.json", `{"exceptions":[{"rule":"asset-url","file":"app/page.html","reason":"Custom handler revalidates this development integration."}]}`)
	got, err := Scan(root)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{
		`{"exceptions":[{"rule":"asset-url","file":"app/page.html","reason":""}]}`,
		`{"exceptions":[{"rule":"typo","file":"app/page.html","reason":"intentional"}]}`,
		`{"exceptions":[{"rule":"asset-url","file":"*","reason":"all files"}]}`,
		`{"exceptions":[]} {}`,
	} {
		put(t, root, ".gantry-check.json", bad)
		if _, err := Scan(root); err == nil || !strings.Contains(err.Error(), ".gantry-check.json") {
			t.Fatal("accepted invalid config", bad, err)
		}
	}
}
