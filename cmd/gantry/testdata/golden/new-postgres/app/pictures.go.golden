package app

import "github.com/scttymn/gantry/images"

// Pictures makes the copies of uploaded pictures: a Storage's Images
// (gantry g storage). The app's main has them made in a child, this program
// started as `<exe> resize ...` (images.Child), so a large picture's memory
// is never the server's. Tests make them in process.
var Pictures = &images.Pipeline{}
