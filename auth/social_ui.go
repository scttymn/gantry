package auth

import (
	"context"
	"embed"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/a-h/templ"
	"github.com/scttymn/gantry/assets"
)

//go:embed css/social.css
var socialFiles embed.FS

// SocialStyles is bundled and minified once through Gantry's asset pipeline.
// The shared buttons include this small inline bundle. Add SocialStyles.Hash()
// to a strict CSP's style-src. App CSS can override --gantry-social-* tokens.
var SocialStyles = assets.MustNew(socialFiles).InlineStyles("social.css")

// SocialButtons renders the configured sign-in providers and email divider.
// Nothing is rendered when social login is disabled. Actions remain full-page
// POST forms; apps can replace this component using Page.Social.
func SocialButtons(p Page) templ.Component { return socialComponent(p, false) }

// SocialConnections renders the same provider buttons for account settings,
// replacing connected providers with their status and omitting the divider.
func SocialConnections(p Page) templ.Component { return socialComponent(p, true) }

func socialComponent(p Page, link bool) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, socialForms(p, link))
		return err
	})
}

func socialForms(p Page, link bool) string {
	if len(p.Social) == 0 {
		return ""
	}
	var b strings.Builder
	// This fixed, embedded stylesheet cannot fail to render to a strings.Builder.
	_ = SocialStyles.Tag().Render(context.Background(), &b)
	b.WriteString(`<div class="gantry-social">`)
	for _, provider := range p.Social {
		if link && provider.Linked {
			fmt.Fprintf(&b, `<p class="gantry-social__connected">%s is connected.</p>`, html.EscapeString(provider.Label))
			continue
		}
		action, label := provider.StartURL, "Continue with "+provider.Label
		if link {
			action, label = provider.LinkURL, "Connect "+provider.Label
		}
		icon, variant := "", ""
		switch provider.Name {
		case "google":
			icon, variant = googleSocialIcon, " gantry-social__button--google"
		case "apple":
			icon, variant = appleSocialIcon, " gantry-social__button--apple"
		}
		fmt.Fprintf(&b, `<form class="gantry-social__form" method="post" action="%s" data-turbo="false"><button class="gantry-social__button%s" type="submit">%s<span>%s</span></button></form>`, html.EscapeString(action), variant, icon, html.EscapeString(label))
	}
	b.WriteString(`</div>`)
	if !link {
		b.WriteString(`<div class="gantry-social__divider">Or with email</div>`)
	}
	return b.String()
}

// Provider artwork comes from the official button examples. See
// docs/social-login.md for source links and branding guidance.
const googleSocialIcon = `<svg class="gantry-social__icon" xmlns="http://www.w3.org/2000/svg" aria-hidden="true" focusable="false" viewBox="0 0 48 48"><path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"></path><path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"></path><path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"></path><path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"></path><path fill="none" d="M0 0h48v48H0z"></path></svg>`
const appleSocialIcon = `<svg class="gantry-social__icon" xmlns="http://www.w3.org/2000/svg" aria-hidden="true" focusable="false" viewBox="18 15 20 21"><path fill="currentColor" d="M28.2226562,20.3846154 C29.0546875,20.3846154 30.0976562,19.8048315 30.71875,19.0317864 C31.28125,18.3312142 31.6914062,17.352829 31.6914062,16.3744437 C31.6914062,16.2415766 31.6796875,16.1087095 31.65625,16 C30.7304687,16.0362365 29.6171875,16.640178 28.9492187,17.4494596 C28.421875,18.06548 27.9414062,19.0317864 27.9414062,20.0222505 C27.9414062,20.1671964 27.9648438,20.3121424 27.9765625,20.3604577 C28.0351562,20.3725366 28.1289062,20.3846154 28.2226562,20.3846154 Z M25.2929688,35 C26.4296875,35 26.9335938,34.214876 28.3515625,34.214876 C29.7929688,34.214876 30.109375,34.9758423 31.375,34.9758423 C32.6171875,34.9758423 33.4492188,33.792117 34.234375,32.6325493 C35.1132812,31.3038779 35.4765625,29.9993643 35.5,29.9389701 C35.4179688,29.9148125 33.0390625,28.9122695 33.0390625,26.0979021 C33.0390625,23.6579784 34.9140625,22.5588048 35.0195312,22.474253 C33.7773438,20.6382708 31.890625,20.5899555 31.375,20.5899555 C29.9804688,20.5899555 28.84375,21.4596313 28.1289062,21.4596313 C27.3554688,21.4596313 26.3359375,20.6382708 25.1289062,20.6382708 C22.8320312,20.6382708 20.5,22.5950413 20.5,26.2911634 C20.5,28.5861411 21.3671875,31.013986 22.4335938,32.5842339 C23.3476562,33.9129053 24.1445312,35 25.2929688,35 Z"/></svg>`
