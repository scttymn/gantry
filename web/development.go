package web

import (
	"context"
	"fmt"
	"html"
	"io"

	"github.com/a-h/templ"
)

type developmentWarningsKey struct{}

// developmentWarning has devcheck.Warning's fields, so the panel builds without
// devcheck (-tags nodevcheck).
type developmentWarning struct {
	File                    string
	Line                    int
	Rule, Message, Severity string
}

// DevelopmentWarnings is empty outside Development middleware and when there
// are no findings. Native details/summary keeps the panel keyboard accessible
// and usable without JavaScript. All source text is escaped, never trusted HTML.
func DevelopmentWarnings() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		warnings, _ := ctx.Value(developmentWarningsKey{}).([]developmentWarning)
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
		warnings, _ := ctx.Value(developmentWarningsKey{}).([]developmentWarning)
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
