//go:build nodevcheck

package web

import (
	"log/slog"
	"net/http"
)

// Development is Timing alone: a build with -tags nodevcheck, as a generated
// app's production build is, has no development checks, so its binary carries
// no source scanner. root is unused. Such a build has no DevelopmentWithRules.
func Development(next http.Handler, root string, log *slog.Logger) http.Handler {
	return Timing(next, log)
}
