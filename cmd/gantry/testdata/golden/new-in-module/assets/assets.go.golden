package assets

import (
	"embed"
	"net/http"

	gantry "github.com/scttymn/gantry/assets"
)

//go:embed css js public all:built
var files embed.FS

var All = gantry.MustNew(files)

var Styles = All.Styles("application.css")

var ImportMap = All.ImportMap(
	gantry.Pin("application", "application.js"),
	gantry.PinAll("controllers"),
	gantry.PinVendor("vendor"),
)

var Images = All.Images(nil)

func Path(name string) string { return All.Path(name) }

func Routes(mount func(pattern string, h http.Handler)) { All.Routes(mount) }
