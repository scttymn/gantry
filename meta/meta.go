// Package meta is a page's description for search engines and for the
// previews a link gets when it's shared (Slack, iMessage, X, LinkedIn,
// Facebook): a description, a canonical link, and Open Graph and Twitter
// card tags. The layout draws it in <head>, with the site's defaults and
// the page's own:
//
//	var Site = meta.Site{Name: "Seven Moons", URL: "https://www.example.com",
//		Image: assets.Path("social.png")}
//
//	@meta.Tags(Site, meta.Page{Title: p.Title, Description: p.Description, Path: p.Path})
//
// Previews need absolute addresses, so the page's address and its image
// are made from Site.URL. Without one (in development), the tags that need
// an absolute address are left out rather than written wrong.
package meta

import (
	"context"
	"html"
	"io"
	"strings"

	"github.com/a-h/templ"
)

// Site is what every page shares.
type Site struct {
	// Name is the site's, beside the page's title in a preview.
	Name string
	// URL is the site's canonical address: "https://www.example.com". A
	// page's address and a path image are made from it.
	URL string
	// Image is the preview image when a page has none: an absolute URL, or
	// a path on URL ("/assets/social-1a2b3c4d.png", from assets.Path).
	// 1200×630 shows well everywhere.
	Image    string
	ImageAlt string
	// Twitter is the site's handle on X, "@name", if it has one.
	Twitter string
}

// Page is what a page says about itself.
type Page struct {
	Title       string
	Description string
	// Path is the page's address on the site: "/terms". Its canonical
	// link and og:url are Site.URL and Path.
	Path string
	// Image and ImageAlt replace the site's for this page.
	Image    string
	ImageAlt string
	// Type is the Open Graph type: "website" when empty, or "article".
	Type string
}

// Tags is the page's meta tags, for the layout's <head>.
func Tags(s Site, p Page) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		var b strings.Builder
		meta := func(attr, key, value string) {
			if value != "" {
				b.WriteString(`<meta ` + attr + `="` + key + `" content="` + html.EscapeString(value) + `">` + "\n")
			}
		}
		page := absolute(s.URL, p.Path)
		if p.Path == "" {
			page = ""
		}
		image, alt := p.Image, p.ImageAlt
		if image == "" {
			image, alt = s.Image, s.ImageAlt
		}
		image = absolute(s.URL, image)
		kind := p.Type
		if kind == "" {
			kind = "website"
		}
		card := "summary"
		if image != "" {
			card = "summary_large_image"
		}

		meta("name", "description", p.Description)
		if page != "" {
			b.WriteString(`<link rel="canonical" href="` + html.EscapeString(page) + `">` + "\n")
		}
		meta("property", "og:type", kind)
		meta("property", "og:site_name", s.Name)
		meta("property", "og:title", p.Title)
		meta("property", "og:description", p.Description)
		meta("property", "og:url", page)
		meta("property", "og:image", image)
		if image != "" {
			meta("property", "og:image:alt", alt)
		}
		meta("name", "twitter:card", card)
		meta("name", "twitter:site", s.Twitter)
		meta("name", "twitter:title", p.Title)
		meta("name", "twitter:description", p.Description)
		meta("name", "twitter:image", image)
		if image != "" {
			meta("name", "twitter:image:alt", alt)
		}
		_, err := io.WriteString(w, b.String())
		return err
	})
}

func absolute(base, ref string) string {
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
		return ref
	}
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(ref, "/")
}
