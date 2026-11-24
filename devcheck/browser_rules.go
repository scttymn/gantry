package devcheck

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	broadForm          = regexp.MustCompile(`document\s*\.\s*querySelector\s*\(\s*["']form["']\s*\)`)
	fingerprint        = regexp.MustCompile(`[-.][a-fA-F0-9]{8,}\.`)
	jsTimerCall        = regexp.MustCompile(`(?:^|[^.\w$])((?:(?:window|globalThis|self)\s*\.\s*)?(?:setTimeout|setInterval|requestAnimationFrame|requestIdleCallback)\s*\()`)
	rawDOMHTML         = regexp.MustCompile(`(\.\s*(?:innerHTML|outerHTML)\s*(?:\+=|=[^=])|\.\s*insertAdjacentHTML\s*\(|(?:^|[^.\w$])document\s*\.\s*write(?:ln)?\s*\()`)
	stimulusController = regexp.MustCompile(`\bextends\s+(?:[\w$]+\.)*Controller\b`)
	connectMethod      = regexp.MustCompile(`(?:^|[^.\w$])connect\s*\([^)]*\)\s*\{`)
	disconnectMethod   = regexp.MustCompile(`(?:^|[^.\w$])disconnect\s*\([^)]*\)\s*\{`)
	globalListener     = regexp.MustCompile(`(?:^|[^.\w$])((?:document|window)\s*\.\s*addEventListener\s*\()`)
	listenerCleanup    = regexp.MustCompile(`(?:\bremoveEventListener|\.\s*abort)\s*\(`)
)

func checkForms(s *Source, report func(int, string)) error {
	for _, match := range broadForm.FindAllStringIndex(s.Text, -1) {
		report(match[0], "")
	}
	return nil
}

func checkJSTimers(s *Source, report func(int, string)) error {
	code := maskJSNonCode(s.Text)
	for _, match := range jsTimerCall.FindAllStringSubmatchIndex(code, -1) {
		report(match[2], "")
	}
	return nil
}

func checkRawDOMHTML(s *Source, report func(int, string)) error {
	code := maskJSNonCode(s.Text)
	for _, match := range rawDOMHTML.FindAllStringSubmatchIndex(code, -1) {
		at := match[2]
		if at < len(code) && code[at] != '.' && code[at] != 'd' {
			at++
		}
		report(at, "")
	}
	return nil
}

func checkStimulusListeners(s *Source, report func(int, string)) error {
	code := maskJSNonCode(s.Text)
	if !stimulusController.MatchString(code) {
		return nil
	}
	connectBody, connectStart, ok := methodBody(code, connectMethod)
	if !ok {
		return nil
	}
	adds := globalListener.FindAllStringSubmatchIndex(connectBody, -1)
	if len(adds) == 0 {
		return nil
	}
	if disconnectBody, _, ok := methodBody(code, disconnectMethod); ok && listenerCleanup.MatchString(disconnectBody) {
		return nil
	}
	for _, match := range adds {
		report(connectStart+match[2], "")
	}
	return nil
}

func methodBody(code string, header *regexp.Regexp) (string, int, bool) {
	m := header.FindStringIndex(code)
	if m == nil {
		return "", 0, false
	}
	open := m[1] - 1
	depth := 0
	for i := open; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return code[open+1 : i], open + 1, true
			}
		}
	}
	return "", 0, false
}

func maskJSNonCode(src string) string {
	out := []byte(src)
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	var templateDepth []int
	braceDepth := 0
	regexAllowed := true
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c == '/' && i+1 < len(out) && out[i+1] == '/' {
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(out) && out[i+1] == '*' {
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i < len(out) {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i++
					break
				}
				blank(i)
				i++
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote := c
			out[i] = ' '
			i++
			for i < len(out) {
				if out[i] == '\\' && i+1 < len(out) {
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				done := out[i] == quote
				blank(i)
				if done {
					break
				}
				i++
			}
			regexAllowed = false
			continue
		}
		if c == '`' {
			out[i] = ' '
			i = maskTemplateChunk(out, i+1, &templateDepth, &braceDepth)
			regexAllowed = false
			continue
		}
		if c == '/' && regexAllowed {
			out[i] = ' '
			i++
			inClass := false
			for i < len(out) {
				if out[i] == '\\' && i+1 < len(out) {
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				if out[i] == '[' {
					inClass = true
				} else if out[i] == ']' {
					inClass = false
				} else if out[i] == '/' && !inClass {
					out[i] = ' '
					break
				} else if out[i] == '\n' {
					break
				}
				blank(i)
				i++
			}
			regexAllowed = false
			continue
		}
		if c == '{' {
			braceDepth++
			regexAllowed = true
			continue
		}
		if c == '}' {
			if n := len(templateDepth); n > 0 && templateDepth[n-1] == braceDepth {
				templateDepth = templateDepth[:n-1]
				braceDepth--
				i = maskTemplateChunk(out, i+1, &templateDepth, &braceDepth)
				regexAllowed = false
				continue
			}
			braceDepth--
			regexAllowed = true
			continue
		}
		switch c {
		case ' ', '\t', '\r', '\n':
		case '(', '[', ',', ';', ':', '=', '!', '&', '|', '?', '+', '-', '*', '%', '^', '~', '<', '>':
			regexAllowed = true
		default:
			regexAllowed = false
		}
	}
	return string(out)
}

func maskTemplateChunk(out []byte, i int, templateDepth *[]int, braceDepth *int) int {
	for i < len(out) {
		if out[i] == '\\' && i+1 < len(out) {
			if out[i] != '\n' {
				out[i] = ' '
			}
			if out[i+1] != '\n' {
				out[i+1] = ' '
			}
			i += 2
			continue
		}
		if out[i] == '$' && i+1 < len(out) && out[i+1] == '{' {
			*braceDepth++
			*templateDepth = append(*templateDepth, *braceDepth)
			return i + 1
		}
		done := out[i] == '`'
		if out[i] != '\n' {
			out[i] = ' '
		}
		if done {
			return i
		}
		i++
	}
	return len(out) - 1
}

func checkLayout(s *Source, report func(int, string)) error {
	if strings.HasPrefix(s.Path, "app/") && !strings.Contains(s.Path, "/shared/") && strings.Contains(strings.ToLower(s.Text), "<html") {
		report(0, "")
	}
	return nil
}
func checkAssetURLs(s *Source, report func(int, string)) error {
	z := html.NewTokenizer(strings.NewReader(s.Text))
	offset := 0
	for {
		kind := z.Next()
		start := offset
		offset += len(z.Raw())
		if kind == html.ErrorToken {
			return nil
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		attrs := map[string]string{}
		for _, a := range token.Attr {
			attrs[a.Key] = a.Val
		}
		target := ""
		if token.Data == "script" {
			target = attrs["src"]
		}
		if token.Data == "link" && strings.EqualFold(attrs["rel"], "stylesheet") {
			target = attrs["href"]
		}
		if target == "" || strings.ContainsAny(target, "{}") {
			continue
		}
		u, err := url.Parse(target)
		if err != nil || u.IsAbs() || u.Host != "" || u.Path == "" || fingerprint.MatchString(u.Path) {
			continue
		}
		report(start, "")
	}
}
