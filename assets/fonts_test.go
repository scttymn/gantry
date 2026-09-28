package assets

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// As Google Fonts writes them: a latin-ext and a latin face per weight.
const googleFonts = `
@font-face {
  font-family: 'IBM Plex Mono';
  font-style: normal;
  font-weight: 400;
  font-display: swap;
  src: url("plex-400-ext.woff2") format('woff2');
  unicode-range: U+0100-02BA, U+02BD-02C5, U+1E00-1E9F;
}
@font-face {
  font-family: 'IBM Plex Mono';
  font-style: normal;
  font-weight: 400;
  font-display: swap;
  src: url("plex-400.woff2") format('woff2');
  unicode-range: U+0000-00FF, U+0131, U+0152-0153;
}
@font-face {
  font-family: 'IBM Plex Mono';
  font-style: normal;
  font-weight: 500;
  src: url("plex-500.woff2") format('woff2');
  unicode-range: U+0000-00FF;
}
@font-face {
  font-family: 'IBM Plex Mono';
  font-style: italic;
  font-weight: 400;
  src: url("plex-400-italic.woff2") format('woff2');
  unicode-range: U+0000-00FF;
}
@font-face {
  font-family: Barlow;
  font-weight: 100 900;
  src: url("barlow.woff") format('woff'), url("barlow-variable.woff2") format("woff2");
}
@font-face {
  font-family: "Inline";
  src: url("data:font/woff2;base64,d09GMgAB") format("woff2");
}
`

var fontFiles = fstest.MapFS{
	"css/fonts.css":               {Data: []byte(googleFonts)},
	"css/site.css":                {Data: []byte("body { font-family: 'IBM Plex Mono'; }")},
	"fonts/plex-400-ext.woff2":    {Data: []byte("a")},
	"fonts/plex-400.woff2":        {Data: []byte("b")},
	"fonts/plex-500.woff2":        {Data: []byte("c")},
	"fonts/plex-400-italic.woff2": {Data: []byte("d")},
	"fonts/barlow.woff":           {Data: []byte("e")},
	"fonts/barlow-variable.woff2": {Data: []byte("f")},
}

func TestPreload(t *testing.T) {
	a := MustNew(fontFiles)

	t.Run("a face is found by family, weight and style: its Latin WOFF2 file, digested", func(t *testing.T) {
		for face, want := range map[Face]string{
			{Family: "IBM Plex Mono"}:               a.Path("plex-400.woff2"),
			{Family: "ibm plex mono", Weight: 400}:  a.Path("plex-400.woff2"),
			{Family: "IBM Plex Mono", Weight: 500}:  a.Path("plex-500.woff2"),
			{Family: "IBM Plex Mono", Italic: true}: a.Path("plex-400-italic.woff2"),
			{Family: "Barlow", Weight: 600}:         a.Path("barlow-variable.woff2"), // in a variable font's range
		} {
			got := a.Styles("fonts.css").Preload(face).Preloads()
			if len(got) != 1 || got[0] != want {
				t.Errorf("%s: %v, want %s", face, got, want)
			}
		}
	})

	t.Run("a face drawn into the stylesheet needs no fetch", func(t *testing.T) {
		if got := a.Styles("fonts.css").Preload(Face{Family: "Inline"}).Preloads(); len(got) != 0 {
			t.Error(got)
		}
	})

	t.Run("a face the stylesheets don't have stops the app at start", func(t *testing.T) {
		for _, face := range []Face{
			{Family: "IBM Plex Mono", Weight: 700},
			{Family: "IBM Plex Mono", Weight: 500, Italic: true},
			{Family: "Oswald"},
		} {
			func() {
				defer func() {
					if r := recover(); r == nil || !strings.Contains(r.(string), face.String()) {
						t.Errorf("%s: %v", face, r)
					}
				}()
				a.Styles("fonts.css", "site.css").Preload(face)
			}()
		}
	})

	t.Run("Tag fetches the faces before the stylesheets", func(t *testing.T) {
		s := a.Styles("fonts.css", "site.css").Preload(Face{Family: "IBM Plex Mono"}, Face{Family: "IBM Plex Mono", Weight: 500})
		var b strings.Builder
		if err := s.Tag().Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		want := `<link rel="preload" href="` + a.Path("plex-400.woff2") + `" as="font" type="font/woff2" crossorigin="anonymous">` +
			`<link rel="preload" href="` + a.Path("plex-500.woff2") + `" as="font" type="font/woff2" crossorigin="anonymous"><style>`
		if !strings.HasPrefix(b.String(), want) {
			t.Errorf("%.400s", b.String())
		}
	})

	t.Run("unicode ranges, written out or minified", func(t *testing.T) {
		for r, want := range map[string]bool{
			"":                   true,
			"U+0000-00FF":        true,
			"U+??":               true,
			"U+0-7F, U+100":      true,
			"U+0100-02BA, U+131": false,
			"U+4??":              false,
			"u+0061":             true,
		} {
			if got := covers(r, 'a'); got != want {
				t.Errorf("%q: %v", r, got)
			}
		}
	})
}
