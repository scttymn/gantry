package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/testkit"
)

func fakeJSPM(t *testing.T, turbo int, cdn map[string]string) *[]map[string]any {
	t.Helper()
	f := testkit.HTTP(t)
	var asked []map[string]any
	f.Handle("POST", "https://api.jspm.io/generate", func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		asked = append(asked, body)
		if body["install"].([]any)[0] == "no-such-package" {
			return f.Response(401, `{"error":"Unable to resolve npm:no-such-package@ to a valid version imported from https://ga.jspm.io/"}`), nil
		}
		return f.Response(200, `{"staticDeps":[],"map":{"imports":{
			"@hotwired/stimulus":"https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/dist/stimulus.js",
			"@hotwired/turbo":"https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/dist/turbo.es2017-esm.js"}}}`), nil
	})
	f.On("GET", "https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/dist/stimulus.js", 200, "export class Application {}\n//# sourceMappingURL=stimulus.js.map\n")
	f.On("GET", "https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/dist/turbo.es2017-esm.js", turbo, "export const Turbo = {}\n")
	for _, pkg := range []string{"https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/", "https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/"} {
		for _, file := range append([]string{"package.json"}, licenseFiles...) {
			if body, ok := cdn[pkg+file]; ok {
				f.On("GET", pkg+file, 200, body)
			} else {
				f.On("GET", pkg+file, 404, "Not Found")
			}
		}
	}
	was, wasAPI := httpClient, jspmAPI
	httpClient, jspmAPI = f.Client(), "https://api.jspm.io/generate"
	t.Cleanup(func() { httpClient, jspmAPI = was, wasAPI })
	return &asked
}

const (
	turboLicense    = "Copyright (c) 37signals\n\nPermission is hereby granted, free of charge, to any person obtaining\na copy of this software.\n"
	stimulusLicense = "MIT License\n\nCopyright © 2021 Basecamp, LLC.\n\nPermission is hereby granted, free of charge, to any person obtaining a copy of this software.\n"
)

func TestImportmapPin(t *testing.T) {
	asked := fakeJSPM(t, 200, map[string]string{
		"https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/package.json":   `{"name":"@hotwired/turbo","version":"8.0.23","homepage":"https://turbo.hotwired.dev","license":"MIT"}`,
		"https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/LICENSE":        turboLicense,
		"https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/package.json": `{"name":"@hotwired/stimulus","version":"3.2.2","license":"MIT"}`,
		"https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/LICENSE.md":   stimulusLicense,
	})
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)
	var out, errOut strings.Builder
	if code := run([]string{"importmap", "pin", "@hotwired/turbo", "@hotwired/stimulus@3"}, root, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	if len(*asked) != 1 {
		t.Fatalf("asked jspm %d times", len(*asked))
	}
	body, _ := json.Marshal((*asked)[0])
	if string(body) != `{"env":["browser","module","production"],"flattenScope":true,"install":["@hotwired/turbo","@hotwired/stimulus@3"]}` {
		t.Errorf("asked %s", body)
	}
	for file, want := range map[string]string{
		"@hotwired--turbo.js": "// @hotwired/turbo@8.0.23 downloaded from https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/dist/turbo.es2017-esm.js\n\n" +
			"/*!\n@hotwired/turbo 8.0.23\nhttps://turbo.hotwired.dev\n\n" + turboLicense + "*/\n\nexport const Turbo = {}\n",
		"@hotwired--stimulus.js": "// @hotwired/stimulus@3.2.2 downloaded from https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/dist/stimulus.js\n\n" +
			"/*!\n@hotwired/stimulus 3.2.2\n\n" + stimulusLicense + "*/\n\nexport class Application {}\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, "assets", "js", "vendor", file))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", file, got, err)
		}
	}
	if !strings.Contains(out.String(), "pinned @hotwired/turbo (assets/js/vendor/@hotwired--turbo.js)") || strings.Contains(out.String(), "license") {
		t.Errorf("said %s", out.String())
	}

	out.Reset()
	if code := run([]string{"importmap", "unpin", "@hotwired/turbo@8"}, root, &out, &errOut); code != 0 {
		t.Fatalf("unpin: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "js", "vendor", "@hotwired--turbo.js")); err == nil {
		t.Error("still there")
	}
	errOut.Reset()
	if code := run([]string{"importmap", "unpin", "left-pad"}, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "left-pad isn't pinned") {
		t.Errorf("unpinning what isn't: %d %s", code, errOut.String())
	}
}

