package main

import (
	"fmt"
	"strings"
)

// signInFeature is g auth's routes (auth.Auth.Routes'), social sign-in's
// under /login/ too.
var signInFeature = featureYAML("sign-in", "public", []string{"/login", "/login/", "/logout", "/passwords", "/passwords/"}, "/login", "form")

// featureYAML is a feature for an app's feature map, test/features.yaml
// (testkit.Feature), as a next step shows it: to paste under the map's
// features, two spaces in, once its routes are routed.
func featureYAML(name, role string, routes []string, visit, selector string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "       - name: %s\n         routes:\n", name)
	for _, r := range routes {
		fmt.Fprintf(&b, "           - %s\n", r)
	}
	fmt.Fprintf(&b, "         role: %s\n         visit:\n           - path: %s\n             selector: %s\n", role, visit, selector)
	return b.String()
}
