# Apple and Google sign-in

Gantry Auth treats a user and their sign-in identities separately. Password and
email-code login use the user's email credential. Apple and Google add stable
provider identities to that same user. All methods create the same database
`sessions` records: normal idle/lifetime expiry, session deletion, sign-out and
account-wide revocation apply equally. Provider access, refresh and ID tokens
are not stored as application sessions.

## Add to an app

Start with shared password or email-code Auth, then run:

```sh
bin/gantry g auth --social
bin/gantry db migrate
bin/gantry exec templ generate
bin/gantry exec go mod tidy
```

The additive recipe creates the identities/flow migration, configuration helper,
an app-layout identity screen and tests. It refuses to overwrite existing files.
It does not replace your existing authentication. Before mounting Auth routes:

```go
if err := auth.ConfigureSocial(a.Auth, os.Getenv); err != nil {
    panic(err) // fail startup on partial/invalid credentials
}
a.Auth.Routes(rt)
```

Alternatively set `a.Auth.Social` directly using `gantryauth.Social`,
`NewGoogle(GoogleConfig{...})` and `NewApple(AppleConfig{...})`.
Constructors do no network I/O. The verifier caches provider signing keys and
refreshes them when needed. Provider requests have a ten-second timeout.

New auth views include `@gantryauth.SocialButtons(p)`. Add it to older custom
login views; these are normal POST forms with Turbo disabled so the provider
redirect navigates the whole page. Only configured providers appear. Link
`/login/identities` from account settings. An app can customize `Views.Identities`
and `Page.Social` while preserving POST actions and full-page navigation.

## Configuration

Pass credentials through the app's Compose environment/private secret settings.
Nothing is enabled when neither provider is configured; partial configuration
fails startup. Never commit keys or place secrets in URLs or command arguments.

| Setting | Purpose |
| --- | --- |
| `GOOGLE_CLIENT_ID` | Web application's OAuth client ID |
| `GOOGLE_CLIENT_SECRET` | That client's secret |
| `GOOGLE_REDIRECT_URL` | Exact public callback, e.g. `https://app.example/login/google/callback` |
| `APPLE_CLIENT_ID` | Apple's web Services ID |
| `APPLE_TEAM_ID` | Apple developer Team ID |
| `APPLE_KEY_ID` | Sign in with Apple key ID |
| `APPLE_PRIVATE_KEY_FILE` | Private mounted PKCS#8 `.p8` file inside the container |
| `APPLE_PRIVATE_KEY` | PEM contents instead of the file; set only one |
| `APPLE_REDIRECT_URL` | Exact HTTPS callback, e.g. `https://app.example/login/apple/callback` |
| `AUTH_SOCIAL_SIGNUP` | `true` permits new users; defaults to `false` |

Google: create a web OAuth client, configure its consent screen and register the
exact redirect URI. Apple: configure Sign in with Apple for an App ID, associate
a Services ID, register the web domain/return URL and create a signing key.
Gantry signs short-lived ES256 client secrets from the Apple key, so there is no
manually generated six-month JWT to keep replacing. Rotate the configured key
when rotating Apple credentials.

Google's callback is GET. Apple's email scope uses POST `form_post`; Gantry
registers an exact callback exception in its router and validates independent
state/browser proof. The rest of the app keeps normal cross-origin protection.
Apple's temporary flow cookie is HttpOnly, Secure and SameSite=None; the normal
application session cookie remains SameSite=Lax. Do not broadly exempt `/login`
or disable the application's origin protection.

Use the configured callback URLs, not the incoming Host header. Custom login
paths and scoped mounts must be reflected in registered callback URLs.
Apple requires a real HTTPS domain; HTTP `*.localhost` previews cannot complete
Apple login. Use a registered development HTTPS domain/tunnel for live testing.
Google permits HTTP loopback development redirects; register the exact
`localhost` URL and port. The ordinary local page preview needs no live keys.

## Account matching and automatic linking

1. An already-linked `(provider, subject)` signs into its user, even when the
   provider's email changes. Gantry does not silently change the user's email.
2. For a new provider identity, an exact normalized email match automatically
   links **only when both sides establish email ownership**. The existing user
   must have a verified `email` identity, and the provider must supply verified,
   authoritative email proof. Successful OTP verification records the email
   identity when Social is enabled. Social signup records it too.
