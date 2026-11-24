package web

import (
	"context"
	"fmt"
	"html"
	"io"

	"github.com/a-h/templ"
)

type developmentWarningsKey struct{}

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
		_, err := fmt.Fprintf(w, `%s<aside aria-label="Gantry development warnings" data-gantry-development-warnings><details><summary>Gantry: %d development warning(s)</summary><div><p>Development only &middot; project-wide checks. Reload after fixing.</p><ul>`, developmentStylesheet, len(warnings))
		if err != nil {
			return err
		}
		for _, warning := range warnings {
			location := warning.File
			if warning.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, warning.Line)
			}
			if _, err = fmt.Fprintf(w, `<li><strong>%s</strong> <code>%s</code><br>%s</li>`, html.EscapeString(warning.Severity+" · "+warning.Rule), html.EscapeString(location), html.EscapeString(warning.Message)); err != nil {
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
