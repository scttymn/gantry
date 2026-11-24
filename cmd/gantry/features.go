package main

import (
	"fmt"
	"strings"
)

var signInFeature = featureYAML("sign-in", "public", []string{"/login", "/login/", "/logout", "/passwords", "/passwords/"}, "/login", "form")

func featureYAML(name, role string, routes []string, visit, selector string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "       - name: %s\n         routes:\n", name)
	for _, r := range routes {
		fmt.Fprintf(&b, "           - %s\n", r)
	}
	fmt.Fprintf(&b, "         role: %s\n         visit:\n           - path: %s\n             selector: %s\n", role, visit, selector)
	return b.String()
}
