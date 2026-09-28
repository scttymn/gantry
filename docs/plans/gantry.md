# Plan: gantry, a Go web framework

## Your direction
- "I want to start on a web framework since I have other apps I want to build. It can be simple and opinionated."
- "Focus: speed, small size, a place for everything (conventions like rails, but obviously a proper golang citizen)."
- "I don't really want to re-invent anything but rather pull together existing things that make it fully featured with some opinions on construction and layout."
- "A new dev coming into the app should know exactly where model/view/controller/services are."
- "I don't want to re-invent login for every single app": OTP, email and password, OAuth (Google, Apple, etc.). On OTP: "Both and passkeys."
- "We need a way to work directly with postgres and sqlite3."
- "It should be fast, and support lots of users on minimal hardware."
- "valleybuiltcrossfit … was a POC to see if it could be done in golang and if there were a lot of savings. I went from a 490MB RAM usage rails app to an 8MB RAM usage golang app."
- "We could rebuild valleybuiltcrossfit in the gantry repo as the example. Host it with houston."
- "You should do this in a docker container too. Use houston. Then we can spin it up locally … so I can follow progress in a browser too."
- On IDs: "int64 default is fine with the option of UUIDv7."
- On performance: "I would like … to be able to configure default image compression in gantry … no matter what the image is, we can compress it. In the valleybuiltcrossfit site, I currently convert things to webp 80%. But both should be configurable." "It would be nice if it automatically served up mobile, tablet, or desktop images based on the size too." "I would appreciate pushback or suggestions on how we can be blazing fast no matter the app." "What if we just did webp by default for simplicity with caching? It can be written in a way that is extensible: image pipeline adapters?"
- On the admin's shape: "Folders, via generator": a folder per table, Programs-style code written by hand once, `gantry g resource` extracted from it, and the generator writing the rest.
- On stylesheets: "In-line CSS just seems to go against good practices." "If CSS was in separate files and added to the page as part of the compilation process, that would probably be OK."
- On the example: "Before we commit to the local gantry repo, I want to get rid of the example project. Instead, I would like to update the existing ValleybuiltCrossFit website. Although we need to make sure to preserve data." "Is it something that I could install on the system and then run 'gantry new'? … Then we can basically use gantry on a real site instead of something we're just gonna throw away. It's already proven to me that it's good enough for this website." On making gantry a public repo: "Yep!"
- On the layout: "We don't have to follow rails idioms to the letter." "What helps us write the least amount of code? What structure plays well with goLang." "I just prefer not having everything in one folder because it makes organization a pain in the butt." "The agent's gonna be writing all of this code."

## Goal
- **A framework for your next apps:** Rails' conventions, as idiomatic Go. It's a library plus a CLI that generates plain Go code.
- **Sign-in solved once:** email and password, emailed codes, TOTP, passkeys and OAuth, working in any app that mounts it.
- **Postgres or SQLite:** each app chooses one, and everything gantry ships works with both.
- **The POC's footprint as the ceiling:** a gantry app is no heavier than the hand-written valleybuiltcrossfit, measured the same way.
- **Installed like Rails:** `go install github.com/scttymn/gantry/cmd/gantry@latest` (later, released binaries), `gantry new`, and apps that pin a tagged gantry in `go.mod`. gantry is a public repo, so every build fetches it through Go's module proxy with no credentials.
- **The proof:** valleybuiltcrossfit, the live site, moved onto gantry in its own repo and deployed with Houston.

