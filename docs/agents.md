# Building an app with Gantry: a guide for agents

You're an AI agent building or maintaining a Gantry application. This guide
covers the order of work, the framework patterns to use, and how to verify the
result. It is also a practical starting point for humans. For changes to Gantry
itself, read [CONTRIBUTING.md](../CONTRIBUTING.md).

Gantry is an opinionated Go framework that generates ordinary application code.
Use its existing capabilities before introducing an application-specific
replacement. [Gantry Strict](strict.md) helps detect departures from those
patterns; it covers a growing subset of them, not every performance concern.

## 1. Read the app before changing it

- Read the app's own instructions and README, then `go.mod`, `compose.yml`,
  `Dockerfile`, `app/routes.go`, and `app/shared/layout`.
- Check the pinned Gantry version and any `replace` directive or `go.work`.
  Use the APIs and generators that version provides. The documentation on
  `main` may describe features newer than the app's dependency.
- Inspect existing controllers, models, authentication and tests. Extend the
  established implementation rather than generating a second one alongside it.
- Check the working tree and preserve unrelated changes and existing data.

From an existing app's root:

```sh
bin/gantry --help
bin/gantry check --rules
bin/gantry check
```

Use the app's `bin/gantry`, which selects the CLI matching its Gantry dependency
and the pinned Houston CLI. Cloning a generated app requires Docker, a shell
and curl; the launcher downloads the tools it needs. A local framework checkout
uses the checkout's launcher when one is available. Do not silently replace the
app's pinned toolchain with a globally installed version.

