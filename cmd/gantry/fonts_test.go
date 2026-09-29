package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/scttymn/gantry/testkit"
)

var fonts *testkit.FakeHTTP

// Google's stylesheet, as it answers a browser: a face per subset.
const googleCSS = `/* cyrillic */
@font-face {
  font-family: 'Work Sans';
  font-style: normal;
  font-weight: 400;
  font-display: block;
  src: url(https://fonts.gstatic.com/s/worksans/cyr.woff2) format('woff2');
  unicode-range: U+0400-045F;
}
/* latin-ext */
@font-face {
  font-family: 'Work Sans';
  font-style: normal;
  font-weight: 400;
  font-display: block;
  src: url(https://fonts.gstatic.com/s/worksans/ext.woff2) format('woff2');
  unicode-range: U+0100-02BA;
}
/* latin */
@font-face {
  font-family: 'Work Sans';
  font-style: normal;
  font-weight: 400;
  font-display: block;
  src: url(https://fonts.gstatic.com/s/worksans/latin.woff2) format('woff2');
  unicode-range: U+0000-00FF;
}
/* latin */
@font-face {
  font-family: 'Work Sans';
  font-style: normal;
  font-weight: 600;
  font-display: block;
  src: url(https://fonts.gstatic.com/s/worksans/latin.woff2) format('woff2');
  unicode-range: U+0000-00FF;
}
/* latin */
@font-face {
  font-family: 'Instrument Serif';
  font-style: italic;
  font-weight: 400;
  font-display: block;
  src: url(https://fonts.gstatic.com/s/instrumentserif/italic.woff2) format('woff2');
  unicode-range: U+0000-00FF;
}
`

func fakeGoogle(t *testing.T) *[]*http.Request {
	t.Helper()
	f := testkit.HTTP(t)
	fonts = f
	var asked []*http.Request
	f.Handle("GET", "https://fonts.googleapis.com/css2", func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.RawQuery, "Nope") {
			return f.Response(400, "Font family not found\nmore"), nil
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "Chrome") {
			// A plain client gets TTF, one a family: what the fallbacks are
			// measured by.
			var css strings.Builder
			for _, fam := range r.URL.Query()["family"] {
				name, _, _ := strings.Cut(fam, ":")
				css.WriteString("@font-face {\n  font-family: '" + name + "';\n  font-style: normal;\n  font-weight: 400;\n  src: url(https://fonts.gstatic.com/s/" + strings.ReplaceAll(name, " ", "") + ".ttf) format('truetype');\n}\n")
			}
			return f.Response(200, css.String()), nil
		}
		asked = append(asked, r)
		// As Google does: font-display is what was asked for.
		return f.Response(200, strings.ReplaceAll(googleCSS, "font-display: block", "font-display: "+r.URL.Query().Get("display"))), nil
	})
	for _, name := range []string{"WorkSans", "InstrumentSerif"} {
		f.Handle("GET", "https://fonts.gstatic.com/s/"+name+".ttf", func(r *http.Request) (*http.Response, error) {
			return f.Response(200, string(goregular.TTF)), nil
		})
	}
	for _, name := range []string{"cyr", "ext", "latin"} {
		f.On("GET", "https://fonts.gstatic.com/s/worksans/"+name+".woff2", 200, "wOF2 work sans "+name)
	}
	f.On("GET", "https://fonts.gstatic.com/s/instrumentserif/italic.woff2", 200, "wOF2 instrument italic")
	was := httpClient
	httpClient = f.Client()
	t.Cleanup(func() { httpClient = was })
	return &asked
}

