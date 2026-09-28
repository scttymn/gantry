package assets

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Face is a font face to fetch before the page asks for it.
type Face struct {
	Family string // as the stylesheets name it: "IBM Plex Mono"
	Weight int    // 400 when zero
	Italic bool
}

func (f Face) String() string {
	s := fmt.Sprintf("%s %d", f.Family, f.weight())
	if f.Italic {
		s += " italic"
	}
	return s
}

func (f Face) weight() int {
	if f.Weight == 0 {
		return 400
	}
	return f.Weight
}

// Preload has Tag fetch these faces first: the ones on screen when the page
// opens, so their text never paints in a fallback and swaps. (A face is
// otherwise fetched only once the browser has laid out text in it.) Each is
// found among the bundle's @font-face rules: the one for Latin text (its
// unicode-range takes in "a"), in WOFF2; one drawn into the stylesheet as
// a data: URL needs no fetch, and gets no preload. A face the stylesheets
// don't have is a programming error, so it panics at start:
//
//	var Styles = All.Styles("fonts.css", "site.css").Preload(
//		assets.Face{Family: "Oswald", Weight: 600},
//		assets.Face{Family: "IBM Plex Mono"},
//	)
func (s *Styles) Preload(faces ...Face) *Styles {
	rules := fontFaces(string(s.body))
	for _, face := range faces {
		src := ""
		for _, r := range rules {
			if r.matches(face) {
				src = r.src
				break
			}
		}
		if src == "" {
			panic(fmt.Sprintf("assets: no WOFF2 @font-face for Latin text in %s", face))
		}
		if !strings.HasPrefix(src, "data:") {
			s.preloads = append(s.preloads, src)
		}
	}
	return s
}

// Preloads are the URLs of the faces Tag fetches first.
func (s *Styles) Preloads() []string { return s.preloads }

var fontFaceRule = regexp.MustCompile(`(?is)@font-face\s*\{(.*?)\}`)

type fontFace struct {
	family       string // lower case, unquoted
	weightFrom   int
	weightTo     int
	italic       bool
	src          string // its WOFF2 file; "" when it has none
	unicodeRange string // "" when it covers everything
}

func (r fontFace) matches(f Face) bool {
	w := f.weight()
	return r.src != "" && r.family == strings.ToLower(f.Family) && r.italic == f.Italic &&
		r.weightFrom <= w && w <= r.weightTo && covers(r.unicodeRange, 'a')
}

// fontFaces reads a stylesheet's @font-face rules, minified or not.
func fontFaces(css string) []fontFace {
	var out []fontFace
	for _, m := range fontFaceRule.FindAllStringSubmatch(css, -1) {
		r := fontFace{weightFrom: 400, weightTo: 400}
		for _, decl := range declarations(m[1]) {
			name, value, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "font-family":
				r.family = strings.ToLower(strings.Trim(value, `"' `))
			case "font-style":
				r.italic = strings.HasPrefix(strings.ToLower(value), "italic") || strings.HasPrefix(strings.ToLower(value), "oblique")
			case "font-weight":
				r.weightFrom, r.weightTo = weights(value)
			case "src":
				r.src = woff2(value)
			case "unicode-range":
				r.unicodeRange = value
			}
		}
		out = append(out, r)
	}
	return out
}

// declarations splits a rule's body at its semicolons, but not those
// inside quotes or brackets (a data: URL's).
func declarations(body string) []string {
	var out []string
	depth, quote, start := 0, byte(0), 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ';' && depth == 0:
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	return append(out, body[start:])
}

// weights reads a font-weight: one ("600", "bold") or a variable font's
// range ("100 900").
func weights(value string) (from, to int) {
	named := map[string]int{"normal": 400, "bold": 700}
	var ws []int
	for _, f := range strings.Fields(value) {
		if w, ok := named[strings.ToLower(f)]; ok {
			ws = append(ws, w)
		} else if w, err := strconv.Atoi(f); err == nil {
			ws = append(ws, w)
		}
	}
	switch len(ws) {
	case 0:
		return 400, 400
	case 1:
		return ws[0], ws[0]
	}
	return ws[0], ws[1]
}

var srcURL = regexp.MustCompile(`url\(\s*["']?([^"')]+?)["']?\s*\)\s*(?:format\(\s*["']?([^"')]+)["']?\s*\))?`)

// woff2 is the WOFF2 file among a src's: said so by its format(), or by its
// name when it has none.
func woff2(src string) string {
	for _, m := range srcURL.FindAllStringSubmatch(src, -1) {
		if strings.EqualFold(m[2], "woff2") || (m[2] == "" && strings.HasSuffix(strings.ToLower(m[1]), ".woff2")) {
			return m[1]
		}
	}
	return ""
}

// covers reports whether a unicode-range ("U+0000-00FF, U+0131", or
// minified, "U+??,U+131") takes in r. An empty range takes in everything.
func covers(unicodeRange string, r rune) bool {
	if strings.TrimSpace(unicodeRange) == "" {
		return true
	}
	for part := range strings.SplitSeq(unicodeRange, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		hex, ok := strings.CutPrefix(part, "U+")
		if !ok {
			continue
		}
		from, to := hex, hex
		if a, b, isRange := strings.Cut(hex, "-"); isRange {
			from, to = a, b
		} else if strings.Contains(hex, "?") {
			from, to = strings.ReplaceAll(hex, "?", "0"), strings.ReplaceAll(hex, "?", "F")
		}
		lo, err1 := strconv.ParseUint(from, 16, 32)
		hi, err2 := strconv.ParseUint(to, 16, 32)
		if err1 == nil && err2 == nil && uint64(r) >= lo && uint64(r) <= hi {
			return true
		}
	}
	return false
}
