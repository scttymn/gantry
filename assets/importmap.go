package assets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/a-h/templ"
)

// ModulePin names a module for the page's import map (importmap-rails'
// pin): code imports it by name, and the browser fetches the file.
type ModulePin struct {
	name, file string // "@hotwired/turbo", "@hotwired--turbo.js"
	dir        string // PinAll's folder, instead
	vendor     bool   // PinVendor's: named by file, "--" for "/"
}

// Pin names file, an asset, name in the import map: Pin("application",
// "application.js"), Pin("@hotwired/turbo", "@hotwired--turbo.js").
func Pin(name, file string) ModulePin { return ModulePin{name: name, file: file} }

// PinAll pins every .js asset under dir by its name without ".js"
// (importmap-rails' pin_all_from): js/controllers/hello_controller.js is
// "controllers/hello_controller", and an index.js is its folder's name.
func PinAll(dir string) ModulePin { return ModulePin{dir: strings.TrimSuffix(dir, "/")} }

// PinVendor pins every .js asset under dir by its file's name, "--"
// standing for "/": vendor/@hotwired--turbo.js is "@hotwired/turbo". It's
// where `gantry importmap pin` puts packages, so pinning one is only
// downloading it.
func PinVendor(dir string) ModulePin {
	return ModulePin{dir: strings.TrimSuffix(dir, "/"), vendor: true}
}

// ImportMap is the page's import map (Rails 8's importmap-rails): modules
// by name, each an asset under its fingerprinted name, so a browser loads
// the app's JavaScript with no build step. Declare it once, beside the
// assets:
//
//	var ImportMap = All.ImportMap(
//		gantry.Pin("application", "application.js"),
//		gantry.Pin("@hotwired/turbo", "@hotwired--turbo.js"),
//		gantry.PinAll("controllers"))
//
// A pin of a file that isn't there is a programming error, so it panics at
// start.
type ImportMap struct {
	imports map[string]string // name → URL
	names   []string          // in the order pinned
	json    string
}

// ImportMap is pins as an import map.
func (a *Assets) ImportMap(pins ...ModulePin) *ImportMap {
	m := &ImportMap{imports: map[string]string{}}
	add := func(name, file string) {
		if _, ok := m.imports[name]; !ok {
			m.names = append(m.names, name)
		}
		m.imports[name] = a.Path(file)
	}
	for _, p := range pins {
		if p.dir == "" {
			add(p.name, p.file)
			continue
		}
		var files []string
		for name := range a.byName {
			if strings.HasPrefix(name, p.dir+"/") && strings.HasSuffix(name, ".js") {
				files = append(files, name)
			}
		}
		sort.Strings(files)
		for _, file := range files {
			name := strings.TrimSuffix(file, ".js")
			if p.vendor {
				name = strings.ReplaceAll(strings.TrimPrefix(name, p.dir+"/"), "--", "/")
			} else {
				name = strings.TrimSuffix(name, "/index")
			}
			add(name, file)
		}
	}
	b, _ := json.Marshal(map[string]any{"imports": m.imports})
	// Safe inside <script>: json.Marshal escapes <, > and &.
	m.json = string(b)
	return m
}

// Imports are the pinned names and their URLs.
func (m *ImportMap) Imports() map[string]string { return m.imports }

// Tag is the page's JavaScript, for its head (javascript_importmap_tags):
// the import map, a modulepreload for each module so they're fetched at
// once rather than one import at a time, and entry imported ("application"
// when none is given).
func (m *ImportMap) Tag(entry ...string) templ.Component {
	e := "application"
	if len(entry) > 0 {
		e = entry[0]
	}
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		var b strings.Builder
		b.WriteString(`<script type="importmap">` + m.json + `</script>`)
		for _, name := range m.names {
			b.WriteString(`<link rel="modulepreload" href="` + templ.EscapeString(m.imports[name]) + `">`)
		}
		b.WriteString(`<script type="module">` + m.entry(e) + `</script>`)
		_, err := io.WriteString(w, b.String())
		return err
	})
}

func (m *ImportMap) entry(name string) string {
	quoted, _ := json.Marshal(name)
	return "import " + string(quoted)
}

// Hashes are the two inline scripts' hashes, for a Content-Security-Policy's
// script-src ('sha256-…' 'sha256-…'), for the entry given to Tag.
func (m *ImportMap) Hashes(entry ...string) string {
	e := "application"
	if len(entry) > 0 {
		e = entry[0]
	}
	var out []string
	for _, s := range []string{m.json, m.entry(e)} {
		sum := sha256.Sum256([]byte(s))
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return strings.Join(out, " ")
}