func TestGenerateFonts(t *testing.T) {
	asked := fakeGoogle(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "assets", "css"), 0o755)
	var out, errOut strings.Builder
	if code := run([]string{"g", "fonts", "Work Sans:600,400", "Instrument Serif:400i,400"}, root, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	r := (*asked)[0]
	// In Google's order: upright, then italic, each by weight.
	if got := r.URL.Query()["family"]; strings.Join(got, "|") != "Work Sans:ital,wght@0,400;0,600|Instrument Serif:ital,wght@0,400;1,400" {
		t.Errorf("asked for %v", got)
	}
	if r.URL.Query().Get("display") != "swap" || !strings.Contains(r.Header.Get("User-Agent"), "Chrome") {
		t.Errorf("display %q, agent %q", r.URL.Query().Get("display"), r.Header.Get("User-Agent"))
	}
	// A file two faces share is fetched once.
	latin := 0
	for _, r := range fonts.Requests() {
		if strings.HasSuffix(r.URL.Path, "/worksans/latin.woff2") {
			latin++
		}
	}
	if latin != 1 {
		t.Errorf("the shared file fetched %d times", latin)
	}
	css, _ := os.ReadFile(filepath.Join(root, "assets", "css", "fonts.css"))
	fontFiles, _ := os.ReadDir(filepath.Join(root, "assets", "fonts"))
	var names []string
	for _, f := range fontFiles {
		names = append(names, f.Name())
	}
	// latin and latin-ext kept, cyrillic not; one file for the two weights
	// Google serves from it.
	if len(names) != 3 || !strings.HasPrefix(names[0], "instrument-serif-latin-") || !strings.HasPrefix(names[1], "work-sans-latin-") || !strings.HasPrefix(names[2], "work-sans-latin-ext-") {
		t.Errorf("fonts: %v", names)
	}
	s := string(css)
	if strings.Contains(s, "gstatic") || strings.Contains(s, "cyr") || strings.Count(s, "@font-face") != 4+2 || strings.Count(s, `url("`+names[1]+`")`) != 2 {
		t.Errorf("fonts.css:\n%s", s)
	}
	if !strings.Contains(s, "font-display: swap") || strings.Contains(s, "font-display: block") || !strings.Contains(s, `gantry g fonts "Work Sans:600,400" "Instrument Serif:400i,400" --force`) || !strings.Contains(s, "/* Work Sans 600, latin */") ||
		!strings.Contains(s, "font-family: 'Work Sans Fallback';\n  src: local('Arial');") || !strings.Contains(s, "font-family: 'Instrument Serif Fallback';\n  src: local('Times New Roman');") {
		t.Errorf("fonts.css:\n%s", s)
	}
	for _, want := range []string{`gantry.Face{Family: "Work Sans"},`, `gantry.Face{Family: "Work Sans", Weight: 600},`, `gantry.Face{Family: "Instrument Serif", Italic: true},`,
		`font-family: 'Work Sans', 'Work Sans Fallback', sans-serif;`, `font-family: 'Instrument Serif', 'Instrument Serif Fallback', serif;`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %s in\n%s", want, out.String())
		}
	}

	// Again: refused without --force; with it, the stylesheet replaced and
	// the fonts it no longer uses removed (latin-ext, here; the fake Google
	// answers with every family whatever it's asked for).
	errOut.Reset()
	if code := run([]string{"g", "fonts", "Work Sans"}, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--force") {
		t.Errorf("%d %s", code, errOut.String())
	}
	os.WriteFile(filepath.Join(root, "assets", "fonts", "mine.woff2"), []byte("x"), 0o644) // not the stylesheet's
	if code := run([]string{"g", "fonts", "Work Sans", "--subsets", "latin", "--force"}, root, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	fontFiles, _ = os.ReadDir(filepath.Join(root, "assets", "fonts"))
	names = names[:0]
	for _, f := range fontFiles {
		names = append(names, f.Name())
	}
	if len(names) != 3 || names[1] != "mine.woff2" || !strings.HasPrefix(names[2], "work-sans-latin-") || strings.Contains(names[2], "ext") {
		t.Errorf("after --force: %v", names)
	}
	css, _ = os.ReadFile(filepath.Join(root, "assets", "css", "fonts.css"))
	if !strings.Contains(string(css), `gantry g fonts "Work Sans" --subsets latin --force`) {
		t.Errorf("header: %s", css[:300])
	}
}

func TestGenerateFontsRules(t *testing.T) {
	fakeGoogle(t)
	root := t.TempDir()
	var out, errOut strings.Builder
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"g", "fonts"}, "which fonts?"},
		{[]string{"g", "fonts", "Work Sans"}, "no assets/ here"},
	} {
		errOut.Reset()
		if code := run(c.args, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), c.want) {
			t.Errorf("%v: %d %s", c.args, code, errOut.String())
		}
	}
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"g", "fonts", "Work Sans:450"}, "a weight is 100 to 900"},
		{[]string{"g", "fonts", "Work Sans:bold"}, "a weight is 100 to 900"},
		{[]string{"g", "fonts", ":400"}, "no family"},
		{[]string{"g", "fonts", "Nope Sans"}, "Font family not found"},
		{[]string{"g", "fonts", "Work Sans", "--subsets", "greek"}, "no greek faces"},
	} {
		errOut.Reset()
		if code := run(c.args, root, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), c.want) {
			t.Errorf("%v: %d %s", c.args, code, errOut.String())
		}
		if _, err := os.Stat(filepath.Join(root, "assets", "css", "fonts.css")); err == nil {
			t.Fatalf("%v wrote fonts.css", c.args)
		}
	}
}

// A fallback is sized to its family's measure: Go Regular's, against Arial,
// as next/font's adjustFontFallback computes it (checked against next/font
// by hand: Inter 107.28% here, 107.12% there, a newer Inter).
func TestFallbackFace(t *testing.T) {
	m, err := measure(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	if m.unitsPerEm != 2048 || m.ascent != 1935 || m.descent != 432 || m.lineGap != 0 || m.xWidth < 905 || m.xWidth > 915 {
		t.Errorf("Go Regular: %+v", m)
	}
	face := fallbackFace("Go", m, arial)
	for _, want := range []string{"font-family: 'Go Fallback';", "src: local('Arial');", "size-adjust: ", "ascent-override: ", "descent-override: ", "line-gap-override: "} {
		if !strings.Contains(face, want) {
			t.Errorf("no %q in\n%s", want, face)
		}
	}
	// The overrides are the family's, in the fallback's resized units.
	size := (m.xWidth / 2048) / (904.0 / 2048)
	if want := fmt.Sprintf("size-adjust: %.2f%%;\n  ascent-override: %.2f%%;", size*100, 1935/2048.0/size*100); !strings.Contains(face, want) {
		t.Errorf("want %q in\n%s", want, face)
	}
	if _, err := measure([]byte("not a font")); err == nil {
		t.Error("measured a non-font")
	}
}

func TestFallbackFor(t *testing.T) {
	for family, want := range map[string]string{
		"Work Sans": "Arial", "Oswald": "Arial", "PT Sans Serif": "Arial", "Instrument Serif": "Times New Roman",
		"IBM Plex Mono": "Courier New", "Source Code Pro": "Courier New", "Yellowtail": "Arial",
	} {
		if got := fallbackFor(family).name; got != want {
			t.Errorf("%s: %s, want %s", family, got, want)
		}
	}
}
