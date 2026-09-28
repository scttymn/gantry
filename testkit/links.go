package testkit

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Page is a page for Links to check.
type Page struct {
	Path string
	// Header goes with every request: a session cookie, for a page behind
	// sign-in. A Host in it is the requests' host.
	Header http.Header
	// Skip are paths not to request: links that change something when
	// followed (signing out, say).
	Skip []string
}

// Links fetches a page from h and checks that everything it refers to on
// its own site loads, so a page can't ship a broken image, stylesheet,
// script, font or link: each src and srcset (img, source, script, iframe),
// each <link> href (stylesheets, preloads, icons, the manifest), each url()
// in its styles, and each <a> href. Files must answer 200; a link may
// redirect too (to sign in, say). URLs on other sites, fragments, and
// data:, mailto: and tel: URLs are left alone.
func Links(t testing.TB, h http.Handler, p Page) {
	t.Helper()
	links(t, h, p)
}

// Crawl checks a page's Links, then every page it links to under within
// ("/admin/", say), and theirs, once each: a whole section, however many
// pages it grows to.
func Crawl(t testing.TB, h http.Handler, p Page, within string) {
	t.Helper()
	seen := map[string]bool{p.Path: true}
	queue := []string{p.Path}
	for len(queue) > 0 {
		page := p
		page.Path, queue = queue[0], queue[1:]
		for _, next := range links(t, h, page) {
			if strings.HasPrefix(next, within) && !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
}

// links checks a page's references, and returns the pages it links to that
// answered with HTML.
func links(t testing.TB, h http.Handler, p Page) (pages []string) {
	t.Helper()
	rec := request(h, p.Path, p.Header)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d", p.Path, rec.Code)
	}
	base, err := url.Parse(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := references(rec.Body.String())
	if err != nil {
		t.Fatalf("%s: %v", p.Path, err)
	}
	checked := map[string]bool{}
	for _, ref := range refs {
		u, err := url.Parse(strings.TrimSpace(ref.url))
		if err != nil {
			t.Errorf("%s: %s %q doesn't parse: %v", p.Path, ref.from, ref.url, err)
			continue
		}
		if u.Scheme != "" || u.Host != "" || (u.Path == "" && u.RawQuery == "") {
			continue // another site, data:, mailto:, or a fragment
		}
		u = base.ResolveReference(u)
		u.Fragment = ""
		target := u.RequestURI()
		if checked[target] || slices.Contains(p.Skip, u.Path) {
			continue
		}
		checked[target] = true
		got := request(h, target, p.Header)
		ok := got.Code == http.StatusOK || (ref.link && got.Code >= 300 && got.Code < 400)
		if !ok {
			t.Errorf("%s: %s %s: %d", p.Path, ref.from, target, got.Code)
		}
		if ref.link && got.Code == http.StatusOK && strings.HasPrefix(got.Header().Get("Content-Type"), "text/html") {
			pages = append(pages, target)
		}
	}
	return pages
}

func request(h http.Handler, target string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	for k, vs := range header {
		req.Header[k] = vs
	}
	if host := header.Get("Host"); host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type reference struct {
	url  string
	from string // where the page has it: "img srcset", say
	link bool   // an <a>, which may redirect
}

var cssURL = regexp.MustCompile(`url\(\s*["']?([^"')]+?)["']?\s*\)`)

// references are the URLs a page refers to, in order.
func references(page string) ([]reference, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	var out []reference
	add := func(from, u string, link bool) {
		if u != "" {
			out = append(out, reference{url: u, from: from, link: link})
		}
	}
	styles := func(from, css string) {
		for _, m := range cssURL.FindAllStringSubmatch(css, -1) {
			add(from, m[1], false)
		}
	}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		attr := func(name string) string {
			for _, a := range n.Attr {
				if a.Key == name {
					return a.Val
				}
			}
			return ""
		}
		tag := n.Data
		switch tag {
		case "img", "source", "script", "iframe", "video", "audio":
			add(tag+" src", attr("src"), false)
			for _, u := range srcsetURLs(attr("srcset")) {
				add(tag+" srcset", u, false)
			}
		case "link":
			add("link "+attr("rel"), attr("href"), false)
		case "a":
			add("a", attr("href"), true)
		case "style":
			if n.FirstChild != nil {
				styles("style", n.FirstChild.Data)
			}
		}
		if s := attr("style"); s != "" {
			styles(tag+" style", s)
		}
	}
	return out, nil
}

// srcsetURLs are a srcset's URLs, read as the HTML standard does: a URL
// runs to the next whitespace (so a data: URL keeps its comma), and a
// candidate's descriptors run to the next comma.
func srcsetURLs(srcset string) []string {
	var out []string
	s := srcset
	for {
		s = strings.TrimLeft(s, " \t\n\r\f,")
		if s == "" {
			return out
		}
		end := strings.IndexAny(s, " \t\n\r\f")
		if end < 0 {
			end = len(s)
		}
		u := s[:end]
		s = s[end:]
		if trimmed := strings.TrimRight(u, ","); trimmed != u {
			out = append(out, trimmed) // no descriptors
			continue
		}
		out = append(out, u)
		if comma := strings.IndexByte(s, ','); comma >= 0 {
			s = s[comma+1:]
		} else {
			s = ""
		}
	}
}
