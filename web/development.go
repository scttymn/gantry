package web

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/scttymn/gantry/devcheck"
)

type developmentWarningsKey struct{}

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
			r = r.WithContext(context.WithValue(r.Context(), developmentWarningsKey{}, warnings))
			w.Header().Set("Cache-Control", "no-store")
		}
		serveDevelopmentPage(next, w, r, injectDevelopmentHTML)
	}), log)
}

// DevelopmentWarnings is empty outside Development middleware and when there
// are no findings. Native details/summary keeps the panel keyboard accessible
// and usable without JavaScript. All source text is escaped, never trusted HTML.
func DevelopmentWarnings() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		warnings, _ := ctx.Value(developmentWarningsKey{}).([]devcheck.Warning)
		if len(warnings) == 0 {
			return nil
		}
		_, err := fmt.Fprintf(w, `<aside aria-label="Gantry development warnings" style="flex:none;width:100%%;box-sizing:border-box;background:#fff8e6;color:#332600;border-top:1px solid #bb8500;font:14px/1.5 system-ui,sans-serif;text-align:left"><details><summary style="cursor:pointer;padding:12px 16px;font-weight:700">Gantry: %d development warning(s)</summary><div style="padding:0 16px 12px;max-height:40vh;overflow:auto"><p>Development only &middot; project-wide checks. Reload after fixing.</p><ul style="padding-left:20px">`, len(warnings))
		if err != nil {
			return err
		}
		for _, warning := range warnings {
			location := warning.File
			if warning.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, warning.Line)
			}
			if _, err = fmt.Fprintf(w, `<li style="margin:12px 0;overflow-wrap:anywhere"><strong>%s</strong> <code>%s</code><br>%s</li>`, html.EscapeString(warning.Severity+" · "+warning.Rule), html.EscapeString(location), html.EscapeString(warning.Message)); err != nil {
				return err
			}
		}
		_, err = io.WriteString(w, "</ul></div></details></aside>")
		return err
	})
}

// DevelopmentPage reserves space for the bottom panel. Expanding it reduces the
// scrollable page area instead of covering content. Without warnings it renders
// only its children, so production layout is unchanged.
func DevelopmentPage() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		children := templ.GetChildren(ctx)
		ctx = templ.ClearChildren(ctx)
		warnings, _ := ctx.Value(developmentWarningsKey{}).([]devcheck.Warning)
		if len(warnings) == 0 {
			return children.Render(ctx, w)
		}
		if _, err := io.WriteString(w, developmentPageOpen); err != nil {
			return err
		}
		if err := children.Render(ctx, w); err != nil {
			return err
		}
		if _, err := io.WriteString(w, `</div>`); err != nil {
			return err
		}
		if err := DevelopmentWarnings().Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</div>`)
		return err
	})
}