For a new app, follow [Start an app](../README.md#start-an-app), choose SQLite or
Postgres, then use its generated `bin/gantry`. Strict is available from v0.13.0;
an older CLI may not have `check`.

## 2. Build on Gantry's defaults

| Need | Use | What to preserve |
| --- | --- | --- |
| Routes and middleware | `web.NewRouter`, scopes and pipelines in `app/routes.go` | Shared error handling, authentication filters and the standard HTTP middleware |
| Views | `.templ` views beside their resource's controller, rendered with `web.Render`, inside `app/shared/layout` | One page shell for navigation, metadata and assets |
| Styles and scripts | The app's `assets` package: `Styles.Tag`, `ImportMap.Tag`, `Path`, and `Routes` | Gantry's inline-or-linked styles, content fingerprints, compression and caching |
| Browser interactions | The generated Turbo and Stimulus setup; controllers in `assets/js/controllers` | Normal links and forms first, enhancements scoped to their own elements |
| Public page caching | `web.PageCache` for explicitly selected pages identical for every visitor | Data-version invalidation, bounded cache size and a suitable maximum age |
| Images and uploads | `assets.Images`, Gantry's image pipeline and `storage` as appropriate | Sized image variants, responsive markup and the existing precompile process |
| Data | The app's `db.DB`, migrations, model queries and sqlc configuration | Its selected database, read/write handles and transaction boundaries |
| Sign-in and API access | Existing auth wiring, or the `auth` / `api-tokens` recipes when missing; `auth --email-code [--signup]` for email codes | Framework password/session handling and server-side access checks |
| Email delivery | App-wide `App.Mail`, configured once with `mail.Config`; development inbox at `/dev/inbox` | Shared delivery, safe development capture and reusable testing |
| Background work and live updates | `jobs` and `live`, wired through the app | Existing queues, schedules and authorization for streams |

For email-code login, use the [shared Auth implementation](auth.md), configure a
shared [app mail service](mail.md) and an external code secret, and make
self-registration an explicit app choice. Do not copy an OTP implementation
from another app or add a shared development bypass.

For Apple and Google, use `gantry g auth --social` with existing shared Auth,
then follow [social login](social-login.md). Keep one user/session system. Enable
signup deliberately, never treat a matching unverified email as proof, and keep
Apple's cross-origin exception limited to its state-validated callback. Existing
verified email records need an explicit, trustworthy migration; do not backfill
verification from the presence of a password. Live provider testing requires app
credentials and registered callback URLs.

Keep scripts in `assets/js`, styles in `assets/css`, and shared page shells in
`app/shared`. Mount the generated asset routes. Avoid a parallel file server,
manual asset hashes or hand-maintained cache-busting query strings for these
files. An authenticated download route can have a separate purpose; document
that purpose if Strict flags it.

Page caching is an explicit application decision. Do not wrap account pages,
personalized responses or an entire authenticated router in a public page cache.
Its key is the path and query, not the user's identity. Use `db.DB.Version` for
database-driven invalidation where appropriate, and account for other data
sources and time-dependent content. A clean Strict scan does not check this for
you yet.

For editors, load the initial data in the GET controller and render it with the
page. Avoid an unnecessary HTML → JavaScript → data-fetch chain before the user
can interact. For initial JSON, use `templ.JSONScript` so user-provided text is
escaped correctly. Keep validation and authorization on the server. Scope
JavaScript with an explicit element ID or Stimulus controller; a page may have
both an editor form and a sign-out form. See the [editor example](development.md#editor-example-render-the-initial-state-and-scope-enhancements).

## 3. Generate, integrate and run locally

```sh
bin/gantry dev
```

Houston serves the app at the address it prints, normally
`http://<app-name>.localhost`, and the development container rebuilds on changes.
Use the existing development server when it is already running.

Use generators for a feature when they fit. Read `bin/gantry --help` and the
generator's printed next steps. Generated code still needs application-specific
wiring, validation and tests. The resource generator currently targets
`admin/<table>` and expects a shared admin controller; inspect those dependencies
before treating its output as a complete feature.

When the change needs migrations or regenerated queries/views, use the relevant
steps in the app's container:

```sh
bin/gantry db migrate
bin/gantry exec sqlc generate
bin/gantry exec templ generate
```

Follow the app's existing generation workflow and do not edit generated Go
instead of its `.sql` or `.templ` source. Keep the Dockerfile's asset precompile
step so tests and production use the optimized assets. Preserve the app's
SQLite/upload volumes and apply schema changes through migrations. Development
fixtures belong to the app; do not introduce a shared default production login.

## 4. Use Strict throughout the work

```sh
bin/gantry check --rules   # the installed rule catalog and guidance
bin/gantry check           # violations and advisory findings
bin/gantry check --strict  # nonzero for violations or a failed scan
```

`bin/gantry dev` runs checks before starting Houston; findings do not block the
server. For each finding, inspect the named file and use the recommended Gantry
pattern when it applies. Review advisory findings even though they do not fail
strict mode. Do not hide findings by disabling the panel or moving code beyond
the scanner's reach.

With Gantry's router, `GANTRY_ENV=development` automatically enables the browser
panel and request timing. Do not add a panel component to the app's views or
install duplicate middleware. The panel is full-width at the bottom, starts
collapsed, and makes room above it when opened. Findings are project-wide and
are rescanned on HTML page requests, so reload after a fix. It appears only when
findings exist and the response supports injection.

If the panel interferes with development, set `GANTRY_DEV_WARNINGS=false` in the
app environment and restart. That disables the panel and its scan; CLI checks
remain available. Production and test environments do not enable diagnostics.
A custom HTTP stack outside Gantry's router needs explicit development-only
integration; see [Browser warnings](development.md#browser-warnings).

For a justified alternative, use an exact file/rule exception with a specific
reason in `.gantry-check.json`. Explain why the framework pattern does not apply
and how the intended behavior is preserved. Do not add blanket exceptions just
to get a clean result. The [development guide](development.md) defines the six
current checks, their severities and exception syntax. Use `check --rules` to
discover additions after an upgrade. [Strict's current coverage](strict.md#current-coverage)
also identifies patterns that still require human or agent review.

## 5. Verify behavior and performance

Before handing over or deploying the change:

```sh
bin/gantry check --strict
bin/gantry test
```

`check --strict` and the test suite are separate checks; run both in CI as well.
Report remaining advice and justified exceptions rather than equating a zero
exit status with no findings. Use the app's existing tests and Gantry's `testkit`
helpers where relevant: `Links`/`Crawl` for page assets and navigation,
`Migrations` for schema changes, and test databases for persistence.

Exercise the affected browser flows at the relevant desktop/mobile sizes.
For navigation or form changes, check sign-out and validation on each affected
page. For asset changes, verify referenced files load and changed content has a
new fingerprinted URL. For performance work, compare a first visit with a warm
navigation and inspect the request waterfall. Development's `Server-Timing`
measures time until response headers; it does not measure browser rendering.

Strict does not currently establish page-cache eligibility, image-pipeline
adoption or the absence of blocking data fetches. A clean scan is useful evidence
of convention adherence, not proof that the app is fast or secure.

## 6. Deploy through Houston

Gantry delegates deployment to Houston. Follow
[Houston's app setup guide for agents](https://github.com/scttymn/houston/blob/main/docs/agents.md)
for server connection, linking, secrets, persistent storage and deployment.
Check the configured server and project with `bin/gantry status`; do not invent
a server address or put credentials in app source or output.

Within the user's deployment authorization, use the existing push-to-deploy
workflow or `bin/gantry deploy --server --follow`. Confirm the intended commit
is serving and check the affected live pages before reporting success. Creating
a local development app does not itself require creating a production project.

### First-install password accounts

The password generator now uses shared Gantry Auth with a one-time setup wizard
at `/login`. Retrieve the code only through the generated `auth:setup-code`
operator task in a trusted terminal. Never write code output to deployment logs,
URLs, source, or chat. Existing installations must adopt the setup migration
with `completed=true`; zero users does not mean a new installation. See
[Authentication](auth.md#password-login-and-first-install-setup) for hooks,
recovery, and migration from the older app-owned password implementation.
