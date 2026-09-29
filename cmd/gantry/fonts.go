package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// googleFonts is Google Fonts' stylesheet API; tests point it, and
// httpClient, elsewhere.
var googleFonts = "https://fonts.googleapis.com/css2"

// A browser Google serves WOFF2 to, split by unicode-range.
const fontsUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"

// fontStyle is one face of a family: a weight, upright or italic.
type fontStyle struct {
	italic bool
	weight int
}

type fontFamily struct {
	name   string
	styles []fontStyle
}

// parseFontFamily reads "Work Sans:400,600" and "Instrument Serif:400,400i"
// ("i" for italic); a family alone is its regular, 400.
func parseFontFamily(spec string) (fontFamily, error) {
	name, weights, _ := strings.Cut(spec, ":")
	name = strings.TrimSpace(name)
	if name == "" {
		return fontFamily{}, fmt.Errorf("%q: no family", spec)
	}
	if weights == "" {
		weights = "400"
	}
	f := fontFamily{name: name}
	for w := range strings.SplitSeq(weights, ",") {
		w = strings.TrimSpace(w)
		italic := strings.HasSuffix(w, "i")
		n, err := strconv.Atoi(strings.TrimSuffix(w, "i"))
		if err != nil || n < 100 || n > 900 || n%100 != 0 {
			return fontFamily{}, fmt.Errorf("%q: a weight is 100 to 900, in hundreds, with i for italic (400i)", spec)
		}
		s := fontStyle{italic, n}
		if !slices.Contains(f.styles, s) {
			f.styles = append(f.styles, s)
		}
	}
	// Google wants them in order: upright first, each by weight.
	slices.SortFunc(f.styles, func(a, b fontStyle) int {
		if a.italic != b.italic {
			if a.italic {
				return 1
			}
			return -1
		}
		return a.weight - b.weight
	})
	return f, nil
}

// query is the family as Google's API asks: "Work Sans:ital,wght@0,400;0,600".
func (f fontFamily) query() string {
	var tuples []string
	for _, s := range f.styles {
		ital := 0
		if s.italic {
			ital = 1
		}
		tuples = append(tuples, fmt.Sprintf("%d,%d", ital, s.weight))
	}
	return f.name + ":ital,wght@" + strings.Join(tuples, ";")
}

var (
	fontBlock  = regexp.MustCompile(`(?s)/\* ([a-z0-9-]+) \*/\s*(@font-face \{.*?\})`)
	fontURL    = regexp.MustCompile(`url\((https://[^)]+\.woff2)\)`)
	ttfURL     = regexp.MustCompile(`url\((https://[^)]+\.ttf)\)`)
	localFont  = regexp.MustCompile(`url\("([^"]+\.woff2)"\)`)
	fontFamRe  = regexp.MustCompile(`font-family: '([^']+)'`)
	fontStyRe  = regexp.MustCompile(`font-style: (\w+)`)
	fontWghtRe = regexp.MustCompile(`font-weight: (\d+)`)
)

