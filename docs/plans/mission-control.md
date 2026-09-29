# What gantry needs to rebuild Houston's Mission Control

## Your direction
- "OK gantry needs features necessary to rebuild Houston's Mission Control." (2026-09-28)
- Your list, "Gantry: what Houston needs": MC is gantry's reference app; a side-by-side rewrite with no regressions, then retire the Rails one. Port order: API + auth → jobs + leases → backups/restore → pages + live updates → setup flow.

## Goal
gantry can carry Mission Control (MC): today Rails 8.1, about 7,300 lines of Ruby, SQLite, Solid Queue, Solid Cache, Solid Cable, Turbo and Stimulus, under Puma and Thruster. Target: one Go process, memory that stays flat while it streams, and no regressions against the Rails version run beside it.

This plan is gantry's side: the features, in the order the port needs them. The rewrite itself (and its own plan) is Houston's.

## The target, checked on the Go version
One Go process, and memory that stays flat while it streams (a deploy's log, `docker logs -f`, a snapshot download). No Rails baseline: you know Go will beat it, so the check is that the Go version holds its own line, not a comparison.

## Where MC in Go lives
In Houston's module (`houston/cmd/mission-control`), since its Go code is `internal/`. It then imports instead of rewriting: `project` (compose + `x-houston`; replaces MC shelling out to `houston inspect --json`), `kamal` (`Names`; replaces Ruby's `Generation` copy), `docker`, `humanize`, and the request and response types of `server` and `mission`, so CLI and server share one contract. Houston moves from Go 1.26 to 1.27 for gantry.

## What belongs in gantry
gantry is a framework for many apps; MC is its reference app, a test of it, not its spec. A feature goes into gantry when most web apps would want it (what Rails ships, or standard web plumbing), built for apps in general rather than shaped to MC. Everything else is MC's own code, or Houston's, until a second app needs it; then it moves into gantry, generalised.

## In gantry, in the port's order
Each lands with its tests, and in the gym site where it applies, before the next.

**G1: API and auth**
- Bearer tokens for APIs: named tokens, stored as digests, shown once, last-used throttled; a constant-time check for a secret from the environment (Rails: `authenticate_with_http_token`, `secure_compare`)
- Host-based routing ahead of paths, with a func for hosts only known at runtime (Rails: route constraints)
- Trusted proxies: which peers are trusted, and which header carries the client's address, set per app; spoofable forwarding headers stripped (Rails: `RemoteIp`, `trusted_proxies`). The default stays today's rule.
- Session expiry, idle and absolute, and return-to after sign-in (Rails 8's sign-in generator has return-to)
- Encrypted fields with key rotation, never shown in logs or `%v` (Rails: `encrypts`)
- `db.IsUnique(err)` and a compare-and-swap helper (rows affected), on both engines (Rails: `RecordNotUnique`, `update_all`)
- JSON columns, with a tested example on both engines
- `web.ReadJSON` with a size cap (413, 400) and `web.JSON`, with `{error}` answers
- `Tx` runs code after commit (Rails: `after_commit`)
- Streaming: never buffered by gantry; a failure after the first byte aborts the connection; the client leaving cancels the work; a download helper with a safe filename (Rails: `ActionController::Live`, `send_data`)
- `testkit`: the integration-test browser (cookies, forms, redirects; the gym site's moves in), a fake clock, outbound HTTP stubs, parallel-safe (Rails: `IntegrationTest`, `travel_to`, `parallelize`; WebMock)

**G2: jobs** (Rails: Active Job + Solid Queue)
- In the app's database and process: named queues with their own concurrency; enqueue now or later; retries with a wait, a number of attempts, and a hook when they run out; at most N at a time per key; recurring schedules (every N seconds, daily at a time in a zone); finished jobs cleared; a job's context cancelled on shutdown
- In tests: run inline, or assert what was queued

**G3: pages and live updates** (Rails: turbo-rails, importmap-rails, Action Cable)
- Live updates as Server-Sent Events carrying Turbo Stream HTML (refresh, append, replace), sent after commit, stream names signed, subscribers signed in; fragments rendered outside a request
- `web.Frame(r)`: the Turbo Frame a request is for
- An import map for Turbo, Stimulus and the app's modules, with no build step
- A flash size limit
- A cache with expiry times (Rails: `Rails.cache`)
- Text helpers: time ago in words, byte sizes, pluralize; times in a zone (Rails: ActionView helpers, `Time.use_zone`)

**Throughout**
- Migrations run by a release hook on deploy; each migration's down and up run in a test
- A convention for an app's own subcommands (Rails: rake tasks, `bin/rails runner`)

## MC's own, or Houston's (not gantry)
These are real patterns, but they come from MC being an operations app, not from Rails:
- Leases: a claim with a token, heartbeat, takeover after silence, a finish fenced by the token (`backup_run.rb`, `deploy.rb`)
- A hard deadline per job, stopping everything it started (`DataRun`)
- Safe process calls (argv only, secrets in the environment, process-group kill, stdout and stderr apart, pipes) and their test double: next to Houston's `internal/docker`, which already exists
- Sessions tied to tunnel vs LAN: MC's check, on top of gantry's sessions
- The setup flow with a one-time code, and the installer's subcommands
- The reader for Rails' encryption format: a one-time step in moving MC's data
- The rewrite's checks: byte-identical backups, the tools image, both versions side by side, `/api/v1` as the contract (backups and restore are MC work on G1 and G2)

## Decisions
1. **Docker: the CLI or the Go SDK** (Houston's decision). I recommend the CLI, through Houston's `internal/docker`: Houston already runs docker only that way, MC's inventory has about 25 call sites written against it, and the SDK is a large dependency tree for a binary meant to be small. The SDK would give typed results and no parsing of CLI output; worth it only if parsing turns out fragile.
2. **Live transport.** Server-Sent Events read by `<turbo-stream-source>` (recommended; plain HTTP, one process), rather than ActionCable's WebSocket protocol.
3. **Jobs: build or adopt.** Needs SQLite and Postgres, per-key limits and recurring entries; G2 starts by checking libraries (goqite covers part), and I expect our own small package.
4. **Turbo and Stimulus stay.** The 6 Stimulus controllers (175 lines) and the stylesheet move over as they are.
5. **Moving MC's data.** Encrypted fields read with `crypt.Rails` and rewritten with gantry's keys; sessions reset (everyone signs in again); API tokens keep working (SHA-256 digests).

## Evidence
The three inventories (2026-09-28), from MC at Houston's main:
- Web: 125 routes, 28 HTML and 28 API controllers, 41 templates (2,040 lines), 6 Stimulus controllers, one 66 KB stylesheet, 9 fonts; live updates only through `Turbo::StreamsChannel` (a deploy's append and replace; `broadcast_refresh_to "flight_board"` from about 18 places).
- Data and jobs: 16 tables, 40 indexes (11 partial unique, used as mutexes), 16 foreign keys, 38 migrations; 17 Active Record models and 43 plain objects; 11 jobs, 4 queues, 7 recurring entries; deploys aren't jobs (runners long-poll a claim for up to 25 s).
- Integrations and security: one admin, no mail; three token kinds (personal, runner, per deploy); the `ForwardedHeaders` middleware and tunnel-bound sessions; 9 encrypted fields in 5 tables; no outbound retries; timeouts on everything outbound; subprocesses by argv only, secrets through the environment or stdin.
