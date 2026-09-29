# What gantry needs to rebuild Houston's Mission Control

## Your direction
- "OK gantry needs features necessary to rebuild Houston's Mission Control." (2026-09-28)
- Your list, "Gantry: what Houston needs": MC is gantry's reference app; a side-by-side rewrite with no regressions, then retire the Rails one. Port order: API + auth → jobs + leases → backups/restore → pages + live updates → setup flow.
- "Even authentication is a recipe or a strategy. So, instead of building every single possibility into the framework, it should be a framework and recipes or strategies to handle certain scenarios or desires." (2026-09-28)
- "I do think Phoenix and Elixir has a really interesting way of handling this with plug architecture."
- On the shape: "so we keep generic handler. Since this is technically the pipeline pattern, this is technically a Filter."
- On the request's state: "since we're apeing Rails a bit, let's do current."
- On carrying sign-in: "we could just use db sessions. That would also allow us to expire them a lot easier than cookies or jwt." On API tokens: "I think those are separate. It's like a session that doesn't expire … However, you can revoke them at any time in the UI."
- On error pages: "I like generated pages. It allows the user to customize them."
- "we need gantry new for sure"; MC created by it, first: "yes".
- On constraints: "Let's defer to the rails mechanism."
- "conceptually rails but ideal golang is the goal"
- On session expiry: "it doesn't belong in the framework, but the recipe is a good fit"
- On encrypted fields: keys set at startup, "same as rails"; the extras wait: "yes".
- On G2 and G3: "do rails defaults that make sense in go. I only want to address questions that you are unsure about"
- On the default for pages: "turbo and stimulus is fine. We can always rewrite the gym site with the updated version"
- On an app's commands: "having this work in a local docker container by default. This would make using houston a lot easier and gantry and houston are parts of the same ecosystem." "under the hood, that could just run houston." "gantry new myapp, then gantry dev (or houston dev since it's basically an alias)"; gantry running the `houston` program: "1".

## Goal
gantry can carry Mission Control (MC): today Rails 8.1, about 7,300 lines of Ruby, SQLite, Solid Queue, Solid Cache, Solid Cable, Turbo and Stimulus, under Puma and Thruster. Target: one Go process, memory that stays flat while it streams, and no regressions against the Rails version run beside it.

This plan is gantry's side: the features, in the order the port needs them. The rewrite itself (and its own plan) is Houston's.

## The target, checked on the Go version
One Go process, and memory that stays flat while it streams (a deploy's log, `docker logs -f`, a snapshot download). No Rails baseline: you know Go will beat it, so the check is that the Go version holds its own line, not a comparison.

## Where MC in Go lives
In Houston's module (`houston/cmd/mission-control`), since its Go code is `internal/`. It then imports instead of rewriting: `project` (compose + `x-houston`; replaces MC shelling out to `houston inspect --json`), `kamal` (`Names`; replaces Ruby's `Generation` copy), `docker`, `humanize`, and the request and response types of `server` and `mission`, so CLI and server share one contract. Houston moves from Go 1.26 to 1.27 for gantry.

## What belongs in gantry
gantry is a framework for many apps; MC is its reference app, a test of it, not its spec. A feature goes into gantry when most web apps would want it (what Rails ships, or standard web plumbing), built for apps in general rather than shaped to MC. Everything else is MC's own code, or Houston's, until a second app needs it; then it moves into gantry, generalised.

**Conceptually Rails, ideally Go:** each feature takes its concept, defaults and (where they read well) names from Rails, and is built as a Go expert would: plain structs and fields for settings, standard library types, no hidden global state.