## Principles
- **Glue, not reinvention.** Every piece is a maintained library. gantry adds the layout, the defaults and the wiring. (Buffalo, the closest Go attempt at Rails, owned too much of its stack and was archived in 2024.)
- **Code generation, not runtime magic.** No reflection in a request's hot path, no struct-tag ORM, no global registry. What the generator writes is code you'd have written, and you can edit it.
- **Extract only what the site repeats** (the POC plan's rule). Nothing is built for a user we don't have. The example app is the first user.
- **Measured, not hoped for.** Every batch ends with the numbers.

## Decisions
| Need | Choice | Why |
|---|---|---|
| Routing | `net/http` ServeMux, with a `Resources` helper for the seven REST routes | Methods and path values are built in; no dependency |
| Handlers | `func(w, r) error` | One place turns errors into pages: `sql.ErrNoRows` → 404, validation errors → 422 |
| Views | templ, with htmx for server round trips | Compiled and type-safe, so a wrong field is a compile error; no Node |
| Postgres | pgx/v5 through `database/sql` (`pgx/stdlib`) | The fastest driver, behind the same interface SQLite uses |
| SQLite | modernc.org/sqlite | Pure Go, so the static binary runs on `scratch` |
| IDs | `int64` by default; UUIDv7 per table when asked (`gantry g resource post --uuid`) | `int64` is the least code, the smallest index, and SQLite's own rowid. UUIDv7 is for rows whose URLs shouldn't reveal a count: made in Go (google/uuid) so both engines behave alike, stored as `uuid` on Postgres and a 16-byte blob on SQLite |
| Queries | sqlc, with `sql_package: database/sql` for both engines | You write SQL and get typed Go, with generated code the same shape on either engine |
| Migrations | goose, with migrations embedded | Works on both; the framework's migrations and the app's each have their own version table |
| Sessions | a `sessions` table owned by `auth` (Rails 8's design), with an opaque random token whose hash is stored | "Sign out everywhere" needs sessions by user, which a generic store (scs) doesn't give |
| Flash, return-to | signed cookies, by gantry's `sign` (the POC's purpose-bound HMAC signer) | No server state for one-page messages; 60 tested lines, instead of gorilla/securecookie |
| CSRF | `http.CrossOriginProtection` (Go 1.25+) | Standard library; no tokens in forms |
| Passwords | bcrypt, with rate limits on login | argon2id takes about 64 MB per hash, which breaks the memory ceiling on small hosts |
| TOTP | pquerna/otp | Standard, and small |
| Passkeys | go-webauthn/webauthn | The maintained Go WebAuthn library |
| OAuth | x/oauth2, with coreos/go-oidc for OIDC providers (Google, Apple, Microsoft); plain OAuth2 for GitHub | Two focused libraries instead of goth's 60 providers and gorilla sessions |
| Apple's client secret | golang-jwt/jwt v5 (an ES256 JWT) | Apple requires one |
| Mail | wneessen/go-mail; development logs mail to the console | SMTP, maintained |
| Forms | go-playground/form to decode; validation is plain Go methods (`Validate() web.Errors`) | Reflection only when a form is submitted; rules are code you can read |
| Assets | embedded files served under digested names: the POC's `assets`, made a library (CSS `url()` rewriting, tdewolff/minify, gzipped once at startup) | Immutable caching with no build step. hashfs does only the naming, so it isn't used |
| Config | environment variables into a typed struct (caarlos0/env) | Houston and compose already speak environment variables |
| Logging | log/slog | Standard library |
| CLI | cobra | Houston already uses it |
| Tests | `testing` and httptest, goquery for HTML, fixtures in YAML | The POC's toolkit, generalised |

## An app's layout
The layout is what `gantry new` writes and what every generator follows.
```
myapp/
  cmd/myapp/main.go       # small: config, then app.Run
  config/config.go        # the environment → Config
  app/
    routes.go             # every route, nested ones included; builds each controller
    models/               # package models: every table's type, its queries and its methods
      posts.sql           #   the queries (sqlc input)
      posts.sql.go        #   generated by sqlc, never edited
      posts.go            #   the model: methods on sqlc's Post (Validate, derived values)
      posts_test.go
    posts/                # package posts: everything served under /posts
      controller.go       #   type Controller struct{ what it uses }; Index, Show, New, Create, Edit, Update, Delete
      index.templ  show.templ  form.templ
      controller_test.go
      comments/           # package comments: /posts/{post}/comments, the same shape
    admin/                # a namespace is a folder, as in Rails
      programs/  staff/
    services/             # work spanning models or talking to other systems: a package each
      photos/             #   package photos: the store, resizing
      schedule/           #   package schedule: PushPress, kept warm
    mailers/              # package mailers: one file per mailer, its templates beside it
    jobs/                 # background work (later)
    shared/
      layout/             # package layout: page shells
      ui/                 # package ui: buttons, form fields, flash, pagination
  db/
    migrations/           # goose: 00001_create_posts.sql
    seeds.go
  assets/                 # css/, js/, images/, embedded
  test/fixtures/          # YAML, one file per table
  sqlc.yaml               # queries: app/models/*.sql, generated into app/models
  Dockerfile              # dev, test and production stages (Houston's)
  compose.yml             # x-houston included
```
- **Models share one package.** They refer to each other constantly (a post's author, a user's posts), and within one package that can never be an import cycle. sqlc writes into the same package (it lets its output files be named, and never removes other files), so methods go straight on its generated types, with no wrapper.
- **A resource's folder is its controller and its views,** and it mirrors the URL. Views share the controller's package, so they use its unexported helpers, and every resource has the same names (`Controller.Index`, `index.templ`). An agent copies that pattern reliably, where one flat folder would need a prefix on everything.
- **A page that shows another resource's part imports that resource.** The home page draws the schedule's week and the lead form, so `home` imports `schedule` and `leads`; they never import `home`. A nested resource loads its parent through `models`, not by importing it. A partial used by several resources moves to `shared/ui`.
- **A service is a package of its own** (`services/photos`, `services/schedule`), because a service has an API: types and functions others call. One `services` package would make their names collide (every service has a `Store` or a `Week`) and need a prefix on each.
- **Imports go one way:** `routes.go` → resources → services → models, and anything → `shared`. Go forbids import cycles, so the compiler enforces the layering.
- **Controllers name their dependencies** (`Controller{Q *models.Queries; Mail mail.Sender}`), and `routes.go` builds them, so a test can pass fakes. The generator writes these few lines.
- **Views never touch the database.** A controller loads what the page needs and passes plain values to a component.
- **Tests sit beside the code,** as Go does it.
- **Why not the alternatives:**
  - One flat `app` package is the least code, but you said a single folder makes organisation a pain.
  - Rails' `controllers/` and `views/` folders spread a feature across the tree without saving any code.
  - A full package per resource (model included) needs wrapper types around sqlc's output and has import cycles wherever models relate.

## The repo
```
gantry/
  go.mod                  # github.com/scttymn/gantry
  cmd/gantry/             # the CLI: new, g (resource, model, controller, migration, auth:views), db (migrate, rollback, status)
  db/                     # Open(url): pools, pragmas, migrations, transactions
  web/                    # handler type, Resources, render, errors, flash, method override, middleware, compress
  auth/                   # every sign-in method, sessions, routes, default views
  mail/
  assets/
  testkit/                # a database per test, fixtures, request helpers
  internal/skeleton/      # the files `gantry new` and the generators write
  docs/plans/
```

## Databases
- **The URL chooses the engine:** `DATABASE_URL=sqlite:///data/app.sqlite3` or `postgres://…`.
- **SQLite is set up for concurrency:**
  - WAL, `busy_timeout`, `synchronous=NORMAL` and foreign keys on.
  - Two pools: one connection that writes (`_txlock=immediate`), and a read pool of `GOMAXPROCS` connections.
  - `db.Write` and `db.Read` name which pool a query goes to. On Postgres both are the same pool.
- **Postgres:** a `database/sql` pool through pgx, with limits sized for small hosts.
- **The framework's own SQL** (auth) is written for each dialect wherever the two differ, and its tests run against both. The test stage has a Postgres service.
- **An app's SQL is written for the engine it chose.** `gantry new --db=sqlite|postgres` writes sqlc.yaml for that engine.

## Auth
`auth` is mounted like Devise: `auth.Mount(mux, auth.Config{Methods: ...})`. Each method is switched on in config. Its views are defaults that `gantry g auth:views` copies into the app for editing.

**Tables** (its own migrations, in its own version table):
- `users`: email, `email_verified_at`, `password_digest` (nullable: passwordless accounts), timestamps.
- `sessions`: token hash, `user_id`, IP, user agent, `last_seen_at`. Changing the password ends every other session.
- `identities`: `user_id`, provider, subject, and the email the provider gave (OAuth).
- `passkeys`: `user_id`, credential ID, the credential as webauthn stores it, a name, `last_used_at`.
- `one_time_codes`: email, purpose (`sign_in`, `verify_email`), code hash, expiry, attempts.
- `totp`: `user_id`, the secret encrypted with a key derived from `SECRET_KEY`, and hashed recovery codes.

**Methods:**
- **Email and password:** sign-up, sign-in, reset by an emailed link (signed and stateless, dead once the password changes, as in the POC), and email verification.
- **Emailed codes:** a 6-digit code, valid for 10 minutes and good for 5 tries, then rate-limited by email and by IP.
- **TOTP:** an optional second factor after a password or code. Enrolment shows a QR code and gives recovery codes.
- **Passkeys:** register one from account settings; sign in with a discoverable credential (no email typed).
- **OAuth:** Google, Apple, Microsoft and GitHub.
  - An identity joins an existing account only when the provider says its email is verified.
  - Apple posts its callback from another site. That path is exempted from cross-origin protection, and `state` and nonce checks protect it instead.
  - Apple sends the user's name only on the first sign-in, so it's saved then.
- **In every case:** constant-time comparisons, the same response whether or not an account exists, and rate limits on every endpoint that sends mail or checks a secret.
- **For the app:** `auth.Current(r)` gives the user, and `auth.Require` is the middleware.

## The site: valleybuiltcrossfit on gantry
- **Where:** its own repo (`github.com/scttymn/valleybuiltcrossfit`), on the branch `gantry`, in the app layout above. `go.mod` requires a tagged gantry. Until 2026-09-28 it was developed here as `examples/valleybuiltcrossfit`; the batch records and Evidence below keep that name.
- **Pushes to its `main` deploy** (its `x-houston`), so the move stays on the branch until the switch-over, on your say-so.
- **The branch can't deploy before the admin is ported.** The live site has an admin (copy, photos, programs, staff, theme) and sign-in, which the gym uses. So the switch waits for batches 2 and 3 on this branch.
- **Houston, everywhere:** developed, tested and measured in Docker through Houston (`houston dev`, `houston test`, `houston dev --production`), with nothing installed on the host. On the `gantry` branch, `houston dev` serves it at http://gantry.valleybuiltcrossfit.localhost, a branch instance with its own copy of the dev data (`valleybuiltcrossfit-gantry_data`); main's stays as it was.
- **Working on gantry and the site together:** each deploy builds against the gantry its `go.mod` pins (`v0.1.0` now). When a change spans both, a `go.work` (ignored by git) points the site at `../gantry`, and the dev container needs `../gantry` mounted beside it: set up the first time it's needed, then a new gantry tag and `go get` before the site's change is committed.
- **The data, preserved:**
  - Before the first deploy, the migrations run on a copy of the live database, with row counts and `PRAGMA integrity_check` compared against the original.
  - At the switch, Houston snapshots before the deploy (and `houston restore` rolls code and data back together). The maintenance page stays up for the switch, because the new version migrates when it starts while the old may still be serving, and the old code writes NULLs the new schema refuses.
  - Locally, the dev volume `valleybuiltcrossfit_data` was migrated by the example on 2026-09-27; the untouched copy is `valleybuiltcrossfit-poc_data`.
- **The spec:** the POC's behaviour and its tests (ported from Rails). The admin's port reads the POC from `main` (`git show main:internal/web/admin.go`), and `docs/rails/` describes the Rails app's.

## Batches
1. **Skeleton.** gantry's `db` (both engines, the two SQLite pools, goose), `web` (handler type, render, errors, middleware, compress) and `assets`. The example serves the public home page from a copy of the live data, in the app layout, under `houston dev`. The baseline and the first measurements are recorded.
2. **Auth core.** Email and password, sessions, reset, rate limits, and the default views. The site's admin sign-in runs on it, with the users migration.
3. **Resources.** The site's admin CRUD becomes the conventions: `Resources`, forms, validation, flash. Then `gantry g resource` is extracted from what the example actually does.
4. **Codes, TOTP, passkeys.**
5. **OAuth:** Google and Apple first (the site's admin gets "Sign in with Google"), then Microsoft and GitHub.
6. **`gantry new` and Postgres parity.** A generated app on each engine passes its generated tests; the framework's tests run against both.
7. **Switch over.** The full comparison with the POC runs on the same data. Then, on your say-so, the live project moves to the gantry repo.

Each batch gets its full map (contract pin, tests, evidence) before its code, as in the POC's plan.

## Batch 1: the skeleton and the public site (full map)
**The slice:** `houston dev` in `examples/valleybuiltcrossfit` serves the POC's public site at http://valleybuiltcrossfit.localhost, from the local data. That's everything a visitor reaches: `/`, the `/schedule` fragment, `POST /leads` with its hand-off, `/photos/…`, `/favicon.svg`, `/manifest.json`, the error pages, `/up` and the assets. The admin and sign-in come in batches 2 and 3; until then `/admin` and `/login` are 404.

**gantry packages in this batch:**
- `db`: `Open(url)`, SQLite's two pools and pragmas, Postgres through pgx, and goose migrations with the version table named by the caller (gantry's own tables and the app's are kept apart). Postgres is covered by tests that run when `GANTRY_TEST_POSTGRES_URL` is set; the Postgres service joins the test stage in batch 6.
- `web`:
  - the handler type `func(w, r) error` and its error handling (`sql.ErrNoRows` → 404, `web.Status(422, …)`)
  - render (templ)
  - `/up`
  - middleware: security headers and cross-origin protection, method override with the body caps, recover, compress (the POC's `kit/compress`, with klauspost/compress's gzip)
  - `ClientIP` and rate limits (the POC's fixed windows)
- `assets`: the POC's digested assets, made a library (an app passes its `embed.FS`). It does what hashfs doesn't: CSS `url()` rewriting, minification, gzip once at startup. So hashfs is dropped from the decisions.
- `sign`: the POC's HMAC signer (purpose-bound), keyed by `SECRET_KEY` or by a key generated once and kept in the database. It stays in place of gorilla/securecookie: it's 60 lines, already tested, and nothing it lacks is needed.
- `mail`: `Message`, `Sender`, SMTP through wneessen/go-mail, and a log sender for development.
- `testkit`: a migrated database per test, and YAML fixtures.

**The toolchain:** a `toolchain` Dockerfile stage with Go 1.27, templ v0.3.1020 and sqlc 1.31.1 (copied from `sqlc/sqlc`). `bin/go` runs it on the repo. Generated code (templ, sqlc) is committed, as Go does: a library's must be, for `go get`. Go's caches are in `.cache/`: templ's watcher skips folders starting with `.` or `_`, so the dev server watches the whole repo (the example and gantry) without walking thousands of cached modules.

**The example:** its own `go.mod`, with `replace github.com/scttymn/gantry => ../..`, laid out as "An app's layout" says. Models are sqlc queries over the POC's schema, made plain by one migration (below). Nothing the POC does changes.

**The schema, made plain** (`00002_plain_columns.sql`): every table the POC kept is rebuilt once, in place, keeping its data, ids and counters.
- Text columns become `NOT NULL DEFAULT ''`. Rails left nearly all of them nullable, so sqlc would have made each one `sql.NullString`, and every view would read `.String`. Nothing here told NULL from blank (the POC read NULL as ""). NULL stays where it means something: an unset position, no class capacity, not yet synced.
- `datetime(6)` becomes `datetime`. The driver (modernc) reads a column as a time only when it's declared exactly `DATE`, `DATETIME` or `TIMESTAMP`, and Rails' declared precision made every timestamp arrive as text. The POC never noticed because it scanned timestamps as strings.

**Contract:**
- The POC's public tests pass, each ported under its own name (home, layout, sections, schedule, leads, photos, errors, icons, compression, assets, theme, seeds).
- **Pages match:** the POC and the example, both on a copy of the same data, give the same HTML for `/`, `/schedule?week=1`, a 404 and `/manifest.json`, apart from signed tokens and digests.
- **Measured** as the baseline was, and recorded in Evidence. The targets are no worse than the POC.

**Data:** before the example first runs, `valleybuiltcrossfit_data` is copied to `valleybuiltcrossfit-poc_data`. The example's migrations only add gantry's version table.

## Batch 1b: caching and images (full map)
**Why:** at half a CPU, gantry's own overhead is negligible (`/up` does 16,000 req/s), while the home page does 120–160 req/s. That's the page's own cost: 100 KB of HTML, seven queries and a render, gzipped on every request. The biggest win for any app is not doing work that's the same for every visitor.

**1. Page cache** (`web.PageCache`, `Router.Cached`):
- A cached route's 200 response is kept in memory, with its body as written and gzipped once, plus an ETag. Other statuses and responses that set a cookie are never kept.
- **Invalidation is automatic on SQLite.** `db.DB.Version` reads `PRAGMA data_version` on a connection of its own. It changes whenever another connection commits, so any write (an admin edit, a schedule refresh) drops every cached page, with no code in the app.
- **Postgres has no equivalent.** There the cache relies on its max age alone until gantry has a version source (LISTEN/NOTIFY, or an explicit bump).
- **A max age (default one minute)** covers what changes with time rather than data: "today" on the schedule, a class in progress, the year in the footer.
- Concurrent misses on the same page render it once. Entries are capped (default 1,000), so query strings can't grow the cache without limit.
- **A page must be the same for every visitor.** Anything per-visitor lives in a fragment that isn't cached. The example's form token isn't per-visitor: it's a signed time, and a page at most a minute old keeps it within the form's rules. So it stays in the page.
- Browsers get `Cache-Control: no-cache` plus the ETag: they check back each time and get a 304 when nothing changed.

**2. `gantry/images`**, the POC's photo pipeline made general:
- **WebP at quality 80 by default**, and both are configurable. Encoders are adapters (`ContentType`, `Ext`, `Encode(w, img, quality)`); gantry ships WebP and JPEG.
- **An adapter that needs cgo lives in its own module** (a native AVIF encoder, say), so apps that don't use it keep the static binary. AVIF in pure Go took 24–41 s and up to 680 MB for one photo at half a CPU (Evidence), so it isn't a default.
- **Decoders are adapters too.** HEIC, which iPhones upload, is the first.
- **Named presets give each slot its widths and `sizes` hint.** The page lists the widths (`srcset`) and the browser picks the one that fits its screen and pixel density. The server never guesses the device: guessing gets tablets and dense screens wrong, and varying by user agent defeats caching.
- The component sets width and height (no layout shift), lazy-loads everything but the first-screen photo, and shows a blurred placeholder.
- **URLs carry the width, quality and format** (`/images/<key>/1400w-q80.webp`), so a changed setting is a new URL, and every copy is cached forever by browsers and Cloudflare.
- **Copies are made in the background when a photo is uploaded**, in a short-lived child process that keeps the encoder's memory out of the server. A request for a copy that doesn't exist yet makes it once, however many people ask for it at the same time.
- The example's photo service moves onto it, keeping its URLs and its admin quality setting.

**3. Defaults for new apps:** stylesheets linked with fingerprinted names and cached forever, not inlined, and precompressed static files. The example keeps its inlined CSS: it's a one-page site, and inlining is what paints its first screen soonest.

**Contract:**
- The example's tests all pass, and its pages match the POC's as before.
- The home page's throughput is measured before and after, as in Evidence.
- The cache is tested in gantry: a hit doesn't call the handler; a write, the max age, a non-200 or a cookie each keep a response out; gzip and plain, ETag and 304, HEAD, concurrent misses, and the cap on entries.

## Batch 2: sign-in (full map)
**The slice:** the site's admin sign-in, password reset and admin gate run on gantry's `auth`, on the `gantry` branch of valleybuiltcrossfit, behind the admin's layout and a first admin page (the dashboard). The rest of the admin is batch 3.

**Who owns the tables: the app, as Rails 8 does.** Rails 8's `generate authentication` writes the migrations, models and controllers into the app, and the app then owns its `users` table (and adds its own columns: a name, a role). gantry does the same: `auth` works on a minimal schema it documents, and the app's own migrations create it (later written by `gantry g auth`). The alternative, gantry running its own migrations, would collide with any app that already has a `users` table (this one does, from Rails), and would force gantry's shape onto columns apps want to own.
- `users`: `id`, `email_address` (unique, stored lowercased), `password_digest`, `created_at`, `updated_at`. Rails 8's own shape, so a Rails app's users carry over as they are, bcrypt hashes included.
- `sessions`: `id`, `user_id`, `token_digest` (unique), `ip_address`, `user_agent`, `created_at`, `last_seen_at`.

**`gantry/auth`:**
- **Sessions:** the cookie holds a random 32-byte token; the table holds its SHA-256. A copy of the database gives nobody a usable session, and ending a session is deleting its row. (The POC signed the session's id instead: anyone with the signing key could forge any session.) The cookie is HttpOnly, SameSite=Lax, Secure except on a local host, and lasts until sign-out (Rails 8's permanent cookie). `last_seen_at` is written at most every hour.
- **Passwords:** bcrypt, cost 12. An unknown email costs the same time as a wrong password. Changing a password ends every session. The rules are the app's (`Rules`); gantry's default is 8 to 72 bytes and a matching confirmation.
- **Reset:** an emailed link carrying a signed token of the user, an expiry (15 minutes) and a fingerprint of the password hash, so a used link dies. The same answer whether or not the address has an account. Links use the app's configured address, never the request's Host.
- **Rate limits** on sign-in and reset (10 in 3 minutes per address, the POC's).
- **`Require` and `Current`:** a wrapper for routes that need a user, which sends others to sign in and brings them back after (a signed return address, on-site only), and the user of a request. Only the routes that need it look a session up: the public pages stay cached and never touch the sessions table.
- **Routes and views:** `Routes` mounts sign-in, sign-out and reset at paths the app can change (defaults: `/login`, `/logout`, `/passwords/…`, the POC's). The views are the app's to give (`Views`); gantry's defaults are plain HTML forms.
- **`web.Flash`:** a one-page message in a signed cookie (the POC's), in `web`, since the admin will use it everywhere.

**The site:**
- A migration turns the Rails `sessions` table into gantry's shape. Its rows are dropped: their cookies were signed with a key the new version doesn't have, so admins sign in once more, as planned. `users` already has the shape.
- The admin's layout and its sign-in, forgot-password and new-password views move over from the POC as they are, and `/admin` shows the dashboard.
- The POC's auth tests are ported under their names. One changes meaning: "the session cookie is signed…" becomes the cookie holding a random token.

**Developing gantry and the site together:** `go.work` (ignored by git and Docker) uses `../gantry`, and `bin/go` and the dev container mount it at `/gantry`, which is `../gantry` from `/app`. Deploys build against the tagged gantry: `v0.2.0` when this batch is done.

## Batch 3: the admin (full map)
**The slice:** everything the POC's admin does, on the `gantry` branch: the seven content tables (pillars, programs, steps, membership options, staff, FAQs, workouts), the site's copy (six sections and the announcement), the settings (theme with its preview, photos, PushPress), inquiries, and admins. The POC's admin tests (`admin_test.go`, `admin_more_test.go`, `admin_theme_test.go`, and the site rules left out in batch 1) pass under their names. With this, the branch can replace the live site.

**The shape (your choice: folders, via generator):**
- `app/admin` keeps the admin's shared parts, ported from the POC's templates so their markup (and its tests) carries over: the layout, form fields (`Field`, `RecordForm`, `Errors`), the list table (`ResourceIndex`) and the delete button.
- Each table is a folder, `app/admin/<table>/`: `controller.go` (its handlers, and the form's values as typed), `index.templ` and `form.templ` built from the shared parts.
- Its queries and rules sit beside its model: `app/models/<table>.sql` (get, create, update, delete, the next position) and `<table>.go` (`Errors()`, the Rails model's validations).
- The special pages (site sections, announcement, settings, inquiries, admins) are ordinary controllers in folders of their own.

**gantry gains:**
- `web.Resources(rt, "/admin/pillars", controller, wrap)`: the seven REST routes, each from the method the controller has (`Index`, `New`, `Create`, `Show`, `Edit`, `Update`, `Delete`), `Update` on both PATCH and PUT.
- `web.Sent(r, "pillar")`: the fields a form sent, as `pillar[title]` (form and multipart alike); a field it didn't send isn't there, as Rails' permitted params. `web.Upload(r, name)`: a file field's file, if one was chosen.
- `cmd/gantry` with `g resource`: `gantry g resource admin/pillars title:string body:text position:position` writes the model's SQL and rules, the admin folder, and a test, and prints the route to add. Written from Pillars by hand, then run for the other six, each finished by hand (photos, choices, hints, the Rails validations), as a scaffold is.

**Order:** gantry's pieces and the shared admin parts; Pillars by hand; the generator; the other six generated and finished; then the special pages and the tests, in parallel. gantry `v0.3.0` at the end.

## Open questions (decide when their batch starts)
- **Jobs:** River is Postgres-only. An SQLite app needs another queue, or gantry's own small one. Nothing needs jobs until the example's lead hand-off (the POC's is a ticker).
- **File uploads and storage:** the site's photos need them. Decide whether it's a gantry package or stays the example's service.

## Evidence
Until 2026-09-28 the site was developed in this repo as `examples/valleybuiltcrossfit` ("the example" below). It's now its own repo.

### Baseline: the POC (valleybuiltcrossfit at e403bc6), 2026-09-27
Measured by running its production image in Docker, at its compose limits (0.5 CPU, 384 MB), on a copy of the local dev data volume. The load generator ran in its own container on the same Docker network, with 32 workers for 20 s, sending `Accept-Encoding: gzip` as browsers do.

| Measure | Result |
|---|---|
| Image | 10.2 MB |
| Memory, idle after 200 requests to `/` | 12.5 MiB |
| `/up` | 16,053 req/s, p50 0.5 ms, p99 67 ms; peak 28 MiB |
| `/` (100 KB of HTML, 26 KB gzipped) | 159 req/s, p50 187 ms, p99 713 ms; peak 53 MiB |
| `/` without gzip | 224 req/s |
| Memory, idle after the load | 46 MiB (Go keeps the heap it grew; it returns it over minutes) |
| Start to `/up` | ≤ 1.2 s, too coarse to use: timed from outside, with a container per probe. Next time it's taken from the app's own log. |

Found while measuring:
- **The POC's `script/measure` fails on a volume the dev container made.** The files are root's, and production runs as 65532, so the database is read-only. gantry's measurement changes the copy's owner.
- **gzip is about a third of the home page's CPU** (224 vs 159 req/s). Candidates: klauspost/compress's faster gzip, and caching pages that are the same for every visitor.
- **An admin list** wasn't measured: it needs a signed-in session. It's added once auth is gantry's (batch 2).

### Batch 1: the example on gantry, 2026-09-27
Measured the baseline's way. Machine load moves throughput between sessions (the POC gave 159 req/s on `/` earlier and 118–124 now), so each comparison is the two apps in the same session.

| Measure | POC | gantry example |
|---|---|---|
| Image | 10.2 MB | 11.4 MB (pgx, goose, go-mail) |
| Memory, idle after 200 requests to `/`, restarted on migrated data | 16.9 MiB | 19.5 MiB |
| `/`, two runs of 15 s | 124, 118 req/s; p99 ≈ 0.9–1.0 s | 124, 122 req/s; p99 ≈ 1.0 s |
| Memory, idle after that load | 51.8 MiB | 33.8 MiB |
| `/up` | 16,053 req/s | 16,199 req/s |
| Pages, on a copy of the same data | — | `/`, `/schedule?week=1`, a 404 and `/manifest.json` byte-for-byte the POC's, apart from the form's signed token (its key and second differ) |

Found while measuring and building:
- **The first start after the switch-over is heavier.** The migration that rebuilds every table leaves about 10 MiB more resident (28.9 vs 19 MiB idle) until Go returns it to the system. Go's live heap was 2 MB either way (gctrace), and a second start doesn't have it. It happens once.
- **The faster gzip didn't show.** klauspost/compress in place of the standard library's changed nothing measurable on `/`. So the win for pages the same for every visitor is caching them, not compressing them faster. That's a gantry candidate, not yet built.
- **SQLite's driver reads times only from `DATE`, `DATETIME` and `TIMESTAMP` columns**, exactly those names; `datetime(6)` arrives as text. gantry's convention: declare timestamps `datetime`.
- **Dates are text on SQLite.** A `time.Time` bound for a `date` column is written `2006-01-02 00:00:00+00:00`, which sorts after the day itself, so `BETWEEN` loses the last day. Date parameters are cast to text in the query (`CAST(sqlc.arg(first) AS TEXT)`) and passed as `2006-01-02`.
- **The same instant in two formats isn't equal as text.** Rails wrote `2006-01-02 15:04:05.000000`; gantry writes `…+00:00`. They sort correctly together, but `=` doesn't match across them: compare such timestamps by range.
- **goose's Go migrations would deadlock on SQLite's single writer** (goose runs them on the pool while it holds a connection). gantry's migrations are SQL.

### Batch 1b: the page cache, 2026-09-28
The example with `/`, `/schedule`, the favicon and the manifest cached (`web.PageCache`, invalidated by SQLite's `data_version`), against the POC in the same session, measured as before.

| Measure | POC | gantry, cached |
|---|---|---|
| `/`, two runs of 15 s | 126, 126 req/s | 8,118, 6,471 req/s |
| `/` p50 / p99 | 203 ms / ≈ 910 ms | ≈ 1 ms / 75 ms |
| `/schedule?week=1`, 10 s | 625 req/s | 7,509 req/s |
| Memory, idle after 200 requests to `/` | 19.6 MiB | 12.9 MiB |
| Memory, idle after the load | 39.9 MiB | 16.6 MiB |

Found while building it:
- **The tests caught what invalidating on the database alone misses.** The photos' placeholders are files made in the background after boot, not rows, so a page cached before they existed kept showing none. `PageCache.Clear` covers what isn't in the database: the photo store calls it when warming makes anything, and `main` starts warming once the handler (which connects the two) is built.
- **The remaining p99 (75 ms) is the container's CPU quota**, enforced in 100 ms periods at 0.5 CPU, rather than the app.

### Batch 1b: images, 2026-09-28
`gantry/images` holds the POC's image work, made general: encoders are adapters (WebP by default, JPEG), decoders too (`images/heic`), and the pipeline keeps copies at `<dir>/<key>/<width>w-q<quality><ext>`, one make at a time, in a child process. The example's photo service is now Active Storage's rows and the site's slots, over `photos.Images(...)`, its image settings: WebP, the admin's quality, HEIC read.

| Measure | Before (page cache only) | With `gantry/images` |
|---|---|---|
| Production image | 11.4 MB | 12.4 MB (HEIC's decoder, libheif as WASM) |
| Memory, idle after 200 requests to `/` | 12.4 MiB | 13.6 MiB |
| Pages, on a copy of the same data | — | `/`, `/schedule?week=1`, a 404 and `/manifest.json` still the POC's, apart from the form's token |

Found while building it:
- **A decoder package can register itself with `image.Decode` just by being imported.** gen2brain/heic does, so any binary importing it would read HEIC in every pipeline. `Resize` checks the sniffed type against the pipeline's own formats first, so its configuration decides.
- **The example's test for "a type Go can't resize" used HEIC**, which now resizes: it uses TIFF, and a new test attaches a real HEIC (made by libheif's `heif-enc`) and checks it's sized, warmed and given a placeholder.

Not done yet, from this batch's map: a generic image component for new apps (the example keeps its own `ui.Photo`; gantry has `images.Srcset`), and the defaults for new apps (linked stylesheets, precompressed static files), which land with `gantry new`.

### Inlined or linked stylesheets, 2026-09-28
The example's production image as it is (the three stylesheets inlined) against the same with them linked, each at 0.5 CPU and 384 MB on a copy of the same data, with photos warmed. Lighthouse 12.8 (headless Chrome, simulated throttling as PageSpeed does), in a container on the same Docker network; five runs each, medians. The runs agreed closely: every score identical, LCP within 0.02 s.

| | Score | FCP | LCP | Speed index | HTML (compressed) | CSS | Requests |
|---|---|---|---|---|---|---|---|
| Mobile, inlined | 98 | 1.20 s | 2.25 s | 1.20 s | 25.2 KB | — | 25 |
| Mobile, linked | 98 | 1.50 s | 2.34 s | 1.50 s | 18.4 KB | 8.1 KB | 28 |
| Desktop, inlined | 100 | 0.32 s | 0.50 s | 0.32 s | 25.2 KB | — | 25 |
| Desktop, linked | 100 | 0.40 s | 0.52 s | 0.40 s | 18.4 KB | 8.1 KB | 28 |

- **Inlining paints first sooner:** 0.3 s on mobile, 0.08 s on desktop. It costs 6.8 KB more HTML per page, compressed. The whole stylesheet is only 8.1 KB compressed, so linking saves little even for returning visitors.
- **The example keeps inlining.** For gantry's default the numbers argue less strongly than the plan assumed. Linking pays when an app has many pages and heavy CSS; for small stylesheets (under about 10 KB compressed) inlining is as good or better. The default for new apps is decided with `gantry new`, by weight: inline under a threshold, link over it.
- **On mobile, the largest paint is the headline's text, not a photo** (phones show the hero's placeholder). It comes 1 s after the first paint, which points at the web fonts: the headline repaints when its font arrives. That's the next lever for mobile, not the CSS.

### Stylesheets: written as files, bundled at start, 2026-09-28
- **`assets.Styles(names...)`** bundles stylesheets once at start, in order. `Tag()` draws the bundle into the page when it's under `assets.InlineLimit` (10 KB gzipped) and links it when it's over. `InlineStyles` and `LinkStyles` force either. The bundle is also served at its own fingerprinted URL, and `Hash()` gives its `'sha256-…'` for a Content-Security-Policy.
- **The CSS is always separate files** (`assets/css/*.css`); a template never holds any. The example declares `var Styles = All.Styles("application.css", "fonts.css", "site.css")` beside its assets, and its layout draws `@assets.Styles.Tag()`. The bundle is 8.1 KB gzipped, so it's inlined, and the pages are byte-for-byte what they were.
- **A strict `style-src` isn't possible yet, and it's not the bundle's fault.** The example's templates carry about 20 inline `style="…"` attributes: spacing (`gap:12px`), each card's `--i`, the program grid's shape, each photo's placeholder, and the honeypot's hiding. htmx also injects a `<style>` of its own unless `includeIndicatorStyles` is off. A hash covers a `<style>` block, not attributes. Making the site CSP-strict means moving those into classes (the placeholders into a hashed `<style>` of their own) and turning htmx's injection off. That changes the pages, so it's a step of its own.

### Batch 2: sign-in, 2026-09-28
gantry `v0.2.0` (`auth`, `web` cookies and flash); the site's branch `gantry` runs its admin sign-in on it.

| Check | Result |
|---|---|
| gantry's `auth` tests | Sign-in, sign-out, the cookie (a random token; the table holds its SHA-256), tampered and ended sessions, the rate limit, `last_seen_at` at most hourly, reset (sent, unknown address, expiry, used link, rules, every session ended, the configured address), the gate and its return address, moved paths. 1.5 s with bcrypt at its minimum cost in tests (12 in production). |
| The site's ported tests | The POC's `TestSessions`, `TestPasswords`, `TestAdminGate` and `TestProtection` under their names, and the two admin tests batch 1 left out (the admin's stylesheets, the admin's icons). All pass against the published `v0.2.0` in `houston test`. |
| Pages, on a copy of the same data | `/login`, `/passwords/bogus/edit`, and `/admin` and `/admin/programs` signed out (redirects), the POC's byte for byte, headers included. `/passwords/new` the same but for `Transfer-Encoding: chunked` (the POC streamed it; gantry sends its length). |

Found while building it:
- **The local dev data has no admins**: it was seeded, not copied from production. `valleybuiltcrossfit adduser EMAIL` (password on stdin, the site's rules) adds one, and is how a fresh install gets its first admin.
- **`go get` in a workspace resolves to the local gantry**, so pinning a new gantry version runs with `GOWORK=off`: the site's tests then run against the published tag, as a deploy would.

### Batch 3: the admin, 2026-09-28
- **The generator writes what was written by hand.** Pillars was written by hand first; `gantry g resource admin/pillars title:string:required body:string position:position` then gave the same SQL and list page, and the same controller but for two choices made while extracting it: the record's variable is its full name (`pillar`, `workout`), since a first letter would shadow the handler's own (`w` for a Workout is the response writer, `c` for a Category the controller), and `Errors` builds a list, to take any number of rules. Its output is pinned by golden files in `cmd/gantry/testdata`.
- **Two of the seven tables showed what the generator lacked:** photo slots (`photo:photo`: a file field, saved or removed with the record, shown on its edit form) and headings from the table's `singular` constant, so "FAQ" or "Staff" is changed in one line. The six others were generated, then finished by hand, as a scaffold is: labels, hints, choices, list columns, and the Rails validations that read the table (a program's unique key, one workout a day).
- **sqlc edits SQL at byte offsets**, so a non-ASCII character ("…") in a comment before a `SELECT *` shifts its edits and mangles the query (`SELECid`). SQL files stay ASCII.
- **The whole admin matches the POC.** Signed in on a copy of the same data, 20 admin pages (the dashboard, every list and new form, the site's copy, the announcement, the three settings pages, inquiries, a new admin) are the POC's byte for byte. The POC's admin tests pass under their names: `TestAdmin`, `TestAdminNavigation`, `TestAdminSettings`, `TestSiteContent`, `TestAdminTheme` (17), `TestResources`, `TestAdminPhotos`, `TestDashboard`, `TestAdminLeads`, `TestAdminUsers`, `TestAdminGateEverywhere`, and the seven site rules batch 1 left out.
- **`web.Sent` keeps a repeated field's last value, as Rails**: a checkbox sends a hidden "0" and then, ticked, its "1". It kept the first, so a ticked box read as unticked (found porting the announcement's).
- **One change from the POC, on purpose:** changing your own password on the admins' page keeps the browser you did it from signed in (every other session ends). The POC signed you out there too.
- **The site's copy is written through an allow-list:** the six sections and the settings each write a different set of `sites` columns, so their UPDATE is built from a fixed map of the columns the admin may write, never from the request (`app/admin/site/row.go`).

### The switch-over, rehearsed on the live data, 2026-09-28
Houston's new export of the live project (commit `e403bc6`, the POC): the database as SQLite's backup wrote it, and the volume's photos. Every run below was on a copy; the export itself was only read.

| Check | Result |
|---|---|
| The backup | `PRAGMA integrity_check` ok. 1 admin, 4 sessions, 1 lead, the site's content (4 pillars, 3 programs, 4 steps, 7 membership options, 5 staff, 5 FAQs), 105 photo blobs (most of them Rails' own resized copies), 8 cached schedule weeks. |
| The new version started on it | Migrations 1–3 applied; up within about a second (docker run included). |
| After | Integrity ok, 0 foreign key violations. Every table's rows the same but `sessions` (4 → 0, by design: admins sign in once more). Every AUTOINCREMENT counter kept (attachments and blobs at 139, …). The admin's password hash is bcrypt (`$2a$12$`), which gantry's auth reads. |
| Pages, both versions on the live data | 27 of 28 byte for byte: `/`, the schedule, manifest, favicon, the 404, and signed in, the admin's lists, edit forms of real records, the live inquiry, the site's copy and every settings page. The one difference is the home page's form token (a signed time, per render). |
| `/`, 32 workers for 15 s, 0.5 CPU | POC 110 req/s, p99 980 ms; gantry 6,506 req/s, p99 76 ms. |
| Memory | Idle after 200 requests: POC 17.5 MiB, gantry 18.0 MiB. After the load: POC 41.4 MiB, gantry 20.4 MiB. |

Left for after the switch: dropping what only Rails and the POC used (`schema_migrations`, `ar_internal_metadata`, `go_migrations`, `go_settings`, `active_storage_variant_records` and the Rails variant blobs), once a rollback to the POC is no longer wanted.
