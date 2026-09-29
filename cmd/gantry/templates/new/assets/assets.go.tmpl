// Package assets is the app's stylesheets, scripts, fonts and images,
// embedded in the binary and served under fingerprinted names (see gantry's
// assets package for the folder convention). public/ is served at the
// site's root, as Rails' public/: robots.txt, and the error pages.
// js/vendor/ is the JavaScript packages `gantry importmap pin` downloaded.
package assets

import (
	"embed"
	"net/http"

	gantry "github.com/scttymn/gantry/assets"
)

//go:embed css js public
var files embed.FS

// All is every asset, digested once at start.
var All = gantry.MustNew(files)

// Styles is the app's stylesheets, bundled once at start: drawn into the
// page's head while they're small, linked once they grow.
var Styles = All.Styles("application.css")

// ImportMap is the page's JavaScript by name (Rails' config/importmap.rb):
// js/application.js, the Stimulus controllers in js/controllers, and every
// package in js/vendor ("@hotwired--turbo.js" is "@hotwired/turbo").
var ImportMap = All.ImportMap(
	gantry.Pin("application", "application.js"),
	gantry.PinAll("controllers"),
	gantry.PinVendor("vendor"),
)

// Path is an asset's URL: "/assets/application-1a2b3c4d.css".
func Path(name string) string { return All.Path(name) }

// Routes mounts /assets/ and the root files.
func Routes(mount func(pattern string, h http.Handler)) { All.Routes(mount) }
