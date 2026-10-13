# Mail

Mail belongs to the application. Auth, invitations, receipts, notifications and
background jobs use the same `App.Mail`, default sender and delivery settings.
New Gantry apps wire this up automatically. Authentication does not configure its
own SMTP connection or inbox.

## Sending

```go
err := a.Mail.Send(ctx, mail.Message{
    To: "rider@example.com",
    Subject: "You're invited",
    Text: "Join us for a ride on Saturday.",
    HTML: "<p>Join us for a ride on Saturday.</p>", // optional alternative
})
```

`From` defaults to `MAIL_FROM`; a message can override it or supply `ReplyTo`.
Pass `a.Mail` into a controller or job as a `mail.Sender`. Tests can inject their
own capturing sender. Gantry Auth receives the same service:

```go
a.Auth = auth.New(a.DB, a.Signer, a.Mail, a.Mail.From, []byte(authCodeKey))
```

The code-signing key is an authentication setting; delivery is app-wide.

## Configuration

The generated `config.Config` owns a `mail.Config`, read with
`mail.LoadConfig(getenv)`. App startup opens one mailer using
`mail.Open(ctx, cfg.Env, cfg.DataDir, cfg.Mail, logger)` and closes it at shutdown.

| Setting | Meaning |
| --- | --- |
| `MAIL_DRIVER` | `inbox`, `smtp`, `disabled`, or summary-only `log`; blank chooses the environment default |
| `MAIL_FROM` | Default From address, including an optional display name |
| `SMTP_HOST` | The provider's SMTP host |
| `SMTP_PORT` | SMTP port, default 587 |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | Provider credentials |
| `MAIL_INBOX_PATH` | Optional local capture file; default `DATA_DIR/mail-inbox.sqlite3` |

With a blank driver, development captures mail even if SMTP settings exist.
Tests disable delivery. Other environments use SMTP when a host is configured;
otherwise sends return `mail.ErrDisabled`. An explicitly chosen driver overrides
that default, but `inbox` always requires development. SMTP requires a valid
`MAIL_FROM`; the inbox supplies `Gantry <no-reply@example.test>` when omitted.

New apps expose delivery settings in Compose. Store production provider
credentials in Houston secrets. `MAIL_INBOX_PATH`, when needed, can be added to
the app's environment mapping. Auth startup refuses disabled or summary-only
logging mailers when email-code login is enabled.

SMTP works with Resend and other providers; see the
[authentication delivery example](auth.md#delivery-and-development). Applications
can still inject a custom `mail.Sender` for a provider API or durable mail queue.

## Development inbox

`bin/gantry dev` serves the app's mailbox at `/dev/inbox`, for example
`http://rideclub.localhost/dev/inbox`. It captures **every message sent through
App.Mail**, with no external delivery. It is a development capture service, not
an incoming public SMTP server.

The inbox includes:

- A newest-first message list with recipients, subjects, previews and timestamps.
- Search across sender, recipient, subject and bodies.
- Full headers, plain text and isolated HTML previews; remote content and active
  HTML are blocked.
- Message downloads (`.eml`), individual deletion and clearing the inbox.
- A split view on desktop and separate list/message screens on phones.

The inbox checks for changes every two seconds while visible, including mail
captured by separate background processes. New messages appear without a page
reload, preserving the selected message, HTML preview and search. Checking pauses
in hidden tabs and resumes when you return; Refresh remains available manually.
Unchanged polls only read Gantry’s SQLite change counter and return no content.
The newest 200 messages persist
across app reloads in a separate SQLite file, even for Postgres apps. Messages up
to 2 MiB are accepted. Captures contain full email bodies, including development
login codes and reset links. That data belongs in the local development volume.
The constructor, capture sender and HTTP handler require
`GANTRY_ENV=development`; HTTP access also requires a localhost or loopback host.
Nothing mounts in production when using the generated app setup.

## Existing apps

Add `Mail mail.Config` to the app's configuration and load it with
`mail.LoadConfig(getenv)`. Open and retain one `*mail.Mailer` during startup, share
it with controllers, Auth and jobs, and close it on shutdown. Mount its inbox:

```go
if a.Mail != nil && a.Mail.Inbox != nil {
    a.Mail.Inbox.Routes(rt.Mount, "/dev/inbox")
}
```

No production schema migration is needed. The development inbox owns its separate
local store. Keep production application data and capture data separate.

Strict cannot reliably tell whether a custom sender is deliberate, a test fake,
or duplicated transport configuration. Review new email features for reuse of
`App.Mail`; exercise delivery with a capturing sender and verify the generated
mail defaults when changing startup configuration.
