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

// jspm answers as it did for Turbo and Stimulus (2026-09-29).
func fakeJSPM(t *testing.T, turbo int) *[]map[string]any {
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
	was, wasAPI := httpClient, jspmAPI
	httpClient, jspmAPI = f.Client(), "https://api.jspm.io/generate"
	t.Cleanup(func() { httpClient, jspmAPI = was, wasAPI })
	return &asked
}

func TestImportmapPin(t *testing.T) {
	asked := fakeJSPM(t, 200)
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
		"@hotwired--turbo.js":    "// @hotwired/turbo@8.0.23 downloaded from https://ga.jspm.io/npm:@hotwired/turbo@8.0.23/dist/turbo.es2017-esm.js\n\nexport const Turbo = {}\n",
		"@hotwired--stimulus.js": "// @hotwired/stimulus@3.2.2 downloaded from https://ga.jspm.io/npm:@hotwired/stimulus@3.2.2/dist/stimulus.js\n\nexport class Application {}\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, "assets", "js", "vendor", file))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", file, got, err)
		}
	}
	if !strings.Contains(out.String(), "pinned @hotwired/turbo (assets/js/vendor/@hotwired--turbo.js)") {
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

func TestImportmapFailures(t *testing.T) {
	fakeJSPM(t, 404)
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
	// A file that won't download (the second) leaves nothing half-written.
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
