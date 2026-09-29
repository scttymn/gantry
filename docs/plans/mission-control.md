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

**G0: `gantry new`** (moved here from the main plan's batch 6; Rails: `rails new`)
- Writes the app layout in the main plan (`cmd/`, `config/`, `app/routes.go`, `app/models/`, sqlc for the chosen engine, the test setup), and the generated error pages. A generated app builds and passes its own tests.
- Two modes: a new module (`gantry new myapp`, with its `go.mod`), and an app inside the module you're in (`gantry new cmd/mission-control --in-module`), for monorepos and for MC, which imports Houston's `internal/` packages. The layout is the same; only `go.mod` and import paths differ.
- MC is created with it on day one. What G1 to G3 add to every app (pipelines in `routes.go`, error pages) goes into `gantry new` as it lands; what the port adds by hand is the list of what `gantry new` still lacks.

**G1: API and auth**
- Filters and pipelines (the pipes-and-filters pattern; Phoenix: plugs, `pipeline`, `pipe_through`; Rails: `before_action`, once `before_filter`). A filter is an ordinary handler listed in a pipeline: `type Filter = Handler`, a second name for the role, not a new type (as `byte` is `uint8`). Filters run in order before the route's handler; the pipeline stops at the first that returns an error (answered like any handler's error) or writes a response (a redirect), else goes on. Named pipelines (`rt.Pipeline(filters...)`), and scopes that run one for a group of routes (`rt.Scope("/api/v1", api, func(s *web.Scope) {...})`; Phoenix and Rails: `scope`). Current: the request's own state, typed, which a filter sets and later filters and the handler read (`web.Set`, `web.Get`), in place of each package's own context key (Rails: `Current`, `CurrentAttributes`). It sits in the request's context, changed in place, so a filter passes values on without making a new request. Standard middleware stays for what wraps a handler (gzip, recovery).
- Building blocks for sign-in recipes: `web.BearerToken(r)` (RFC 6750; the scheme's case ignored, an empty token is none), and a `token` package: `token.New(prefix)` (a random key and its SHA-256, the key handed out once, the digest stored), `token.Digest`, and `token.Equal` (constant time, both sides hashed first so a secret's length doesn't leak). `auth`'s private `digestOf` moves there. `sign` stays as it is; expiry on signed values waits for a recipe that needs it (Rails: `authenticate_with_http_token`, `secure_compare`)
- Error pages, as Rails': the router answers an error with the app's static `<status>.html` (embedded, self-contained, no layout or database, so it renders when the app is broken), else a generic page with the status's name; an API client gets `{"error": "Not Found"}` instead of an empty body. An app may still draw its own (`ErrorPage`); if that fails, the static page is served. The pages are the app's, generated for it to restyle (400, 404, 422, 500): by `gantry new`, and by `gantry g error-pages` for an app that exists (MC lives in Houston's module, so it isn't made by `gantry new`) (Rails: `public/404.html`, `PublicExceptions`, `exceptions_app`)
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

**Throughout** (Rails' defaults, made for Go)
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
