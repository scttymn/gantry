package auth

import (
	"html"
	"io"
	"net/http"
	"net/url"
)

// DefaultViews are plain forms, to replace with the app's own (Views): they
// show what each page needs, and nothing of any app's look.
func DefaultViews() Views {
	return Views{
		Setup: func(w http.ResponseWriter, r *http.Request, p Page) error {
			return plain(w, "Create your first account", p, `<p>Enter the setup code from your deployment terminal to finish installing this app.</p>
<form id="first-user" method="post" action="`+html.EscapeString(p.Paths.Login+"/setup")+`">
<label>Setup code <input type="password" name="setup_code" required autofocus autocomplete="off" maxlength="128"></label>
<label>Email <input type="email" name="email_address" value="`+html.EscapeString(p.EmailAddress)+`" required autocomplete="username" maxlength="254"></label>
<label>Password <input type="password" name="password" required autocomplete="new-password" maxlength="72"></label>
<label>Confirm password <input type="password" name="password_confirmation" required autocomplete="new-password" maxlength="72"></label>
<button type="submit">Create account</button></form>`)
		},
		RequestCode: func(w http.ResponseWriter, r *http.Request, p Page) error {
			return plain(w, "Sign in", p, `<form method="post" action="`+html.EscapeString(p.Paths.Login)+`">
<label>Email <input type="email" name="email_address" required autofocus autocomplete="email" maxlength="254"></label>
<button type="submit">Email me a code</button>
</form>`)
		},
		VerifyCode: func(w http.ResponseWriter, r *http.Request, p Page) error {
			return plain(w, "Check your email", p, `<p>Enter the code sent to `+html.EscapeString(p.EmailAddress)+`.</p>
<form method="post" action="`+html.EscapeString(p.Paths.Login+"/verify")+`">
<label>Code <input type="text" name="code" required autofocus inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" minlength="6" maxlength="6"></label>
<button type="submit">Continue</button>
</form>
<form method="post" action="`+html.EscapeString(p.Paths.Login)+`">
<input type="hidden" name="email_address" value="`+html.EscapeString(p.EmailAddress)+`">
<button type="submit">Send a new code</button>
</form>
<p><a href="`+html.EscapeString(p.Paths.Login)+`">Use a different email</a></p>`)
		},
		Login: func(w http.ResponseWriter, r *http.Request, p Page) error {
			return plain(w, "Sign in", p, `<form method="post" action="`+html.EscapeString(p.Paths.Login)+`">
<label>Email <input type="email" name="email_address" required autofocus autocomplete="username"></label>
<label>Password <input type="password" name="password" required autocomplete="current-password" maxlength="72"></label>
<button type="submit">Sign in</button>
</form>`+forgot(p))
		},
		ForgotPassword: func(w http.ResponseWriter, r *http.Request, p Page) error {
			return plain(w, "Forgot your password?", p, `<form method="post" action="`+html.EscapeString(p.Paths.Passwords)+`">
<label>Email <input type="email" name="email_address" required autofocus autocomplete="username"></label>
<button type="submit">Email reset instructions</button>
</form>`)
		},
		EditPassword: func(w http.ResponseWriter, r *http.Request, p Page, token string) error {
			return plain(w, "Update your password", p, `<form method="post" action="`+html.EscapeString(p.Paths.Passwords+"/"+url.PathEscape(token))+`">
<input type="hidden" name="_method" value="patch">
<label>New password <input type="password" name="password" required autocomplete="new-password" maxlength="72"></label>
<label>Confirm it <input type="password" name="password_confirmation" required autocomplete="new-password" maxlength="72"></label>
<button type="submit">Save</button>
</form>`)
		},
	}
}

// forgot links to the password reset, when the app offers one.
func forgot(p Page) string {
	if p.Paths.Passwords == "" {
		return ""
	}
	return `
<p><a href="` + html.EscapeString(p.Paths.Passwords) + `/new">Forgot password?</a></p>`
}

func plain(w http.ResponseWriter, title string, p Page, form string) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>` +
		html.EscapeString(title) + `</title></head><body><main><h1>` + html.EscapeString(title) + `</h1>`
	if p.Notice != "" {
		body += `<p role="status">` + html.EscapeString(p.Notice) + `</p>`
	}
	if p.Alert != "" {
		body += `<p role="alert">` + html.EscapeString(p.Alert) + `</p>`
	}
	if p.Status != 0 {
		w.WriteHeader(p.Status)
	}
	_, err := io.WriteString(w, body+form+`</main></body></html>`)
	return err
}