3. A password hash, an entered email, or a provider's display profile is not
   email-ownership proof. Unverified matches ask the user to sign in with their
   existing method and connect the provider on the Sign-in methods screen.
   Linking requires a live session created within the last ten minutes and
   fresh provider authentication. Another user's identity cannot be reassigned.
4. New users require `Signup: true` and verified, authoritative email. Signup and
   both identity records are one transaction. `Social.CreateUser` supports
   app-specific profile fields/roles; insert using the supplied transaction and
   return the matching user. Errors roll back the entire operation.

Google establishes current mailbox ownership for verified Gmail and Workspace
accounts. A third-party address can have `email_verified=true` even when Google
is no longer authoritative for it. Those users first use the app's email/login
flow and explicitly connect Google. See [Google's verification guidance](https://developers.google.com/identity/sign-in/web/backend-auth).
Apple verified addresses include private relay addresses. Relay addresses are
separate identities; never guess that a relay belongs to an existing real email.
Users can connect Apple after signing in with their existing method. Register
sending domains with Apple's relay service if the app sends mail to relay users.

Existing apps can backfill `auth_identities` with `provider='email'`,
`subject=normalized_email`, `user_id`, `email_address=normalized_email` and the
actual verification timestamp **only for emails they independently verified**.
Do not mark all users verified merely because a password or user row exists.
OTP performed before this migration has no new proof record; re-verification or
a trustworthy historical verification record is required. Changing an email
requires new proof; old proof does not automatically transfer to the new address.

First-install password setup remains gated by the operator code. Social signup
cannot claim that installation or bypass the first-account setup state.

## Operation and extension

Schedule `a.Auth.PruneSocialFlows(ctx)` with Gantry jobs to remove expired
handshakes. Flows expire after ten minutes and are consumed atomically. Link
sessions are rechecked in the database; revocation during the provider roundtrip
prevents linking. Application sign-out revokes the app session, not the user's
Google or Apple login in their browser.

The initial identity screen supports connecting and viewing providers. Removing
identities/account deletion, provider revocation notifications, native mobile
SDK exchange and passkeys need their own policies and flows; they are not
implemented by this browser-login feature. Keep an existing login/recovery method
available. Future mechanisms should resolve a user and create the same Gantry
session rather than introducing a parallel session store.

Gantry Strict cannot infer whether arbitrary OAuth cryptography is correct.
Use the shared implementation and generated integration. Tests verify signed
provider fixtures, identity rules, CSRF/state/nonce, replay/concurrency, session
expiry and SQLite/PostgreSQL behavior. Live acceptance also requires configured
provider credentials and callback registration.

Protocol references: [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect),
[Apple web authorization](https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms),
[Apple token exchange](https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens).

## Button appearance

`SocialButtons(p)` includes the provider icons, default button styling and an
“Or with email” divider. `SocialConnections(p)` uses the same styling for account
settings, shows connected status and omits the divider. Both render nothing
when no providers are configured. New generated views use these shared components.

Defaults follow the providers' official web examples: Google's light rectangular
button with its multicolor logo, and Apple's black button with a white logo.
The small stylesheet is embedded, minified once by Gantry's asset pipeline and
included by the component, with no external script, font or image request. For
strict Content Security Policy, include `gantryauth.SocialStyles.Hash()` in
`style-src`. Icons are decorative SVGs; buttons retain text names and visible
keyboard focus.

Apps can set `--gantry-social-width`, `--gantry-social-height`,
`--gantry-social-radius`, `--gantry-social-gap`, `--gantry-social-focus`,
`--gantry-social-muted`, `--gantry-social-divider-space` and
`--gantry-social-divider-color` in their stylesheet. The `.gantry-social__button`
classes are stable styling hooks. To own the entire markup, iterate `Page.Social`
and preserve each POST action and `data-turbo="false"`.

Follow provider branding requirements when customizing logos, colors and labels:
- [Google branding and HTML button configurator](https://developers.google.com/identity/branding-guidelines).
  The Google SVG artwork is from its provided HTML example.
- [Apple button guidance](https://developer.apple.com/design/human-interface-guidelines/sign-in-with-apple).
  The Apple logo path is from [Apple's web button implementation](https://appleid.cdn-apple.com/appleauth/static/jsapi/appleid/1/en_US/appleid.auth.js).