func TestImportmapPinNoLicense(t *testing.T) {
	fakeJSPM(t, 200, map[string]string{
		"https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/package.json": `{"name":"@hotwired/turbo","version":"8.0.23","license":"MIT"}`,
	})
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)
	var out, errOut strings.Builder
	if code := run([]string{"importmap", "pin", "@hotwired/turbo"}, root, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	got, _ := os.ReadFile(filepath.Join(root, "assets", "js", "vendor", "@hotwired--turbo.js"))
	if strings.Contains(string(got), "/*!") {
		t.Errorf("a license comment with no license: %q", got)
	}
	for _, want := range []string{
		"no license file found for @hotwired/turbo (package.json says MIT): add its notice to assets/js/vendor/@hotwired--turbo.js by hand",
		"no license file found for @hotwired/stimulus: add its notice to assets/js/vendor/@hotwired--stimulus.js by hand",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("didn't say %q: %s", want, out.String())
		}
	}
}

func TestImportmapRepinKeepsNotice(t *testing.T) {
	fakeJSPM(t, 200, map[string]string{
		"https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/package.json": `{"name":"@hotwired/turbo","version":"8.0.23","license":"MIT"}`,
	})
	shipped, err := os.ReadFile(filepath.Join("templates", "new", "assets", "js", "vendor", "@hotwired--turbo.js"))
	if err != nil {
		t.Fatal(err)
	}
	_, notice, _ := strings.Cut(string(shipped), "\n\n")
	notice, _, _ = strings.Cut(notice, "*/\n\n")
	if !strings.HasPrefix(notice, "/*!\nTurbo 8.0.23\n") {
		t.Fatalf("gantry new's Turbo has no notice: %q", notice)
	}
	root := t.TempDir()
	path := filepath.Join(root, "assets", "js", "vendor", "@hotwired--turbo.js")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(strings.ReplaceAll(string(shipped), "8.0.23", "8.0.20")), 0o644)
	var out, errOut strings.Builder
	if code := run([]string{"importmap", "pin", "@hotwired/turbo"}, root, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	got, _ := os.ReadFile(path)
	if want := "// @hotwired/turbo@8.0.23 downloaded from https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/dist/turbo.es2017-esm.js\n\n" +
		notice + "*/\n\nexport const Turbo = {}\n"; string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if want := "kept @hotwired/turbo's license notice from the file it replaced (no license file found)"; !strings.Contains(out.String(), want) {
		t.Errorf("didn't say %q: %s", want, out.String())
	}
	if strings.Contains(out.String(), "no license file found for @hotwired/turbo") {
		t.Errorf("warned of a notice it kept: %s", out.String())
	}
}

func TestImportmapFailures(t *testing.T) {
	fakeJSPM(t, 404, nil)
	root := t.TempDir()
	var out, errOut strings.Builder
	if code := run([]string{"importmap", "pin", "x"}, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "no assets/ here") {
		t.Errorf("outside an app: %d %s", code, errOut.String())
	}
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)
	errOut.Reset()
	if code := run([]string{"importmap", "pin", "no-such-package"}, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "Unable to resolve npm:no-such-package") {
		t.Errorf("an unknown package: %d %s", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"importmap", "pin", "@hotwired/turbo"}, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "404") {
		t.Errorf("a failed download: %d %s", code, errOut.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "assets", "js", "vendor")); len(entries) != 0 {
		t.Errorf("written: %v", entries)
	}
	errOut.Reset()
	if code := run([]string{"importmap", "pin"}, root, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "importmap pin|unpin") {
		t.Errorf("no package: %d %s", code, errOut.String())
	}
}
