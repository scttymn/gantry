//go:build !nodevcheck

package web

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/scttymn/gantry/devcheck"
)

// Development enables source warnings and response timing. Router enables it automatically in development. Install manually ONLY in
// development. root is the app source directory. Each HTML GET rescans it so
// edits are reflected on reload without a separate watcher or background work.
// Render DevelopmentWarnings in the layout to display the request's findings.
func Development(next http.Handler, root string, log *slog.Logger) http.Handler {
	return DevelopmentWithRules(next, root, log, devcheck.DefaultRules())
}

// DevelopmentWithRules uses the same scanner and exception policy with an
// extended compiled rule registry. Enable only inside a development guard.
func DevelopmentWithRules(next http.Handler, root string, log *slog.Logger, rules []devcheck.Rule) http.Handler {
	return Timing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && WantsHTML(r) {
			warnings, err := devcheck.ScanWithRules(root, rules)
			if err != nil {
				if log != nil {
					log.Warn("development checks failed", "err", err)
				}
				warnings = append(warnings, devcheck.Warning{Rule: "check-error", Message: "Could not finish development checks. Check the server log and source directory."})
			}
			panel := make([]developmentWarning, len(warnings))
			for i, warning := range warnings {
				panel[i] = developmentWarning(warning)
			}
			r = r.WithContext(context.WithValue(r.Context(), developmentWarningsKey{}, panel))
			w.Header().Set("Cache-Control", "no-store")
		}
		serveDevelopmentPage(next, w, r, injectDevelopmentHTML)
	}), log)
}
