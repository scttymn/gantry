# Apple and Google identities

Direction: one user can sign in through multiple identities (password, email
code, Google, Apple; passkeys later). Reuse shared Auth sessions. Providers are
opt-in and signup is an explicit application choice. Automatically link matching email
addresses when the provider establishes authoritative mailbox ownership and the
existing user has a verified email identity. Otherwise require an existing login
to confirm linking. Provider subject IDs remain stable when emails
change; Apple relay addresses must not be guessed or merged.

Use x/oauth2 and coreos/go-oidc, as specified in docs/plans/gantry.md. Verify
signature, issuer, audience, authorized party, expiry and nonce. Google uses
PKCE S256. Apple uses code flow with form_post and a short-lived ES256 client
secret signed from its configured private key. Provider keys are cached by the
OIDC library; no provider network calls during page rendering or startup.

A ten-minute database flow binds provider, nonce, PKCE verifier, browser cookie
and optional linking session. Atomic consumption blocks replay across processes.
An exact POST callback exemption handles Apple's cross-site form; the callback
checks its own state and browser proof with a Secure/SameSite=None cookie.
Other forms remain protected. Provider tokens and errors are never logged or
persisted. HTTP timeouts and generic errors bound failures.

Application-owned migrations add auth_identities (provider, subject, user_id)
and auth_oauth_states. Explicit linking requires a recent live session. New
accounts and identity records commit together, with email collision refusal and
an optional transactional profile hook. First-install password setup remains
closed to social signup until operator setup completes. Existing mechanisms
continue to use the user's same email credential and sessions.

Adoption: additive `gantry g auth --social` recipe for existing password or
email-code apps; shared buttons in new auth views; environment-based Google and
Apple config; identity management screen; docs for provider registration,
HTTPS callbacks, Apple relay email, key rotation and migration. No speculative
Strict rule for cryptographic correctness; framework reuse, tests and explicit
integration guide are the adoption path.

| Acceptance | Coverage |
| --- | --- |
| Google/Apple code roundtrip, existing sessions and signup | Social integration tests, SQLite/Postgres |
| Provider subject identity; email collision requires linking | Social account tests |
| Expired/missing/mismatched/replayed state or browser cookie | Social callback tests |
| Bad signatures/issuer/audience/azp/nonce/expiry/verification | Signed-token provider fixtures |
| Google PKCE and Apple ES256 secret/form_post | Provider request assertions |
| Fresh authenticated linking, no reassignment, rollback/races | Social linking/concurrency tests |
| Exact callback CSRF exception; all other routes protected | Web router and social tests |
| Missing credentials, pending first install, network failure | Configuration/failure tests |
| Both generators, migrations, build, Strict, docs | Recipe tests and golden fixtures |
| Phone/desktop presentation | Browser preview |

Primary references:
- https://developers.google.com/identity/openid-connect/openid-connect
- https://accounts.google.com/.well-known/openid-configuration
- https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms
- https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens
- https://appleid.apple.com/.well-known/openid-configuration
- https://github.com/coreos/go-oidc

Evidence (2026-10-12): full `go test ./...` and `go vet ./...` pass in
Gantry's Docker toolchain with PostgreSQL enabled. `go test -race ./auth ./web`
passes. Both database engines and password/email-code generated apps pass
migration, golden, Strict, compilation, vet and tests. The expanded race run
found an existing streaming-test log-read race, fixed separately on main.

Evidence (2026-10-15), rebuilt on main at v0.15.2: `go test ./...` with
PostgreSQL 17, `go vet ./...`, gofmt and `go test -race ./auth ./web` pass;
the social recipe's generated apps build, vet and test on both engines.

The isolated Houston preview at http://social-preview.localhost/login was
checked at desktop and 375px phone widths. Email-code signup, the captured
inbox, shared session and Sign-in methods screen work in the browser. Provider
buttons are rendered using preview-only credentials. Live Apple and Google
credentials are not available; signed local provider fixtures verify callback
behavior without contacting real user accounts. Nothing is published yet.
