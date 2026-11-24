// Package featuremap is what testkit.Verify and gantry g features agree on
// about a feature map (test/features.yaml): which of the router's routes a
// feature's routes name. One answer for both, so the routes g features adds
// are the ones Verify reports in no feature.
package featuremap

import "strings"

// Covers reports whether a feature's route names the router's route with
// that method ("" for any) and path. A feature's route is as `gantry routes`
// shows one, "GET /posts", or with no method, any method's. One ending in /
// is every route under it too, as ServeMux's patterns are: "/posts/" names
// "GET /posts/{id}".
func Covers(route, method, path string) bool {
	m, p, ok := strings.Cut(route, " ")
	if !ok {
		m, p = "", route
	}
	if m != "" && m != method {
		return false
	}
	return path == p || strings.HasSuffix(p, "/") && strings.HasPrefix(path, p)
}
