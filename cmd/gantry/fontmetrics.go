package main

import (
	"fmt"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// A fallback is a system font that stands in for a web font until it
// arrives, with its metrics (capsize's, from the font's hhea and letters):
// the web font's own fallback face resizes it to the web font's measure, so
// text painted in it keeps its place when the web font swaps in.
type fallback struct {
	name, generic                                string
	unitsPerEm, ascent, descent, lineGap, xWidth float64
}

var (
	arial         = fallback{"Arial", "sans-serif", 2048, 1854, -434, 67, 904}
	timesNewRoman = fallback{"Times New Roman", "serif", 2048, 1825, -443, 87, 819}
	courierNew    = fallback{"Courier New", "monospace", 2048, 1705, -615, 0, 1229}
)

// fallbackFor is the system font a family stands in with: Courier New for a
// monospace one, Times New Roman for a serif, Arial for the rest (Google's
// categories, told by their names).
func fallbackFor(family string) fallback {
	f := strings.ToLower(family)
	switch {
	case strings.Contains(f, "mono") || strings.Contains(f, "code"):
		return courierNew
	case strings.Contains(f, "serif") && !strings.Contains(f, "sans"):
		return timesNewRoman
	}
	return arial
}

// letterWeights are how often each letter, and the space, turns up in
// English: a font's average width is its letters' widths so weighted
// (capsize's xWidthAvg), which is what text in it measures.
var letterWeights = map[rune]float64{
	'a': 0.0668, 'b': 0.0122, 'c': 0.0228, 'd': 0.0348, 'e': 0.1039, 'f': 0.0182, 'g': 0.0165,
	'h': 0.0499, 'i': 0.057, 'j': 0.0013, 'k': 0.0063, 'l': 0.0329, 'm': 0.0197, 'n': 0.0552,
	'o': 0.0614, 'p': 0.0158, 'q': 0.0008, 'r': 0.049, 's': 0.0518, 't': 0.0741, 'u': 0.0226,
	'v': 0.008, 'w': 0.0193, 'x': 0.0012, 'y': 0.0162, 'z': 0.0006, ' ': 0.1818,
}

// fontMeasure is a font's metrics in its own units.
type fontMeasure struct {
	unitsPerEm, ascent, descent, lineGap, xWidth float64
}

// measure reads a TrueType or OpenType font's metrics.
func measure(data []byte) (fontMeasure, error) {
	f, err := sfnt.Parse(data)
	if err != nil {
		return fontMeasure{}, err
	}
	upem := f.UnitsPerEm()
	ppem := fixed.Int26_6(upem) << 6 // one pixel a unit
	var buf sfnt.Buffer
	m, err := f.Metrics(&buf, ppem, font.HintingNone)
	if err != nil {
		return fontMeasure{}, err
	}
	units := func(v fixed.Int26_6) float64 { return float64(v) / 64 }
	fm := fontMeasure{unitsPerEm: float64(upem), ascent: units(m.Ascent), descent: units(m.Descent)}
	fm.lineGap = units(m.Height) - fm.ascent - fm.descent
	for r, weight := range letterWeights {
		idx, err := f.GlyphIndex(&buf, r)
		if err != nil || idx == 0 {
			return fontMeasure{}, fmt.Errorf("no glyph for %q", r)
		}
		adv, err := f.GlyphAdvance(&buf, idx, ppem, font.HintingNone)
		if err != nil {
			return fontMeasure{}, err
		}
		fm.xWidth += weight * units(adv)
	}
	return fm, nil
}

// fallbackFace is the @font-face that makes fb stand in for family, sized to
// m (next/font's adjustFontFallback): size-adjust makes its letters as wide
// on average, and the overrides put its lines where the family's go.
func fallbackFace(family string, m fontMeasure, fb fallback) string {
	size := (m.xWidth / m.unitsPerEm) / (fb.xWidth / fb.unitsPerEm)
	pct := func(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }
	return fmt.Sprintf(`/* %s Fallback: %s, sized to %s's measure, so text painted in it
   keeps its place when %s swaps in. */
@font-face {
  font-family: '%s Fallback';
  src: local('%s');
  size-adjust: %s;
  ascent-override: %s;
  descent-override: %s;
  line-gap-override: %s;
}
`, family, fb.name, family, family, family, fb.name, pct(size),
		pct(m.ascent/m.unitsPerEm/size), pct(m.descent/m.unitsPerEm/size), pct(m.lineGap/m.unitsPerEm/size))
}
