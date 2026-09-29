package assets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

var scripts = fstest.MapFS{
	"js/application.js":                      {Data: []byte(`import "@hotwired/turbo"`)},
	"js/controllers/hello_controller.js":     {Data: []byte(`export default class {}`)},
	"js/controllers/admin/tab_controller.js": {Data: []byte(`export default class {}`)},
	"js/controllers/index.js":                {Data: []byte(`// the folder`)},
	"js/controllers/notes.txt":               {Data: []byte(`not a module`)},
	"js/other/x.js":                          {Data: []byte(`x`)},
	"vendor/@hotwired--turbo.js":             {Data: []byte(`export const Turbo = {}`)},
	"js/vendor/@hotwired--stimulus.js":       {Data: []byte(`export class Application {}`)},
	"js/vendor/lodash.js":                    {Data: []byte(`export default {}`)},
	"js/vendor/index.js":                     {Data: []byte(`// a package named index`)},
}

func TestImportMap(t *testing.T) {
	a, err := New(scripts)
	if err != nil {
		t.Fatal(err)
	}
	m := a.ImportMap(Pin("application", "application.js"), Pin("@hotwired/turbo", "@hotwired--turbo.js"), PinAll("controllers/"), PinVendor("vendor"))
	want := map[string]string{
		"application":                      a.Path("application.js"),
		"@hotwired/turbo":                  a.Path("@hotwired--turbo.js"),
		"controllers":                      a.Path("controllers/index.js"),
		"controllers/hello_controller":     a.Path("controllers/hello_controller.js"),
		"controllers/admin/tab_controller": a.Path("controllers/admin/tab_controller.js"),
		"@hotwired/stimulus":               a.Path("vendor/@hotwired--stimulus.js"),
		"lodash":                           a.Path("vendor/lodash.js"),
		"index":                            a.Path("vendor/index.js"),
	}
	if got := m.Imports(); len(got) != len(want) {
		t.Errorf("imports: %v", got)
	}
	for name, url := range want {
		if m.Imports()[name] != url || !regexp.MustCompile(`^/assets/.+-[0-9a-f]{8}\.js$`).MatchString(url) {
			t.Errorf("%s: %q, want %q", name, m.Imports()[name], url)
		}
	}

	var b strings.Builder
	m.Tag().Render(context.Background(), &b)
	page := b.String()
	mapJSON := regexp.MustCompile(`^<script type="importmap">(.*?)</script>`).FindStringSubmatch(page)
	if mapJSON == nil {
		t.Fatalf("no import map first: %s", page)
	}
	var parsed struct{ Imports map[string]string }
	if err := json.Unmarshal([]byte(mapJSON[1]), &parsed); err != nil || len(parsed.Imports) != len(want) {
		t.Errorf("the map: %v %v", parsed, err)
	}
	// Preloads in the order pinned, then the entry.
	preloads := regexp.MustCompile(`<link rel="modulepreload" href="([^"]+)">`).FindAllStringSubmatch(page, -1)
	if len(preloads) != 8 || preloads[0][1] != want["application"] || preloads[1][1] != want["@hotwired/turbo"] {
		t.Errorf("preloads: %v", preloads)
	}
	if !strings.HasSuffix(page, `<script type="module">import "application"</script>`) {
		t.Errorf("the entry: %s", page)
	}
	b.Reset()
	m.Tag("admin").Render(context.Background(), &b)
	if !strings.HasSuffix(b.String(), `<script type="module">import "admin"</script>`) {
		t.Errorf("another entry: %s", b.String())
	}

	// The hashes are of the scripts as drawn.
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	if got := m.Hashes(); got != hash(mapJSON[1])+" "+hash(`import "application"`) {
		t.Errorf("hashes: %s", got)
	}
	if got := m.Hashes("admin"); !strings.HasSuffix(got, hash(`import "admin"`)) {
		t.Errorf("another entry's hashes: %s", got)
	}

	// The files are served, as JavaScript.
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", want["application"], nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Body.String() != `import "@hotwired/turbo"` {
		t.Errorf("served: %d %s %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}

// A name that could close the script can't: the map is escaped JSON.
func TestImportMapEscapes(t *testing.T) {
	a, _ := New(scripts)
	m := a.ImportMap(Pin("</script><script>alert(1)</script>", "application.js"))
	var b strings.Builder
	m.Tag().Render(context.Background(), &b)
	if strings.Contains(b.String(), "<script>alert") {
		t.Errorf("%s", b.String())
	}
}

func TestImportMapUnknownFile(t *testing.T) {
	a, _ := New(scripts)
	defer func() {
		if recover() == nil {
			t.Error("a pin of a missing file didn't panic")
		}
	}()
	a.ImportMap(Pin("missing", "missing.js"))
}
