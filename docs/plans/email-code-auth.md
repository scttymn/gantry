# Email-code authentication

Gantry Auth will provide the two-step email login demonstrated by Ride Club's
Phoenix prototype. Menu Manager keeps password authentication. Ride Club will
opt into creating accounts only after an email code has been verified.

## Design

- `Auth.EmailCodes *EmailCodes` selects passwordless email-code routes. Nil keeps
  existing password routes and behavior. Reuse session handling and mail.Sender.
- Six random decimal digits, ten-minute expiry, five attempts. Request limits:
  one email per minute, three per email per fifteen minutes, twenty per IP per
  fifteen minutes. Verification is additionally limited to fifty attempts per
  IP per fifteen minutes. An optional daily send ceiling protects provider usage.
- A random challenge travels in an HttpOnly cookie; only its digest is stored.
  Store an HMAC of challenge, normalized email and code using a separately
  configured secret of at least 32 bytes. Do not store or log plaintext codes.
- Database transactions make attempts, resend replacement and consumption safe
  across instances. SQLite and PostgreSQL must both pass the same tests.
- Every valid address receives the same email/response. Existing-account-only
  apps reject unknown addresses only after verifying mailbox ownership. This
  avoids existence-dependent SMTP timing at the request endpoint.
- Signup is disabled by default. With signup enabled, insert only after proof;
  an optional transactional callback supports app-specific required fields.
  New passwordless users have an empty, unusable password digest. No password
  reset routes are mounted in email-code mode.
- `gantry g auth --email-code [--signup]` generates thin integration, templ
  views, migrations and an integration test. Existing password recipe unchanged.
- Existing apps adopt the migration and config explicitly; no automatic schema
  changes. Pruning expired challenges/limit buckets is an exposed maintenance
  operation, documented for scheduled jobs.

## Validation

Test request -> verify -> session for existing and newly verified users; no
premature signup; disabled signup; exact expiry; wrong/replayed/replaced codes;
five-attempt cap including simultaneous attempts; simultaneous successful
verification; per-email and per-IP limits across Auth instances; mail failures;
transactional signup rollback; key rotation; configuration validation; no
password routes in code mode; existing password tests; SQLite/Postgres migration
up/down; generated-app build, integration tests, Strict and documentation.

Static heuristics cannot reliably distinguish every custom OTP implementation
from a legitimate use of hashing, randomness or mail. Document the shared API
and review requirement rather than add a noisy Strict rule.

Reference: Ride Club `website/lib/rideclub/accounts.ex` and
`website/lib/rideclub_web/controllers/auth_controller.ex`; Gantry's original
auth plan; OWASP Authentication, Forgot Password and MFA cheat sheets.

## App-wide mail and evidence

Mail configuration belongs to the application, not Auth. Generated apps now
open one Mailer for controllers, jobs and authentication, and automatically mount
the persistent development inbox at /dev/inbox. It retains complete messages of
any kind across reloads, with search, text/isolated HTML previews, downloads and
delete controls. Production uses SMTP or explicit disabled delivery.

Validated the framework and generated SQLite/Postgres apps with the complete Go
suite, vet, and race detection for auth and mail. Tests cover independent DB
connections, replay/expiry/limits, verified signup, durable concurrent mailbox
writes, retention, escaping, isolated HTML and development-only access. Generated
recipes pass Strict. The release test job now starts Postgres and the framework
toolchain includes pg_dump, so Postgres checks run there too.

Browser-checked the generated email/code forms, wrong code, verified signup and
sign-out via Houston at otp-preview.localhost. Checked the general inbox on
desktop and mobile, including search and HTML preview. The preview captures
local email only; live provider delivery and a Ride Club deployment are outside
this change. Menu Manager is unchanged.