**Framework, strategies and recipes** (Phoenix's split):
- The framework is primitives and extension points, and decides nothing about who a caller is: filters, pipelines and scopes in the router, typed values on the request, one way of answering errors, and helpers like `sign`, a constant-time compare and `web.JSON`.
- A strategy is a filter: a session check, a bearer token check, a shared secret.
- A recipe writes filters, tables and pages into the app, which owns them from then on (as `mix phx.gen.auth`). Sign-in and API tokens are recipes; today's `auth` package becomes one later.

## In gantry, in the port's order
Each lands with its tests, and in the gym site where it applies, before the next.

**G0: `gantry new`** (moved here from the main plan's batch 6; Rails: `rails new`). Its full map is below.

**G1: API and auth** (its full map is below)
- Filters and pipelines (the pipes-and-filters pattern; Phoenix: plugs, `pipeline`, `pipe_through`; Rails: `before_action`, once `before_filter`). A filter is an ordinary handler listed in a pipeline: `type Filter = Handler`, a second name for the role, not a new type (as `byte` is `uint8`). Filters run in order before the route's handler; the pipeline stops at the first that returns an error (answered like any handler's error) or writes a response (a redirect), else goes on. Named pipelines (`rt.Pipeline(filters...)`), and scopes that run one for a group of routes (`rt.Scope("/api/v1", api, func(s *web.Scope) {...})`; Phoenix and Rails: `scope`). Current: the request's own state, typed, which a filter sets and later filters and the handler read (`web.Set`, `web.Get`), in place of each package's own context key (Rails: `Current`, `CurrentAttributes`). It sits in the request's context, changed in place, so a filter passes values on without making a new request. Standard middleware stays for what wraps a handler (gzip, recovery).
- Building blocks for sign-in recipes: `web.BearerToken(r)` (RFC 6750; the scheme's case ignored, an empty token is none), and a `token` package: `token.New(prefix)` (a random key and its SHA-256, the key handed out once, the digest stored), `token.Digest`, and `token.Equal` (constant time, both sides hashed first so a secret's length doesn't leak). `auth`'s private `digestOf` moves there. `sign` stays as it is; expiry on signed values waits for a recipe that needs it (Rails: `authenticate_with_http_token`, `secure_compare`)
- Error pages, as Rails': the router answers an error with the app's static `assets/public/<status>.html` (gantry's public folder, served at the root as Rails' `public/`; embedded, self-contained, no layout or database, so it renders when the app is broken), else a generic page with the status's name; an API client gets `{"error": "Not Found"}` instead of an empty body. An app may still draw its own (`ErrorPage`); if that fails, the static page is served. The pages are the app's, generated for it to restyle (400, 404, 422, 500): by `gantry new`, and by `gantry g error-pages` for an app that exists (MC lives in Houston's module, so it isn't made by `gantry new`) (Rails: `public/404.html`, `PublicExceptions`, `exceptions_app`)
- Constraints, as Rails': `rt.Constraint(check, func(s *web.Scope) {...})`, where `check` sees the whole request; a request that fails it never reaches those routes. Rails' fall-through: when the check passes but no route inside matches, the rest of the routes get a try, then 404. Helpers that build checks: `web.Host(...)` (the hostname cleaned: no port, lowercase, no trailing dot; fixed or decided at run time) and `web.Subdomain(...)`; an app writes its own for anything else (the client's address, a header). Constraints nest and combine with scopes and pipelines. A constraint decides whether a route exists (404, or another route answers); a filter, whether a request may proceed (401, a redirect) (Rails: `constraints`, `matches?`; Phoenix: `scope host:`)
- Trusted proxies and allowed hosts, as fields on the router: `rt.Proxies = web.Proxies{Trusted: ..., ClientIP: ...}` (which peers are proxies, as `netip.Prefix`es, `web.PrivateNetworks` by default, Rails' rule and today's; which header carries the client's address, `X-Forwarded-For` by default or a provider's own such as `CF-Connecting-IP`), and `rt.Hosts` (empty: any host; else others get 403). The client's address, scheme and host are worked out once as the request arrives and kept in Current; `web.ClientIP`, `web.Secure` and `web.Host` read them. From an untrusted peer the forwarding headers are ignored, and removed on the router's own copy of the request, so nothing later reads a forged one. Rails' IP spoofing check is left out: with a trusted list it adds nothing (Rails: `RemoteIp`, `trusted_proxies`, `config.hosts`)
- Encrypted fields, as Rails' `encrypts`: a column type, `crypt.String`, declared per column in `sqlc.yaml` (sqlc's overrides), encrypted when written and decrypted when read. Keys from the environment, set once at startup (`crypt.Use`, as `sql.Register`); the newest encrypts, all decrypt, each value records its key; re-encrypting old rows is an app command. AES-256-GCM, stored as versioned text on both engines. Printed, logged or as JSON it's `[FILTERED]`; `.Reveal()` gives the value. Deterministic mode and unencrypted data wait for an app that needs them (Rails: `encrypts`, `filter_attributes`)
- Constraint errors and stale writes: `db.IsUnique(err)` and `db.IsForeignKey(err)`, the same on both engines (Rails: `RecordNotUnique`, `InvalidForeignKey`). Compare-and-swap is sqlc's `:execrows` (0 rows: someone got there first); a model then returns `db.ErrStale`, answered 409 (Rails: `StaleObjectError`). Automatic optimistic locking (`lock_version`) waits for an app that needs it
- JSON columns: `db.JSON[T]`, a column type declared in `sqlc.yaml` like `crypt.String`, encoded when written and decoded when read; `jsonb` on Postgres, `text` on SQLite, the same Go on both; `NULL` reads as the zero value. Queries inside the JSON are the app's own SQL, per engine. Tested on both engines (Rails: `json` columns, `serialize ... coder: JSON`)
- JSON in and out: `web.JSON(w, status, v)` (Rails: `render json:`) and `web.ReadJSON(r, &v)` into a struct, capped at 1 MB by default and per route (413 over, 400 malformed), unknown fields ignored. A client asking for JSON gets errors as JSON: `{"error": "Not Found"}`, or the handler's own message; a 500 says only "Internal Server Error" (the detail is logged); a 422 gives the fields' errors, `{"errors": {"name": ["can't be blank"]}}` (Rails: `render json: record.errors`)
- After commit: `db.Tx`'s function gets a `*db.Tx` (Go's `*sql.Tx`, embedded, so sqlc's queries work unchanged) with `AfterCommit(func)`: run in order once the transaction commits, never on rollback; a hook can't undo the commit and logs its own failure. Jobs (G2) and live updates (G3) enqueued inside a transaction wait for its commit (Rails 7.2's jobs). The gym site's `db.Tx` calls change their parameter's type. `after_rollback` waits (Rails: `after_commit`)
- Streaming: nothing in gantry holds a stream back (gzip passes flushes through; `text/event-stream` is never compressed; flushing is Go's `http.ResponseController`, no gantry helper). A long stream extends its own write deadline. A failure after the first byte aborts the connection, so the client sees an error, not a cut-off response that looks whole. The client leaving cancels the handler's context, not logged as an error. `web.Download(w, r, filename, contentType, reader)` streams with a safe `Content-Disposition` (non-ASCII names too); files on disk use `http.ServeContent`, with ranges (Rails: `ActionController::Live`, `send_data`, `send_file`)
- `testkit` (Rails: `IntegrationTest`, `travel_to`, `parallelize`; WebMock):
  - A test browser: `testkit.Browser(t, app)` with `Get`, `Post` and `Submit` (fills and submits a form on the page); keeps cookies, follows redirects if asked, sends headers (a bearer token); `Find` by CSS selector (`assert_select`) and JSON bodies. The gym site's request helpers move in.
  - A clock you can set and move (`testkit.Clock`), handed in as gantry's parts take a `Now`, not a patched global time.
  - Outbound HTTP: code is handed an `*http.Client`; testkit's answers what the test scripted, and any other request fails the test.
  - Parallel-safe: a database per test and nothing global, so `t.Parallel()` works.
  - Job helpers (run inline, assert what was queued) come with G2.

**Recipes the port needs** (built on G1; written into the app, which owns them; not the framework)
- Sessions: sign-in has two sides, a method that proves who someone is once (password, emailed code, TOTP, passkey, OAuth), and a database session that holds the result, its key carried by a cookie (browsers) or `Authorization: Bearer` (the CLI, agents, apps). Expiry and revocation live in the row, whichever way the key came. Expiry: `Idle` and `Lifetime` (zero: never, Rails' default); an ended session is deleted when found and the request goes on signed out; the cookie's own expiry matches `Lifetime`; `last_seen_at` written at most hourly; return-to after sign-in. Tested with testkit's clock. JWT is left to a recipe for an app that needs stateless tokens. Today's `auth` package becomes this recipe.
- API tokens: their own table, built as sessions are (a random key, its digest stored): named, shown once, never expiring, revoked in the app's UI, and not ended by signing out everywhere.

**G2: jobs** (Rails: Active Job + Solid Queue; Rails' defaults unless noted)
- A job is a name, a typed argument struct and a function: `jobs.Define(q, "backup", perform, opts)` gives a `Job[A]` with `Enqueue(ctx, a)`, `EnqueueAt` and `EnqueueIn`. Arguments are stored as JSON; the name, not the Go type, identifies the job, so renaming code doesn't strand queued ones. Defined on the app's queue value, not a global registry.
- Stored in the app's database, on both engines, in gantry's own tables (its own migration version table, as `auth`'s). Claimed with `FOR UPDATE SKIP LOCKED` on Postgres, through the single writer on SQLite. Enqueuing wakes the process's own workers at once; polling (Solid Queue's 0.1 s) is only for jobs scheduled later or enqueued by another process.
- Run in the web process by default (Rails 8: Solid Queue in Puma), started by `app.Run`; or on its own, by an app subcommand.
- Queues: named, each with its own concurrency (3 workers by default, as Solid Queue's threads), and a priority within a queue (lower first, 0 by default).
- Failures: no retries unless asked, as Active Job. `Retry{Attempts: 5, Wait: 3 * time.Second}` (Rails' `retry_on` defaults), or `jobs.Polynomial` for Rails' `:polynomially_longer`; `OnExhausted` runs when attempts run out; `jobs.Discard(err)` gives up at once (`discard_on`). A failed job is kept, with its error, to inspect and retry by hand (Solid Queue's failed executions).
- `Timeout` per job, none by default: the job's context ends at it. Rails can't do this (Ruby can't stop a thread safely); Go's contexts can, so it's the framework's. (Stopping the processes a job started is still MC's.)
- At most N at a time per key: `Limit{To: 1, Key: func(A) string}`, a job over the limit waits (Solid Queue's `limits_concurrency`, `on_conflict: :block`; `Discard` as the other choice); a limit held by a crashed process expires after 3 minutes (Solid Queue's default period).
- Recurring: in code, `jobs.Every(30 * time.Second)` and `jobs.Cron("0 3 * * *", loc)`, in a time zone (Solid Queue's `recurring.yml`). Each tick is enqueued once however many processes run: a unique (task, time) row, so a second insert is `db.IsUnique` and skipped.
- Finished jobs kept a day, then cleared (Solid Queue's `clear_finished_jobs_after`).
- Shutdown: workers stop claiming, running jobs get a grace period (5 s, Solid Queue's `shutdown_timeout`), then their contexts are cancelled and they go back to the queue. Each process heartbeats; a process silent for 5 minutes has its claimed jobs released (Solid Queue's defaults).
- Enqueued inside a transaction: waits for its commit (G1's `AfterCommit`; Rails 7.2).
- Logged as Rails does: enqueued, started, finished with its duration, failed with the error.
- In tests: `testkit` runs jobs inline, or records them to assert on (`assert_enqueued_with`) and run later (`perform_enqueued_jobs`).
- A page to see and retry jobs (Mission Control — Jobs) is a recipe, not the framework.

**G3: pages and live updates** (Rails: turbo-rails, Action Cable, importmap-rails, `Rails.cache`, ActionView helpers)
- Live updates, over Server-Sent Events (decision 2):
  - `live.Stream` serves a page's streams as SSE; it's an ordinary route, so the app's pipeline decides who may subscribe (signed in, and so on). Stream names are signed with `sign` (purpose "stream"), as `turbo_stream_from` does, so a page can only subscribe to what it was given.
  - `Broadcast(ctx, stream, event)` sends to every subscriber; inside a transaction it waits for the commit. Refreshes to one stream within half a second are merged into one (turbo-rails' debounced `broadcast_refresh_to`).
  - A comment line every 30 s keeps proxies from closing idle streams (Cloudflare closes after 100 s). A subscriber too slow to keep up is disconnected rather than buffered, so memory stays flat; the browser reconnects.
  - In one process, the hub is in memory. Rails defaults to Solid Cable (the database) because Rails runs many processes; a gantry app is one. A database-backed hub waits for an app that runs several.
  - Fragments render outside a request: a templ component renders to any writer, and `turbo` wraps it as a stream action.
- `gantry new` sets up Turbo and Stimulus by default, as Rails does; htmx stays supported (the gym site keeps it until it's rewritten on the new version).
- Turbo (`turbo` package): stream actions (`turbo.Append(target, c)`, `Prepend`, `Replace`, `Update`, `Remove`, `Refresh`), `turbo.Frame(r)` (the `Turbo-Frame` header: the frame a request is for), and Turbo's HTTP rules as defaults: a redirect after a form submission is 303, a form with errors answers 422 (Rails 7).
- Import map, no build step (importmap-rails, Rails 8's default): `pin` names to files under the app's assets, served digested; the page gets `<script type="importmap">` and `modulepreload` links. `gantry importmap pin turbo` downloads a package into the app's vendored assets (no CDN at run time). Stimulus controllers under `app/javascript/controllers` are pinned and registered by name, as `pin_all_from` and `eagerLoadControllersFrom`.
- Flash size: a flash that would push its cookie past 4 KB (browsers' limit) is cut to fit, with a warning logged. Rails raises `CookieOverflow`; a cut message beats a 500.
- A cache, `Rails.cache` in Go: `cache.Fetch(ctx, key, ttl, func() (T, error))`, `Read`, `Write`, `Delete`. In memory, capped at 32 MB (Rails' memory store default), least recently used out first; concurrent misses for one key compute it once (`singleflight`). A database-backed store (Solid Cache) waits for an app that runs several processes. Separate from `web.PageCache`, which keeps whole responses.
- Text helpers, with Rails' exact wording so pages match: `TimeAgo` (`time_ago_in_words`: "less than a minute", "about 1 hour"), `ByteSize` (`number_to_human_size`: 1024-based, "1.23 MB"), `Pluralize` (`pluralize(2, "person")`, the generator's inflections). Times: the app's zone (UTC by default, `config.time_zone`) and a request's own zone in Current (`Time.use_zone`); helpers format in it.

**Throughout** (Rails' defaults, made for Go; built in G0, since they shape every app from its first day)
- Migrations, the Rails set (today there's only `db.Migrate`, run at startup):
  - `gantry g migration create_posts` writes `db/migrations/<timestamp>_create_posts.sql` (Rails' timestamps, so branches don't collide; the gym site's `00001`-style files keep working), in the app's engine's SQL. The name shapes the file as Rails' does: `create_posts` a table, `add_email_to_users` a column.
  - `gantry g resource` writes its table's migration from its fields.
  - Pending migrations run in any order, as Rails' (goose's out-of-order option).
  - `db migrate`, `db rollback` (one, or `STEP=n`), `db status`, `db seed` (the app's `db/seeds.go`), `db reset` (development: drop, migrate, seed), `db console` (sqlite3 or psql, Rails' `dbconsole`).
  - After `db migrate` in development, `db/schema.sql` is written, the whole schema in one file to read and review (Rails' `structure.sql`); from SQLite itself, from `pg_dump --schema-only` on Postgres.
  - Migrations keep running at startup by default (Rails 8's `db:prepare` in the container's entrypoint); `myapp db migrate` is there for a separate release step.
  - `testkit.Migrations(t, ...)` runs every migration up, each down and up again, on a fresh database, so a broken down section fails a test.
  - A migration that can't be undone leaves its down section empty; `db rollback` stops there with an error (Rails' `IrreversibleMigration`). Postgres' `CREATE INDEX CONCURRENTLY` uses goose's no-transaction marker.
- An app's own commands (Rails: `bin/rails`, rake tasks, `runner`):
  - The app's binary is its command line: `myapp` serves (the web and, by default, jobs), `myapp db ...` as above, `myapp jobs` runs only jobs, and `myapp tasks` lists the app's own.
  - The app's tasks are registered in one file, as routes are (`app/tasks.go`: a name, a line of help and a function given the app's context), and run as `myapp task NAME args...`. That covers rake tasks and `rails runner`. Go has no console (`rails console`); `db console` is the nearest.
  - In development these run in the app's local container by default, and gantry just runs Houston to do it (gantry and Houston are one ecosystem): `gantry db migrate` is `houston exec myapp db migrate`, in the running dev container, or a one-off container from the same image, code, data and environment when it isn't running (as `docker compose exec web bin/rails ...`). Generators write files on the host and run their tools (sqlc, templ, `go`) the same way, so only Docker, Houston and gantry are installed. `--local` runs on the host instead, for someone without Docker.
  - `gantry new` writes all of this, and runs `houston init` for Houston's files (Dockerfile stages, `compose.yml` with `x-houston`), with its commands set: `test` is `go test ./...`, `console` is `myapp db console`. A new app runs under `houston dev` from the start, at http://<appname>.localhost (a branch at <branch>.<appname>.localhost), with no ports to choose or remember; gantry already treats `*.localhost` as local (plain-HTTP cookies).
  - gantry includes Houston by running it: gantry's own commands are `new`, `g`, `db` and `task`, and anything else goes to `houston` (`gantry dev` is `houston dev`, and `test`, `console`, `deploy`), as `bin/rails server` and `bin/rails test` are one command. The gantry library stays free of Houston's code; each is released on its own. If `houston` isn't installed, gantry says how to install it, or offers to. (One binary holding both was considered: it needs Houston's CLI made public and gantry's CLI in its own module; it can come later without changing a command.)
  - Needs from Houston (Houston's plan): `houston exec CMD...`, any command in the app's container or a one-off one; `console` becomes a case of it.

## G0: `gantry new` (full map)
**The slice:** `gantry new myapp`, then `gantry dev`, serves a working app at http://myapp.localhost: a home page, `/up`, the error pages, and `gantry test` passing, on SQLite or Postgres. `gantry new cmd/mission-control --in-module` does the same inside Houston's module, and MC starts there. G0 also takes the Throughout items, migrations and an app's commands, since they shape `main.go` and every app from its first day.

**What it writes** (the main plan's layout; all of it the app's, to edit):
```
myapp/
  go.mod                   module myapp (--module github.com/you/myapp); requires the gantry release that made it
  cmd/myapp/main.go        config, then serve, or a command: db ..., jobs, task NAME
  config/config.go         the environment into a Config (caarlos0/env): DATABASE_URL, SECRET_KEY_BASE, PORT
  app/app.go               opens the database, runs migrations, builds the router
  app/routes.go            the routes, and the browser pipeline
  app/tasks.go             the app's own tasks: none yet
  app/home/                controller.go, index.templ, controller_test.go
  app/models/              empty, for sqlc's output
  app/shared/layout/       layout.templ: the page shell, the stylesheet, the flash
  assets/css/application.css
  assets/public/           400.html, 404.html, 422.html, 500.html (the error pages; served at the root, as Rails' public/)
  db/migrations/           empty; db/seeds.go
  db/schema.sql            written by db migrate in development
  test/fixtures/
  sqlc.yaml                for the chosen engine
  Dockerfile               toolchain, dev, test and production stages (as the gym site's)
  compose.yml, .dockerignore, .env, .gitignore: from `houston init`
  README.md                gantry dev, gantry test, gantry db migrate, where things go
```
- **Flags:** `--db sqlite|postgres` (SQLite by default, as Rails 8), `--module PATH` (the name by default), `--in-module` (an app in the module you're in: no `go.mod` of its own, imports under the module's path, and a Dockerfile whose build context is the module's root), `--gantry PATH` (a `go.work` using a local gantry, for developing both, as the gym site's), `--skip-houston`.
- **Steps:** write the files; `houston init` (named after the app); `go mod tidy`, `templ generate` and `sqlc generate` in the toolchain container; `git init`. A folder that exists and isn't empty is refused.
- **Turbo, Stimulus, pipelines and the rest** go into the skeleton as G1 to G3 land. G0's app is what gantry has today.

**gantry gains:**
- `db`: rollback (one step, or n), status, pending migrations run in any order, and the schema written to `db/schema.sql` (SQLite from itself, Postgres with `pg_dump --schema-only`).
- `testkit.Migrations`: every migration up, each down and up again, on a fresh database.
- The CLI: `new`; `g migration` (a timestamped file shaped by its name); `g error-pages` (skips pages that exist, `--force` to replace); `g resource` writes its table's migration; `db` and `task` run the app's binary through `houston exec` (`--local`: on the host); anything else goes to `houston`, which gantry offers to install when it's missing.
- An app's commands are plain generated code: `main.go` switches on its arguments and calls `db`'s functions and the app's tasks. No framework package for it, as the main plan's "code generation, not runtime magic".

**Needs from Houston first:** `houston exec CMD...` (in the running dev container, or a one-off one from the same image, code, data and environment), and `houston init` taking the name without asking. Both in Houston's plan.

**Tests:**
- The generated files against golden copies in `cmd/gantry/testdata`, for each engine and for `--in-module`.
- A generated app, on each engine, in the toolchain container: builds, `go vet` is clean, and its own tests pass (Postgres when `GANTRY_TEST_POSTGRES_URL` is set, as the framework's).
- `g migration`'s shapes (create a table, add a column), `g error-pages` not overwriting, `g resource`'s migration applying and rolling back.
- `db` rollback and status on both engines; `testkit.Migrations` failing on a broken down section.

**Evidence, recorded here when G0 is done:**
- `gantry new myapp && cd myapp && gantry dev`: http://myapp.localhost answers 200, `/up` answers, `/nope` is the generated 404; `gantry test` passes; `gantry db migrate` runs in the container.
- The same with `--db postgres`.
- MC created with `--in-module` in Houston's module, building and serving its home page.
- The new app's footprint, the baseline for G1 to G3: its binary's size, and its memory idle and under load, measured as the gym site's.

**Progress, 2026-09-29:**
- Houston's two changes are done (Houston `67b5217`, `docs/plans/exec-and-init-name.md` there): `houston exec`, and `houston init --name`.
- `db`: `Rollback` (n steps, newest applied first; an empty down section stops it with `IrreversibleError`, before running it), `Status`, pending migrations in any order, and `Schema` on Postgres through `pg_dump` (its comments, `SET` lines and `\restrict` keys left out, so it's the same run to run). `testkit.Migrations`: each migration up alone, each down checked against the schema before its up, then all up again. Tests pass on SQLite and on Postgres 17 (the test binary run in `postgres:17`, for its `pg_dump`); the mutation check caught all 11 (one, a down that leaves a table behind, first slipped past, caught only later by a vaguer error, so the test was made to name the migration).
- The generators: `g migration NAME [field:type[:required]...]` (a UTC timestamp, the name shaping it as Rails': `create_<table>` with `id`, `created_at` and `updated_at`, `add_<cols>_to_<table>`, else an empty up and down; the gym site's column conventions, on either engine by `sqlc.yaml`), `g error-pages [--force]` (Rails' four pages, self-contained, in `assets/public/`, kept when they're there), and `g resource` writing its table's migration. The mutation check caught all 11 (two first had to be redone as valid code).
- `gantry new NAME [--db sqlite|postgres] [--module] [--gantry PATH] [--skip-houston]`: the gym site's shape (`cmd/NAME` with `serve`, `db` and `task`; `config` from `getenv`, as the site's, not caarlos0/env; `app.App` with `Handler()`; the home page; `app/tasks.go`; `db/migrations` embedded, with `testkit.Migrations` and a schema check; `db/seeds`; `test/`; `assets/` with `public/` and the error pages; `sqlc.yaml`; the Dockerfile's stages; `compose.yml` with `x-houston`), then `houston init --name`, `houston exec go mod tidy`, `houston exec templ generate` and `git init`. Postgres apps get a `db` service, a random local `POSTGRES_PASSWORD` in `.env`, Postgres 17's client in the toolchain, and `testkit.Postgres`, a database of its own per test. `gantry db|task|tasks` run `go run ./cmd/NAME ...` through `houston exec` (`--local`: on the host); anything else is `houston`'s. Pinned by golden files on each engine; a generated app builds, vets clean and passes its tests in the toolchain (`TestNewAppBuilds`).
- Added on the way: `db.Console`, a SQL console in the app's own binary, for `db console` where `sqlite3` or `psql` isn't installed (the production image; Houston needs a server console command); `db.Schema` leaves out gantry's own tables (`gantry_settings`), which aren't the app's; a migration that does nothing either way can be rolled back; the schema file's header no longer names the gym site's command (the gym site regenerates its `db/schema.sql` once when it moves to `v0.6.0`); `testkit.Migrations` takes its database, so it runs on Postgres too.
- End to end, 2026-09-29, with Houston `67b5217` and this checkout (`--gantry`), on rootless Docker: `gantry new shop` then `gantry dev` served `http://shop.localhost` (200; `/up` 200; `/robots.txt` 200; `/no-such-page` the generated 404). `gantry g migration create_posts ...` then `gantry db migrate` in the running container rewrote `db/schema.sql`; `db console` (sqlite3), `db rollback`, `db status` and `tasks` worked; with dev stopped, `db migrate` and `db status` ran as one-offs and left nothing running, and the app's tests passed in its container. On Postgres (`gantry new depot --db postgres`): the one-off started the database for the run, `db/schema.sql` came from `pg_dump`, `db console` was `psql`, and the tests (`TestMigrations` on Postgres included) passed. `gantry test` builds against the `go.mod` pin, `v0.5.0`, which lacks this plan's `db` changes, so it fails until `v0.6.0` is tagged.
- The mutation check caught all 21 in this part (three first had to be redone: two that changed nothing or didn't compile, and one real gap, a command's exit code, which got `TestExitCodes`).
- `v0.6.0` tagged and pushed (your go-ahead, 2026-09-29). A new app's `go.mod` requires it; `TestNewAppBuilds` checks this checkout through a `replace`, since go.work alone still reads the required release's `go.mod`.
- `--in-module` (gantry `5d9ba70`): the app is a folder of the module (no `go.mod`, imports under the module's path), built from the module's root with a `Dockerfile.dockerignore` beside its Dockerfile, the module mounted at `/app` in development with the app's folder as its working directory, and no `git init`. Houston needed two changes for it (Houston `922fcac`, `e3f0feb`, `docs/plans/build-in-a-monorepo.md` there): a build may reach the rest of its git checkout, where it was held to the compose file's folder, and `houston init` completes an app whose context is above its folder. Found on the way, and fixed for every app (`75621b4`): `templ generate` runs before `go mod tidy` (tidy took templ for an indirect requirement), and `.houston/` is ignored by git.
- **MC created** (Houston `c581361`): `gantry new mission-control-go --in-module` in Houston's root, where you said structure doesn't matter. The folder is `mission-control-go/`, and so is the Houston project, so it runs beside the Rails one (`mission-control`): `gantry dev` served `http://mission-control-go.localhost` (`/` 200, `/up` 200, a missing page the 404, `/robots.txt` 200); `gantry db status` and its tests in the dev container passed; `gantry test` built its test image from Houston's root (a 3.3 MB context) against the published `v0.6.0` and passed; Houston's own suite passes with it, on Go 1.27.1, where Houston's module and toolchain image moved.
- **The baseline, for G1 to G3** (MC's Go app as `gantry new` made it; production image, rootless Docker on this desktop): a 16 MB binary in a 6.6 MB image; 16.7 MiB idle; 28 MiB after 5,000 requests to `/`, 50 at a time, all 200.
- **G0 is done.** Next: G1, from its first item (filters and pipelines).
- Found: this machine's Docker is rootless now (`DOCKER_HOST`), and gantry's Postgres tests still need a Postgres and `pg_dump` by hand; the test stage's Postgres service is batch 6's.

**Order:** Houston's two changes; `db` and `testkit.Migrations`; the generators and the skeleton; the CLI's commands and Houston pass-through; the generated app's tests on both engines; MC created. gantry `v0.6.0` at the end (`v0.5.0` is the release before this plan, which the gym site runs).

## G1: API and auth (full map)
**The slice:** everything an app needs to serve an API and a signed-in admin the way MC does, as framework pieces (the recipes that use them are their own step): the router's filters, pipelines, scopes and constraints; Current; trusted proxies and hosts; error pages and JSON answers in the router; the token building blocks; the database's constraint errors, JSON columns, encrypted fields and after-commit hooks; streaming; and testkit's browser, clock and fake outbound HTTP. Each lands in `gantry new`'s app where it applies, and MC's Go app is updated by hand (its own code) as they land.

**The router (`web`), items 1, 4 and 5:**
- `type Filter = Handler`; `type Pipeline []Filter`, written as a list (`web.Pipeline{web.AcceptJSON, tokens.Require}`): the plan's `rt.Pipeline(...)` holds no router state, so a plain list is the Go way.
- `Scope`: what routes are added to. The router is one (its routes are the root scope's), and `Handle`, `Mount`, `Resources` and `Cached` are the scope's, so existing code is unchanged. `s.Scope(prefix, pipeline, func(s *Scope))` nests: prefixes join, pipelines run outer first. A scope's routes live on the router's mux, each handler wrapped by its filters.
- A pipeline runs its filters in order, then the route's handler. It stops at a filter's error (answered as a handler's is) or at one that wrote a response (gantry's writer already tracks that).
- `Constraint(check, func(s *Scope))`, `check` being `func(*http.Request) bool`: its routes are on a mux of its own. A request tries the constraints in the order they were declared (a nested one before its parent), each only when its check passes and its mux has the route; then the ordinary routes, then 404 (Rails' fall-through). `web.Host("admin.example.com", ...)`, `web.HostFunc(func(host string) bool)` (hosts known at run time) and `web.Subdomain("api")` build checks, on the host cleaned: no port, lowercase, no trailing dot.
- Current: `web.NewKey[T](name)` is a typed key (a pointer, so two packages' keys never collide); `web.Set(r, key, v)` and `web.Get(r, key) (T, bool)`. The router puts an empty store in each request's context as it arrives; `web.WithCurrent(r)` gives one to a request made outside a router (a filter's unit test).
- Trusted proxies and hosts (item 5): `rt.Proxies` and `rt.Hosts`, as the item says; the address, scheme and host are worked out as the request arrives and kept in Current, and `ClientIP`, `Secure` and the host checks read them.

**Answers (`web`), items 3 and 8:** `rt.Public` names the app's public files (`assets.All`, which gains `Public(name)`); an error is its `<status>.html` there, else the router's generic page; the app's `ErrorPage`, when set, is tried first, and the static page follows if it fails. A client asking for JSON gets `{"error": ...}`, and a 422 `{"errors": {...}}`. `web.JSON` and `web.ReadJSON` (1 MB by default; 413, 400). `gantry new`'s `app/errors.go` goes, replaced by `rt.Public = assets.All`.

**The rest, as the items say:** `web.BearerToken` and the `token` package (item 2); `crypt.String` (6); `db.IsUnique`, `IsForeignKey`, `ErrStale` answered 409 (7); `db.JSON[T]` (7); `db.Tx`'s `*db.Tx` with `AfterCommit` (9), and the gym site's `db.Tx` calls change type, in its next move; streaming and `web.Download` (10); testkit's `Browser`, `Clock` and `HTTP` (11).

**Tests:** each item's tests first, as gantry's are; the router's with `httptest` (a filter that stops, one that sets Current, nesting, the order of constraints and their fall-through, host cleaning, forged headers from an untrusted peer); the database's on SQLite and, by hand as in G0, on Postgres 17; the mutation check for each item. `gantry new`'s app keeps building and passing its tests (`TestNewAppBuilds`), and so does the gym site against the new gantry through its `go.work`.

**Order:** filters, pipelines, scopes and Current (the rest build on them); constraints; trusted proxies and hosts; error pages and JSON answers; tokens; the database's items; encrypted fields; streaming; testkit. A tag at the end (`v0.7.0`), and the gym site and MC's app moved to it.

**G1 progress:**
- **1. Filters, pipelines, scopes, Current** (2026-09-29): `web.Filter`, `web.Pipeline`, `Scope` (the router's routes are its root scope's, so `Handle`, `Mount`, `Resources` and `Cached` are unchanged for apps), `rt.Scope`/`s.Scope`, and Current (`NewKey`, `Set`, `Get`, `WithCurrent`; the router gives each request one, inside its middleware). Tests first (`TestPipelines`, `TestCurrent`, `TestCurrentPerRequest`); gantry's suite passes, and so does `TestNewAppBuilds`; the gym site's suite passes against this checkout but for `TestSchemaIsCurrent`, which is G0's header change only (two comment lines; its `db/schema.sql` is regenerated when it moves). The mutation check caught all 10 (one, a filter's redirect not ending the request, was first caught only in part, the test comparing the start of the body; it compares the whole body now).

- **4. Constraints** (2026-09-29): `rt.Constraint`/`s.Constraint(check, routes)`, each with a mux of its own, tried in declaration order (a nested one before its parent, both checks passing), falling through to the other routes, then 404; `web.Host`, `web.HostFunc` (read on every request) and `web.Subdomain` (Rails' tld_length 1), on `web.RequestHost` (no port, lowercase, no trailing dot). Tests: `TestConstraints`, `TestConstraintInAScope`, `TestConstraintOrder`, `TestRequestHost`. The mutation check caught all 11, after two test gaps were closed (a constraint inside a scope with filters; the same run-time host asked before and after its setting changes) and two mutations that changed nothing were redone.

## MC's own, or Houston's (not gantry)
Checked against the framework-first rule (2026-09-29): only the per-job deadline moved into gantry (G2's `Timeout`). The rest are built on gantry's pieces (compare-and-swap, `token`, filters, jobs' `Limit`) without being general needs.
These are real patterns, but they come from MC being an operations app, not from Rails:
- Leases: a claim with a token, heartbeat, takeover after silence, a finish fenced by the token (`backup_run.rb`, `deploy.rb`)
- Stopping everything a job started when its deadline passes (`DataRun`); the deadline itself is G2's `Timeout`
- Safe process calls (argv only, secrets in the environment, process-group kill, stdout and stderr apart, pipes) and their test double: next to Houston's `internal/docker`, which already exists
- Sessions tied to tunnel vs LAN: MC's check, on top of gantry's sessions
- The setup flow with a one-time code, and the installer's subcommands
- The reader for Rails' encryption format: a one-time step in moving MC's data
- The rewrite's checks: byte-identical backups, the tools image, both versions side by side, `/api/v1` as the contract (backups and restore are MC work on G1 and G2)

## Decisions
1. **Docker: the CLI or the Go SDK** (Houston's decision). I recommend the CLI, through Houston's `internal/docker`: Houston already runs docker only that way, MC's inventory has about 25 call sites written against it, and the SDK is a large dependency tree for a binary meant to be small. The SDK would give typed results and no parsing of CLI output; worth it only if parsing turns out fragile.
2. **Live transport.** Server-Sent Events read by `<turbo-stream-source>` (recommended; plain HTTP, one process), rather than ActionCable's WebSocket protocol.
3. **Jobs: build or adopt.** Needs SQLite and Postgres, per-key limits and recurring entries; G2 starts by checking libraries (goqite covers part), and I expect our own small package.
   G2 and G3 take Rails' defaults as they make sense in Go (your direction, 2026-09-29: "Go through G2 and G3 and do rails defaults that make sense in go").
4. **Turbo and Stimulus stay, and become gantry's default.** MC's 6 Stimulus controllers (175 lines) and the stylesheet move over as they are; `gantry new` sets up Turbo and Stimulus for every app, htmx still supported.
5. **Moving MC's data.** Encrypted fields read with `crypt.Rails` and rewritten with gantry's keys; sessions reset (everyone signs in again); API tokens keep working (SHA-256 digests).
6. **Filters are handlers.** A pipeline is a list of `web.Handler`s, so there's one type and nothing new to learn; values pass forward through Current. Considered: a separate type returning the request (`Step`, `Plug`, `Gate`), and an output added to `Handler`, which every action would return and throw away. `Middleware` stays the name for wrappers only.

## Evidence
The three inventories (2026-09-28), from MC at Houston's main:
- Web: 125 routes, 28 HTML and 28 API controllers, 41 templates (2,040 lines), 6 Stimulus controllers, one 66 KB stylesheet, 9 fonts; live updates only through `Turbo::StreamsChannel` (a deploy's append and replace; `broadcast_refresh_to "flight_board"` from about 18 places).
- Data and jobs: 16 tables, 40 indexes (11 partial unique, used as mutexes), 16 foreign keys, 38 migrations; 17 Active Record models and 43 plain objects; 11 jobs, 4 queues, 7 recurring entries; deploys aren't jobs (runners long-poll a claim for up to 25 s).
- Integrations and security: one admin, no mail; three token kinds (personal, runner, per deploy); the `ForwardedHeaders` middleware and tunnel-bound sessions; 9 encrypted fields in 5 tables; no outbound retries; timeouts on everything outbound; subprocesses by argv only, secrets through the environment or stdin.
