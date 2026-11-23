// Package env is what gantry reads from an app's environment itself, rather
// than through the app's config: GANTRY_ENV, whose one read is here
// (golangci-lint's forbidigo holds the framework to it), so the inbox, the
// router's diagnostics, the signing key and the rest agree on it. Any value
// but development or test, or none, is production's behavior.
package env

import "os"

// Development is whether the app runs in development: GANTRY_ENV is
// "development".
func Development() bool { return name() == "development" }

// Test is whether the app runs its tests: GANTRY_ENV is "test".
func Test() bool { return name() == "test" }

func name() string { return os.Getenv("GANTRY_ENV") }
