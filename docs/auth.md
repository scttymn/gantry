# Authentication

Gantry supports email/password login and passwordless **email codes**, with the
same database-backed sessions. Email codes are an explicit application choice;
existing applications keep their current password login. Apple and Google can
be added as [social identities](social-login.md), sharing the same users and
sessions. TOTP authenticator apps and passkeys remain planned features.

## Password login and first-install setup

`bin/gantry g auth` generates a thin wrapper around shared `gantry/auth`, views
in the app layout, migrations, tests, and the `auth:setup-code` operator task.
Follow its printed wiring steps. Default login/logout paths are `/login` and
`/logout`; old app-owned generators used `/session/new` and `/session`.

A fresh migration creates a singleton `auth_setup` row with `completed=false`.
When password routes start, Gantry generates a cryptographically random setup
code, saves it with file mode 0600 on the persistent data volume, and stores only
its digest in the database. It never prints the code in startup/deploy logs.
`/login` shows **Create your first account**, with setup code, email, password
and confirmation. The code proves control of the installation; it is not email
verification. The first account's roles remain the application's choice.

Retrieve the code in your trusted terminal:

```sh
bin/gantry task auth:setup-code
```

On production, an operator logged in to Houston (`houston login`) can retrieve it
from the app's folder:

```sh
houston exec --server /app task auth:setup-code
```

This requires Houston v0.5.10 or newer (`houston exec --server`) and the saved
`houston login`: it goes through Mission Control's API, no SSH needed. It runs in the
first matching running web container and sends output directly to your terminal,
without recording it in deployment logs. With an older Houston CLI, run
`/app task auth:setup-code` in an authorized terminal inside the deployed app
container instead. Automatic retrieval in Houston's UI is not implemented.

Do not put this command in a release hook or repeat its output in logs/chat.
Append `--rotate` to replace a lost pending code, invalidating the old one; it
cannot reopen completed setup.

Creating the account and recording completion happen in one transaction, with
cross-process database locking on SQLite and PostgreSQL. A second submission
cannot create another first account. Successful setup signs the user in,
clears the stored digest and removes the code file. Restarting/upgrading, deleting
the last user, or repeating an old form never reopens setup. Account recovery is
an explicit administrative operation, not an unauthenticated registration form.
Missing setup schema/state or database failures fail closed.

Shared Auth opts in with `FirstUser: &auth.FirstUser{CodeFile: absolutePath}`;
nil preserves existing behavior. `Routes` prepares setup at startup; custom
routers call `PrepareFirstUser(ctx)` themselves. Keep the code file on private
persistent storage, outside the public/assets tree. Instances share the setup
state in the app database. With separate volumes, retrieve the code on the
instance that generated it, or deliberately rotate it on the target instance.
If a filesystem/database write is interrupted, a digest mismatch fails closed;
operator rotation repairs an incomplete setup.