// generateFonts is `gantry g fonts FAMILY[:WEIGHTS]... [--subsets latin,latin-ext]
// [--force]`: Google Fonts served from the app itself, so no stylesheet on
// another host holds up the first paint. It downloads each face's WOFF2
// files into assets/fonts and writes assets/css/fonts.css, and prints the
// preloads and the font stacks. The text paints at once (font-display:
// swap) in a fallback face for each family, a system font sized to the
// family's measure (next/font's adjustFontFallback), so it keeps its place
// when the family swaps in: measured from the family's TTF, which Google
// serves a plain client, and which isn't kept.
func generateFonts(root string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("fonts", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	subsets := fs.String("subsets", "latin,latin-ext", "the character sets to keep, as Google names them")
	force := fs.Bool("force", false, "replace assets/css/fonts.css, and the fonts it used")
	var specs []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		specs, args = append(specs, fs.Arg(0)), fs.Args()[1:]
	}
	if len(specs) == 0 {
		return errors.New(`which fonts? gantry g fonts "Work Sans:400,600" "Instrument Serif:400,400i"`)
	}
	if _, err := os.Stat(filepath.Join(root, "assets")); err != nil {
		return errors.New("no assets/ here: run gantry at the app's root")
	}
	var families []fontFamily
	for _, spec := range specs {
		f, err := parseFontFamily(spec)
		if err != nil {
			return err
		}
		families = append(families, f)
	}
	keep := map[string]bool{}
	for s := range strings.SplitSeq(*subsets, ",") {
		keep[strings.TrimSpace(s)] = true
	}
	cssPath := filepath.Join(root, "assets", "css", "fonts.css")
	old, err := os.ReadFile(cssPath)
	if err == nil && !*force {
		return errors.New("assets/css/fonts.css is there already: --force replaces it, with every family it should have")
	}

	q := url.Values{}
	for _, f := range families {
		q.Add("family", f.query())
	}
	q.Set("display", "swap")
	req, _ := http.NewRequest(http.MethodGet, googleFonts+"?"+q.Encode(), nil)
	req.Header.Set("User-Agent", fontsUserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Google Fonts: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// Google's error is its first line: "Font family not found".
		msg, _, _ := strings.Cut(strings.TrimSpace(string(body)), "\n")
		return fmt.Errorf("Google Fonts: %s: %s", resp.Status, msg)
	}

	// Everything is fetched before anything is written, so a failure leaves
	// the app as it was.
	files := map[string][]byte{} // file name → its bytes
	names := map[string]string{} // Google's URL → file name
	var rules []string
	for _, m := range fontBlock.FindAllStringSubmatch(string(body), -1) {
		subset, rule := m[1], m[2]
		if !keep[subset] {
			continue
		}
		u := fontURL.FindStringSubmatch(rule)
		if u == nil {
			continue
		}
		name, ok := names[u[1]]
		if !ok {
			data, err := fetch(u[1])
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			family := fontFamRe.FindStringSubmatch(rule)[1]
			name = fmt.Sprintf("%s-%s-%s.woff2", strings.ReplaceAll(strings.ToLower(family), " ", "-"), subset, hex.EncodeToString(sum[:])[:6])
			names[u[1]], files[name] = name, data
		}
		label := fontFamRe.FindStringSubmatch(rule)[1]
		if fontStyRe.FindStringSubmatch(rule)[1] == "italic" {
			label += " italic"
		}
		label += " " + fontWghtRe.FindStringSubmatch(rule)[1]
		rules = append(rules, fmt.Sprintf("/* %s, %s */\n%s\n", label, subset, strings.Replace(rule, u[0], `url("`+name+`")`, 1)))
	}
	if len(rules) == 0 {
		return fmt.Errorf("Google Fonts had no %s faces for those", *subsets)
	}
	fallbacks, err := fallbackFaces(families)
	if err != nil {
		return err
	}
	css := fmt.Sprintf(`/* The app's fonts, served from the app itself rather than Google Fonts, so
   no stylesheet on another host holds up the first paint. Written by:
     gantry g fonts %s --force
   font-display: swap: the text paints at once, in each family's fallback
   (the faces at the end: a system font sized to the family's measure), and
   keeps its place when the family arrives (preloaded; see assets.go). Name
   the fallback after the family: font-family: 'Oswald', 'Oswald Fallback'.
   Each face carries its unicode-range, so a browser downloads only the
   files a page uses. Google's fonts are under the SIL Open Font License (or
   Apache 2.0): see fonts.google.com. */

%s
%s`, quoteSpecs(specs, *subsets), strings.Join(rules, "\n"), strings.Join(fallbacks, "\n"))

	fontsDir := filepath.Join(root, "assets", "fonts")
	for name, data := range files {
		if err := write(filepath.Join(fontsDir, name), data); err != nil {
			return err
		}
	}
	if err := write(cssPath, []byte(css)); err != nil {
		return err
	}
	// The fonts the old stylesheet used and the new one doesn't.
	for _, m := range localFont.FindAllStringSubmatch(string(old), -1) {
		if _, ok := files[m[1]]; !ok {
			os.Remove(filepath.Join(fontsDir, m[1]))
		}
	}
	fmt.Fprintf(out, "  wrote assets/css/fonts.css and %d fonts in assets/fonts\n", len(files))
	fmt.Fprintf(out, `
Next:
  1. In assets/assets.go, embed fonts (//go:embed ... fonts ...), put
     "fonts.css" first in All.Styles, and preload the faces on screen when a
     page opens, so its text paints once in its font:
       var Styles = All.Styles("fonts.css", "application.css").Preload(
`)
	for _, f := range families {
		for _, s := range f.styles {
			face := fmt.Sprintf("Family: %q", f.name)
			if s.weight != 400 {
				face += fmt.Sprintf(", Weight: %d", s.weight)
			}
			if s.italic {
				face += ", Italic: true"
			}
			fmt.Fprintf(out, "           gantry.Face{%s},\n", face)
		}
	}
	fmt.Fprintf(out, `       )
     (keep only the ones above the fold: each preload competes with the page)
  2. Use them in the stylesheets, each with its fallback:
`)
	for _, f := range families {
		fmt.Fprintf(out, "       font-family: '%s', '%s Fallback', %s;\n", f.name, f.name, fallbackFor(f.name).generic)
	}
	return nil
}

// fallbackFaces are each family's fallback @font-face, measured from its
// TTF (a plain client's answer from Google), at its first style.
func fallbackFaces(families []fontFamily) ([]string, error) {
	q := url.Values{}
	for _, f := range families {
		first := fontFamily{name: f.name, styles: f.styles[:1]}
		q.Add("family", first.query())
	}
	req, _ := http.NewRequest(http.MethodGet, googleFonts+"?"+q.Encode(), nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Google Fonts: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google Fonts, for the fallbacks' measure: %s %v", resp.Status, err)
	}
	ttf := map[string]string{} // family → its TTF
	for _, block := range regexp.MustCompile(`(?s)@font-face \{.*?\}`).FindAllString(string(body), -1) {
		fam, u := fontFamRe.FindStringSubmatch(block), ttfURL.FindStringSubmatch(block)
		if fam != nil && u != nil && ttf[fam[1]] == "" {
			ttf[fam[1]] = u[1]
		}
	}
	var faces []string
	for _, f := range families {
		u, ok := ttf[f.name]
		if !ok {
			return nil, fmt.Errorf("Google Fonts had no TTF of %s to measure its fallback by", f.name)
		}
		data, err := fetch(u)
		if err != nil {
			return nil, err
		}
		m, err := measure(data)
		if err != nil {
			return nil, fmt.Errorf("measuring %s: %w", f.name, err)
		}
		faces = append(faces, fallbackFace(f.name, m, fallbackFor(f.name)))
	}
	return faces, nil
}

// quoteSpecs is the command's arguments again, for the stylesheet's header.
func quoteSpecs(specs []string, subsets string) string {
	var out []string
	for _, s := range specs {
		out = append(out, strconv.Quote(s))
	}
	if subsets != "latin,latin-ext" {
		out = append(out, "--subsets "+subsets)
	}
	return strings.Join(out, " ")
}
