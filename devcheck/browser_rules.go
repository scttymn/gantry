package devcheck

import (
	"golang.org/x/net/html"
	"net/url"
	"regexp"
	"strings"
)

var broadForm = regexp.MustCompile(`document\s*\.\s*querySelector\s*\(\s*["']form["']\s*\)`)
var fingerprint = regexp.MustCompile(`[-.][a-fA-F0-9]{8,}\.`)

func checkForms(s *Source, report func(int, string)) error {
	for _, match := range broadForm.FindAllStringIndex(s.Text, -1) {
		report(match[0], "")
	}
	return nil
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