Use `FirstUser.CreateUser(ctx, tx, email, passwordDigest)` for required profile
fields or the application's initial administrator role. The callback must insert
with tx and return that account; the digest is already hashed. Returning an error
rolls back the account and completion together. Custom setup views implement
`Views.Setup`; never echo passwords or the setup code. Every view answers with
`Page.Status` (200 when it's zero), as the generated ones do.
A programmatically created user through this configured Auth closes setup in the
same transaction. Startup also closes setup if it finds seeded/imported users.

**Existing installations:** add the `auth_setup` table through an app migration
but seed `completed=true`, even if there are currently zero users. Never use an
empty-user check to decide whether an existing installation is new. Missing rows
are errors, not permission to initialize. Whole-database restoration can restore
an older pending state; rotate its code before exposing such a restored install.
The framework does not recreate missing tables or reset completion automatically.

## Start an app with email codes

```sh
bin/gantry g auth --email-code --signup
```

`--signup` creates an account only after the user verifies their email. Omit it
for existing-account-only login. `bin/gantry g auth` generates password login and first-install setup using
the same shared Auth implementation. The generator refuses to overwrite existing auth files or
user/session migrations.

The email-code recipe writes user/session and code/limit migrations, templ views
inside the app's shared layout, a thin `app/auth` integration and an integration
test. Security-sensitive operations stay in `github.com/scttymn/gantry/auth`.
Follow its printed steps to generate views, migrate, configure mail, mount the
routes, and run tests. Generated code still needs wiring into `App`.

```go
a.Auth = auth.New(a.DB, a.Signer, a.Mail, a.Mail.From, []byte(authCodeKey))
a.Auth.Routes(rt)
rt.Scope("/account", web.Pipeline{a.Auth.Required}, func(s *web.Scope) {
    // The signed-in user is available through auth.Current(r).
})
```

The app owns the shared mail service and the authentication secret. Routes
validates email-code configuration at startup and panics on a missing database,
sender, valid From address or suitable key.

## Browser flow

1. `GET /login` asks for an email address.
2. `POST /login` sends a six-digit code and sets an HttpOnly challenge cookie.
3. `GET /login/verify` shows the code form, resend button and change-email link.
4. `POST /login/verify` verifies and consumes the code, optionally creates the
   user, then starts the same session used by password login. A wrong, expired
   or used code redirects (303) back to the code form with an alert.
5. `POST /logout` ends the session.

Custom `Paths.Login` moves both the email form and its `/verify` routes.
`Paths.Logout`, `Paths.AfterLogin` and the normal return-to behavior also apply.
Custom views use `Views.RequestCode` and `Views.VerifyCode`; the latter receives
the address in `Page.EmailAddress`. Code mode does not mount password/reset forms.

Both signup modes send the same code email to a syntactically valid address.
Existing-account-only mode refuses an unknown user after code verification;
requesting a code never exposes whether the account exists, including through a
different SMTP path. The request itself never creates a user. Default signup
creates `users` with an empty, unusable password digest; no password is emailed
or assigned. Apps with other required user fields supply
`EmailCodes.CreateUser(ctx, tx, email)`. This hook runs only after verification,
inside the consumption transaction; use the provided transaction and perform
external work after commit. An error rolls back signup and consumption.

## Sessions and sign-out

Every sign-in method starts the same database session. A session ends 30 days
after its last use (`Idle`) or 90 days after sign-in (`Lifetime`), whichever
comes first. The cookie expires with `Lifetime`. Use is recorded at most hourly,
so `Idle` counts in hours. The generated `auth.New` sets both values, so change
them there. A zero value takes the default. `auth.NoLimit`, or any negative
duration, turns that limit off. With both limits off, a session lasts until
sign-out, as in Rails 8's generator.

To sign out, send `POST` (or `DELETE`) to `/logout` from a form:

```html
<form method="post" action="/logout"><button type="submit">Sign out</button></form>
```

The generated `SignOut` component is this form. `GET /logout` is not a route.
Any site can send a browser there with a link or a redirect, and cross-origin
protection guards only the methods that change state.

Password reset gives the same answer, equally fast, whether or not the address
has an account. Auth looks up the address and sends the email after the
response. At most 100 reset emails are in flight at once. Past that, a request
gets the same answer and no email. `a.Auth.Wait()` waits for the emails in
flight, each a minute at most. Call it in `serve` after `server.Shutdown` and
before the database closes, so a deploy doesn't drop them. A test that reads
the reset email calls it first. The reset pages and the reset form's answers
send `Cache-Control: no-store` and `Referrer-Policy: no-referrer`, because the
edit page's address carries the reset token.

**Upgrading.** Apps that left `Idle` and `Lifetime` at zero now get the 30-day
and 90-day limits. A session idle for more than 30 days, or signed in more than
90 days ago, ends the next time it is used after the upgrade. Its user signs in
again. To keep the old behavior, set both values to `auth.NoLimit`. A sign-out
link (`<a href="/logout">`) no longer signs anyone out. Replace it with the form
above.

## Shared Auth and existing applications

Apps already using `gantry/auth` can add the code/limit migration from the
[email-code recipe](../cmd/gantry/templates/emailcodes/codes.sql.tmpl), then set:

```go
a.Auth.EmailCodes = &auth.EmailCodes{
    Key: []byte(authCodeKey), // configured outside the database
    Signup: true,            // only if this app permits self-registration
    DailyLimit: 90,           // example provider budget; zero omits this cap
}
```

The app still supplies `Mail`, `From`, `DB` and `Signer`. Every instance must
share the code key and database. The core schema is documented in `auth/auth.go`;
an existing `password_digest` column must accept the empty string for default
passwordless signup. Users with existing password digests keep them unchanged.

Apps using the older generated, app-owned password implementation must adopt the
shared Auth deliberately, preserving their custom user fields, filters, paths
and views. Do not generate over an existing implementation. Keep auth schema
changes in the app's migrations; the library never creates tables at runtime.

For mobile or custom controllers, call `RequestLoginCode(ctx, email, clientIP)`
and hold its returned opaque challenge in the requesting client. Submit the
challenge and emailed code to `VerifyLoginCode(ctx, challenge, code, clientIP)`.
On success use `StartSession`, or issue the app's existing API token. Keep the
challenge out of URLs/logs, use POST bodies, and derive the client IP from trusted
request/proxy metadata, never a field supplied by the caller. Call
`ValidateEmailCodes` during startup if not mounting Auth's browser routes.

## Delivery and development

Auth uses the [app-wide mail service](mail.md), shared with other features.
New apps capture mail at `/dev/inbox` automatically in development. Configure
`MAIL_FROM` and `SMTP_*` settings once for production delivery. For Resend, set
`SMTP_HOST=smtp.resend.com`, `SMTP_PORT=587`, `SMTP_USERNAME=resend` and supply the
API key as `SMTP_PASSWORD` through deployment secrets.

Custom integrations can still supply any `mail.Sender`, for example:

```go
sender := mail.SMTP{
    Host: "smtp.resend.com",
    Port: 587,
    Username: "resend",
    Password: resendAPIKey,
}
```

Use a verified sending domain and inject the API key through deployment secrets.
These are [Resend's SMTP settings](https://resend.com/docs/send-with-smtp); Gantry
is not tied to this provider. Provider quotas are separate from app limits.

Generate `AUTH_CODE_KEY` once from cryptographic randomness (for example,
`openssl rand -hex 32`) and store it in deployment secrets. At least 32 bytes are
required. Do not use a development constant or a key stored in the app database
for production: the key prevents offline guessing of short codes after a
database leak. Rotation invalidates outstanding codes and resets keyed rate
limit buckets; sessions are unaffected.

Tests inject a capturing sender. Development uses the shared persistent inbox.
Apps can also explicitly point SMTP at a separate local capture service, with
`SMTP_TLS=none` when it doesn't offer TLS. `mail.Log` is rejected in code mode because it logs a summary
without delivering a code. There is no shared development OTP or bypass code.

Delivery happens after the challenge transaction commits, without holding the
database connection. A reported delivery failure invalidates that challenge
and returns a retry message. It still counts against sending limits. A custom
asynchronous sender is responsible for queue persistence and delivery failure
handling; returning nil means it accepted responsibility for the message.

## Limits, concurrency and maintenance

| Control | Default |
| --- | --- |
| Code lifetime | 10 minutes |
| Attempts per challenge | 5 |
| Resend cooldown per normalized email | 1 minute |
| Requests per email | 3 per 15-minute fixed window |
| Requests per IP | 20 per 15-minute fixed window |
| Verification attempts per IP | 50 per 15-minute fixed window |
| Optional global sending cap | `DailyLimit` per 24-hour fixed window |

Request slots, guesses, resend replacement and one-time consumption use database
transactions. They apply across app instances sharing that database, independent
of the optional process-local `Auth.Limits` used by password login. Issuing a new
code invalidates the previous challenge for the address. Parallel submissions
can consume a code only once. Codes are generated with `crypto/rand` and stored
as an HMAC bound to the challenge and email. Auth stores only the keyed digest. Codes necessarily pass through the configured
mail sender; the development inbox intentionally retains the complete test email.

Schedule `Auth.PruneLoginCodes(ctx)` with Gantry jobs, for example hourly. It
deletes expired challenges and expired limit buckets without resetting active
limits. This bounds retained data as new addresses and IPs appear.

## Verification and Gantry Strict

Run `bin/gantry check --strict` and the app's tests. Exercise the browser flow,
resend, wrong/expired codes, signup and sign-out with a capturing mail service.
Framework tests exercise both SQLite and PostgreSQL, including simultaneous
verification and failed attempts. Set `GANTRY_TEST_POSTGRES_URL` to a disposable
test server when running the framework suite; without it Postgres tests skip.

Strict does not currently prove correct custom OTP implementations. Matching
randomness or hashing calls would flag legitimate code while missing other
bypasses, so this feature relies on the shared implementation, generator,
integration tests and review instead of a speculative static rule.
