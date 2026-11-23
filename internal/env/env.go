// Package env is what gantry reads from an app's environment itself, rather
// than through the app's config.
package env

import "os"

// Development is whether the app runs in development: GANTRY_ENV is
// "development". Any other value, or none, is production's behavior. It's
// the one read of GANTRY_ENV in the framework (golangci-lint's forbidigo
// holds the rest to it), so the inbox, the router's diagnostics and the
// rest agree on what development is.
func Development() bool { return os.Getenv("GANTRY_ENV") == "development" }
