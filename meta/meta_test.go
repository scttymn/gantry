package meta

import (
	"context"
	"strings"
	"testing"
)

func render(t *testing.T, s Site, p Page) string {
	t.Helper()
	var b strings.Builder
	if err := Tags(s, p).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func has(t *testing.T, html string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("missing %s in\n%s", w, html)
		}
	}
}

func hasNot(t *testing.T, html string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(html, u) {
			t.Errorf("unexpected %s in\n%s", u, html)
		}
	}
}

var site = Site{Name: "Seven Moons", URL: "https://www.example.com/", Image: "/assets/social-1a2b3c4d.png", ImageAlt: "The Seven Moons mark"}

func TestTagsForAPage(t *testing.T) {
	html := render(t, site, Page{Title: "Terms of Service", Description: "The terms.", Path: "/terms"})
	has(t, html,
		`<meta name="description" content="The terms.">`,
		`<link rel="canonical" href="https://www.example.com/terms">`,
		`<meta property="og:type" content="website">`,
		`<meta property="og:site_name" content="Seven Moons">`,
		`<meta property="og:title" content="Terms of Service">`,
		`<meta property="og:description" content="The terms.">`,
		`<meta property="og:url" content="https://www.example.com/terms">`,
		`<meta property="og:image" content="https://www.example.com/assets/social-1a2b3c4d.png">`,
		`<meta property="og:image:alt" content="The Seven Moons mark">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="twitter:title" content="Terms of Service">`,
		`<meta name="twitter:description" content="The terms.">`,
		`<meta name="twitter:image" content="https://www.example.com/assets/social-1a2b3c4d.png">`,
	)
}

func TestPageOverridesSite(t *testing.T) {
	html := render(t, site, Page{Title: "Bible App", Path: "/bible-app/privacy", Image: "https://cdn.example.net/bible.png", ImageAlt: "Bible App", Type: "article"})
	has(t, html, `<meta property="og:image" content="https://cdn.example.net/bible.png">`, `<meta property="og:image:alt" content="Bible App">`, `<meta property="og:type" content="article">`)
	hasNot(t, html, "social-1a2b3c4d")
}

func TestNoImage(t *testing.T) {
	html := render(t, Site{Name: "Shop", URL: "https://shop.example.com"}, Page{Title: "Shop", Path: "/"})
	has(t, html, `<meta name="twitter:card" content="summary">`, `<link rel="canonical" href="https://shop.example.com/">`)
	hasNot(t, html, "og:image", "twitter:image")
}

func TestNoSiteURL(t *testing.T) {
	html := render(t, Site{Name: "Shop", Image: "/assets/social.png"}, Page{Title: "Shop", Description: "Things.", Path: "/"})
	has(t, html, `<meta property="og:title" content="Shop">`, `<meta name="description" content="Things.">`, `<meta name="twitter:card" content="summary">`)
	hasNot(t, html, "canonical", "og:url", "og:image", "twitter:image")
}

func TestAbsoluteImageWithoutSiteURL(t *testing.T) {
	html := render(t, Site{Name: "Shop"}, Page{Title: "Shop", Image: "https://cdn.example.net/card.png"})
	has(t, html, `<meta property="og:image" content="https://cdn.example.net/card.png">`, `<meta name="twitter:card" content="summary_large_image">`)
}

func TestEscapes(t *testing.T) {
	html := render(t, site, Page{Title: `Tom & "Jerry" <script>`, Description: "a'b", Path: `/x?q="1"&r=<2>`})
	has(t, html, `content="Tom &amp; &#34;Jerry&#34; &lt;script&gt;"`, `content="a&#39;b"`)
	hasNot(t, html, "<script>", `q="1"`)
}

func TestEmptyFieldsAreLeftOut(t *testing.T) {
	html := render(t, Site{}, Page{})
	hasNot(t, html, `content=""`, "og:site_name", "description")
	has(t, html, `<meta property="og:type" content="website">`)
}

func TestTwitterHandle(t *testing.T) {
	has(t, render(t, Site{Name: "S", Twitter: "@sevenmoons"}, Page{Title: "S"}), `<meta name="twitter:site" content="@sevenmoons">`)
}

func TestJoining(t *testing.T) {
	for _, u := range []string{"https://a.example", "https://a.example/"} {
		html := render(t, Site{URL: u, Image: "assets/c.png"}, Page{Path: "terms"})
		has(t, html, `href="https://a.example/terms"`, `content="https://a.example/assets/c.png"`)
	}
}
