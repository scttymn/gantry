# Plan: performance, fast without being told how

## Goal
100,000 users on a modest server, for an app that did nothing special to get there. Gantry tunes itself: what makes an app fast lives in the framework (the database path, the generators' defaults, the build), sized from the machine and the workload, with no option an author has to know. No library in the hot path is kept for its own sake, and nothing has to be Go if something else does it better. It stays a framework for ordinary development: `bin/gantry dev`, `test` and `check` work as they do now.

## Evidence
From the $12 server challenge (2026-10-13): the feed API as a generated v0.14.0 app, one M5 core, 2 GB, all 42 of the challenge's checks passing.

- Flat out on 64 connections: 21,700 req/s, 46 µs of CPU a request. The same handlers with their statements prepared once: 28,300 req/s.
- Where the CPU went, flat out: running queries 35%, compiling the SQL again on every call 20%, network syscalls 13%, JSON 7%, GC 5%, gantry's middleware 3%. The database path is about two-thirds.
- At 20,000 simulated users, direct: 46–50% of the core and 866 MB (43 KB a connection, 8 KB of it `net/http`'s buffers and the rest goroutine stack, which SQLite's deep frames regrow: 7% of CPU). The challenge's reference FastAPI submission on the same setup: about 57% and 291 MB. The video's leaderboard has Go and Python at 22,000 users, Rust and C++ at 64,000.
- Deployed with Houston, an app is behind Cloudflare, the tunnel and kamal-proxy: a few busy connections, not thousands of idle ones. The flat-out shape is the real one. Idle connections are the proxy's.
- `modernc.org/sqlite` v1.59.0 (`stmt.go`): a `driver.Stmt` for a single statement keeps its compiled handle (`pstmt`) and resets it after each run; a script (more than one statement) is compiled each time it runs. `conn.QueryContext`/`ExecContext` make a new `stmt` per call, which is the 20%. `sqlite.NewConnector(dsn)` is its seam for wrapping a connection.
- Neither modernc's `conn` nor its `stmt` checks argument values itself (`NamedValueChecker`, `ColumnConverter`), so `database/sql` converts arguments the same way on the prepared and unprepared paths.
- Every SQLite database gantry opens goes through `db.openSQLite` (`db/sqlite.go`): apps, `testkit.DB`, `mail`'s inbox. Nothing reaches the raw driver connection (`Raw`, backup, serialize).
- Postgres apps already have this: pgx's `database/sql` driver caches prepared statements by default.
- Uncommitted on `codex/social-login`: `db/sqlguard` opens its own pool through its own connector (`sqlguard.Open(dsn, guard)` over `&sqlite.Driver{}`), replacing `DB.Read` and `DB.Write`.

## Scope
Batch 1: SQLite connections opened by `db.Open` keep their compiled statements, so SQL an app runs again isn't compiled again. No app code, generator output or configuration changes.

Boundary: when `sqlguard` lands it should wrap this connector, not open a pool beside it, or development and production run different drivers. That's its change to make, on its branch.

## Design
`db/stmtcache.go`, unexported, in package `db`:

- `cachingConnector` embeds the connector from `sqlite.NewConnector(dsn)`; `Connect` wraps the connection. `openSQLite` passes it to `sql.OpenDB` in place of `sql.Open("sqlite", dsn)`. Nothing is registered globally.
- `cachingConn` forwards `Begin`, `BeginTx`, `Ping`, `ResetSession`, `IsValid`, `Prepare` and `PrepareContext` as they are. Its `QueryContext` and `ExecContext` look the SQL up in the connection's cache, prepare it with the driver on a miss, and run the `driver.Stmt`. `Close` closes every cached statement, then the connection.
- The cache is per connection: a map from the SQL text to an entry, in least-recently-used order. 256 statements; SQL over 16 KiB isn't cached. Both are constants, not options.
- A query's rows are wrapped (`cachedRows`, forwarding the five `ColumnType…` methods modernc's rows have); closing them frees the entry. An entry whose rows are still open is busy: the same SQL run again on that connection goes to the driver as it is, which gives it a statement of its own, closed with its rows. A busy entry is never evicted, and room is made before a new statement is compiled: a connection full of busy ones keeps nothing new, and new SQL goes to the driver as it is.
- No lock: `database/sql` holds a connection's mutex around every driver call on it, rows' `Next` and `Close` included.
- An error from running a statement leaves it cached: the driver resets it on every path.

## Contract pin
`db.Open("sqlite:…")`, `DB.Read`, `DB.Write` (`*sql.DB`): unchanged types and behaviour.

- **Inputs:** any SQL and arguments, as now. Cached when it's one statement, at most 16 KiB, and its entry isn't busy. A script, empty or comment-only SQL, and SQL that doesn't compile behave exactly as with the plain driver.
- **Authz:** none: in process.
- **Preconditions (a connection's states, all allowed):** idle; in a transaction; rows open on other SQL; rows open on the same SQL (a statement of its own).
- **Effects:** nothing durable. Memory: at most 256 statements a connection (the read pool, the writer, the version connection).
- **Errors:** the driver's own, unwrapped: `db.IsUnique` and `db.IsForeignKey` see what they saw.

## Templates filled
Concurrency (one connection, two uses of one statement):

```text
Writer A: a transaction's query on SQL S, its rows still being read
Writer B: the same transaction running S again inside that loop
Ordering / lock / CAS / uniqueness: the entry's busy flag, set when rows are handed out, cleared when they close; database/sql's per-connection mutex serialises the calls
Bad interleaving: B binds and steps the statement A's rows are stepping; A loses its remaining rows, or SQLite reports misuse
Expected end state: both result sets whole and right; B's statement closed with B's rows; S's entry still cached and free once A's rows close
Re-check under the lock/lease that mutates?: busy is read and set inside the driver call, under database/sql's connection mutex
Work under the lock (same store only): the driver's prepare, bind and step on this connection. Nothing foreign.
```

Crash and at-least-once: nothing here writes durably or runs from a queue.

## AC ↔ test map (Batch 1)
All in `db/stmtcache_test.go`. "Counted" means through a connector that counts the driver's `PrepareContext` and statement `Close` calls around the real driver: no counters in production code.

| AC | Test | Lens |
| --- | --- | --- |
| The same SQL run 100 times on a connection is compiled once (counted) | `TestStatementCachePreparesOnce` | Contract |
| Writes in `DB.Tx` (`INSERT … RETURNING` read as a row, and an `Exec`) are compiled once across 50 transactions, and every row is there | `TestStatementCacheInTransactions` | Contract (the second door) |
| Results, rows affected, last insert id and column types equal the plain driver's, for each argument kind (int, text, blob, NULL, bool, time) | `TestStatementCacheMatchesDriver` | Parity |
| The wrapped connection and rows implement exactly the optional `database/sql/driver` interfaces modernc's do | `TestStatementCacheKeepsDriverInterfaces` | Parity, Honest surface |
| The same SQL run again while its rows are open, in one transaction, gives both their full results | `TestStatementCacheSameQueryNested` | Concurrency |
| A script runs all its statements; empty and comment-only SQL do what the driver does; SQL that doesn't compile gives the driver's error and caches nothing | `TestStatementCacheScriptsAndErrors` | Contract (invalid input) |
| After a unique and a foreign-key violation (`db.IsUnique`, `db.IsForeignKey` still true), the same SQL with good values succeeds | `TestStatementCacheAfterConstraintError` | Crash & repair |
| After a query is cancelled, in its first step or between rows, the same SQL runs again and is right, and nothing kept on a connection the driver gave up stays open | `TestStatementCacheAfterCancel` | Crash & repair |
| A cached `SELECT *` sees a column added by another connection; after `DROP TABLE` it errors, and after the table is made again it works | `TestStatementCacheAfterSchemaChange` | Migrate & deploy |
| 1,000 different statements leave at most 256 open on the connection, the oldest closed first, a busy one never; SQL over 16 KiB is never cached (counted) | `TestStatementCacheBounded` | Scale |
| With all 256 kept statements busy, new SQL (a query and an `Exec`) runs uncached and right, nothing kept is closed under its rows, and once they close new SQL is kept again (counted) | `TestStatementCacheEveryStatementBusy` | Concurrency, Scale (from review) |
| A `PRAGMA` set, read and set again (cache_size, foreign_keys, user_version, busy_timeout), DDL, a savepoint and `EXPLAIN` answer as the driver's do, each time | `TestStatementCachePragmas` | Parity (from review) |
| SQL starting with a comment, as sqlc's does, is compiled once | `TestStatementCacheCommentedSQL` | Contract |
| Closing the `DB` closes every statement it compiled (counted: closes equal prepares) | `TestStatementCacheClosesStatements` | Re-entry |
| Over a driver whose connections or statements aren't modernc's shape, the first use says so; nothing runs quietly uncached | `TestStatementCacheUnknownDriver` | Contract, Signals |

Gate beside the map: `bin/go go test ./...` green (every package opens its SQLite through this), and `bin/go go test -race ./db`.

Evidence, not a gate: `BenchmarkRepeatedQuery` (cached against the plain driver, ns a query), and the challenge mix before and after on one core, with the scripts from 2026-10-13. Expected from the hand-prepared run: about 28,000 req/s where it was 21,700. If the wrapper gives less than half of that gain, stop and say so before going on.

## Batch 1: what was found (2026-10-13)
- Review of the first tip (a383f5c), my pass and Codex's independently: one defect, the same one. With every kept statement busy, making room after compiling a new statement closed the new one, and then ran it; the test written for it hung. Room is now made first. Codex found nothing else in busy reuse, the connection's lifecycle, arguments, errors, cancellation, schema changes, scripts or shutdown.
- Review of the second tip (07488ac), Codex: the eviction fix is right; one new claim, that a kept `PRAGMA cache_size` reads a stale value and a kept set does nothing. Disproved: `TestStatementCachePragmas` runs that sequence and it answers as the driver alone does, because SQLite marks every `PRAGMA` to be compiled again each time it's run (`sqlite3VdbeRunOnlyOnce`, at the top of `sqlite3Pragma`). No guard added; the test stays as the proof.
- All fifteen tests green; `bin/go go test ./...` green; `go test -race ./db` clean with `-skip TestSQLiteWithoutTempDir` (that test runs its own binary in an empty root, which a cgo build, as the race detector needs, can't do: as on `main`).
- A cache with no busy flag and no bound, tried first: `TestStatementCacheSameQueryNested` failed with SQLite's "bad parameter or other API misuse (21)", and `TestStatementCacheBounded` with 1,001 statements open.
- `BenchmarkRepeatedQuery`: 5.4 µs a query through the driver alone, 1.6 µs kept. The same allocations.
- The challenge mix on one M5 core, the feed app built against `main` and against this, three runs each, turn about: 21,870 / 21,680 / 21,820 req/s, and 26,110 / 26,150 / 25,920. A fifth more, with no failed requests, and the challenge's 42 checks pass. The hand-prepared run's 28,300 was a barer app (no transactions, no sqlc), so not the same thing measured.
- The driver's result for SQL with no statement in it (`""`, a comment) is nil, and asking it for rows affected panics, with or without the cache. Left as it is.
- A cancelled query can cost its connection: the driver reports an interrupted connection unusable until its statements finish, and database/sql drops it and opens another, which compiles its own statements. So a request cancelled mid-query (a visitor who left) costs a connection's opening and its kept statements. As before this change, less the statements; counted in Batch 2.

## Deploy notes
No migration and no setting. Release note: SQLite connections keep their compiled statements; nothing for an app to do.

## Batch 2: the standing benchmark, run by hand
`bin/bench` answers two questions about the checkout it's run in: what a request costs, layer by layer, and how many users one core carries. On this machine; the 100,000-user line is the challenge's box, which Batch 4 runs on.

### Design
- **`bench/feed`**, its own module, made with `gantry new feed --gantry ../..` and built against the checkout (a `replace` in its `go.mod`): the challenge's feed API (five endpoints, its schema, its errors) in a generated app with nothing taken out, so the jobs, mail and live a new app starts with are running, as they would be. Its seeds make the challenge's shape of data (50,000 users, 500,000 posts, about 2 million likes), the same rows every time.
- A build with the `bench` tag also listens on a second port for the bench alone: Go's profiler, and the process's CPU time and memory. An ordinary build has none of it.
- **`bench/`**, a module of its own for the tools, so what they need (the profile format) isn't something gantry requires, and so neither is in the module an app downloads:
  - `load`: requests of the challenge's mix (100 feed : 100 post : 15 like : 2 create) on a set number of connections, due at a set rate whether or not the server keeps up. A request's time runs from when it was due, so waiting for a free connection counts.
  - `report`: the pass line (p95 under 500 ms, p99 under 1 s, under 1% failed), the search for the highest rate that passes, users for a rate (the challenge's loop: 2.17 requests every 20.5 s), and a CPU profile's samples divided among layers.
  - `cmd/bench`: waits for the app, finds the limit, then runs at nine tenths of it while profiling, and prints the report (and writes it as JSON for Batch 3, with the profile beside it).
- **`bin/bench`**: Docker's part. Builds both in the toolchain image, runs the app on one core with 2 GB and a new database, runs `cmd/bench` on other cores.

A sample belongs to the layer of the innermost frame that isn't the Go runtime's or a syscall's: SQLite compiling (under the driver's prepare), SQLite running, `database/sql` and gantry's `db`, the network (`net/http`, `net`), JSON, gantry's `web`, the app, the runtime by itself (GC, the scheduler), and other.

### Contract pin
- `bin/bench [-quick]`, and `BENCH_CPUS` (0.1 to 1: the app's share of its core; anything else is refused): no other input. Output: the report on stdout, `bench/out/report.json`, and the measured run's CPU profile, `bench/out/report.cpu.prof`. Exit 1 when the app doesn't come up, no rate passes, or a request fails at the measured rate.
- `load.Run(ctx, Options{URL, Rate, Connections, Duration, Secret, Users})` → `Result{Due, Done, Failed, Latencies}`. Every request due is in `Done` or `Failed`; none is dropped.
- The bench port (`BENCH_ADDR`, bench builds only): `/debug/pprof/…`, `/debug/usage` → `{"cpu_us":…,"rss_bytes":…}`. No auth: it isn't in a build an app deploys, and `bin/bench` doesn't publish it.

### AC ↔ test map (Batch 2)
| AC | Test | Lens |
| --- | --- | --- |
| Load arrives at the asked rate, in the challenge's mix, on no more than the asked connections | `load.TestRunHoldsRateAndMix` | Contract |
| A request that waited for a free connection is as late as it was: time runs from when it was due | `load.TestRunCountsTheWait` | Signals |
| A wrong status and a failed connection are counted failed; due = done + failed | `load.TestRunCountsFailures` | Contract (unhappy), Signals |
| A profile's samples divide among the layers by their stacks, compiling SQL apart from running it; a stack no layer claims is "other"; the shares are the whole | `report.TestLayers` | Contract, Signals |
| The pass line: each of p95, p99 and failed just inside and just outside | `report.TestPasses` | Contract |
| The search finds a known ceiling within 5% | `report.TestLimitFindsTheCeiling` | Contract |
| A server that never passes has no limit, not a made-up one; one that passes at the top rate says "at least" | `report.TestLimitWithoutACeiling` | Contract (unhappy), Signals |
| Users for a rate, and back, by the challenge's loop | `report.TestUsers` | Contract |
| The feed API: every endpoint and every error in the challenge's table | `feed/app.TestPosts` | Contract, Authz |
| The seeds: the counts, the same rows each time, nothing added by a second run, no like of a user or post that isn't there | `feed/db/seeds.TestSeed` | Contract, Re-entry |
| An ordinary build serves nothing under `/debug` | `feed/app.TestNoDebugRoutes` | Authz |

Evidence, not a gate: `bin/bench` run here, its report in this file.

### Batch 2: what was found (2026-10-13)
`bin/bench`, on one M5 core, the app built against the statement cache:

```text
At 21,600 req/s, nine tenths of the limit, a request cost 38.4 µs of CPU:
  running queries                  21.3 µs   55%
  compiling SQL                     0.0 µs    0%
  database/sql and gantry's db      2.8 µs    7%
  the network                       8.8 µs   23%
  JSON                              1.7 µs    4%
  gantry's web                      0.5 µs    1%
  the rest of gantry                0.0 µs    0%
  the app                           0.6 µs    1%
  Go's runtime by itself            2.8 µs    7%
  other                             0.0 µs    0%
  p50 0.3 ms, p95 10.0 ms, p99 19.5 ms; 0 of 648,000 failed; most memory held 49 MB

The limit: 24,000 req/s, which is about 227,000 of the challenge's users, on this machine
  (p95 under 500 ms, p99 under 1 s, under 1% failed)
100,000 users are 10,585 req/s: 44% of it
```

- Before and after the statement cache, by `bin/bench` itself (the same bench and app, on `main`'s gantry and on this):

  | | `main` | with the cache |
  | --- | --- | --- |
  | The limit | 20,000 req/s (189,000 users) | 24,000 req/s (227,000 users) |
  | A request's CPU | 46.9 µs | 38.4 µs |
  | of it, compiling SQL | 6.7 µs | 0.0 µs |
  | of it, running queries | 22.0 µs | 21.3 µs |
  | 100,000 users, of the limit | 53% | 44% |

- `BENCH_CPUS=0.5 bin/bench`, the app held to half its core: the limit 13,500 req/s (128,000 users), a request 41.0 µs, and 100,000 users 78% of the limit. A share of a core stands in for a slower one, to ask "what if"; what the challenge's box is a share of, only a run on it says.
- The limit over four full runs: 24,000, 24,000, 25,000, 24,000 req/s (the search is good to 5%), and a request's cost over the last two: 37.5 and 38.4 µs. k6, flat out on the same app: 25,300. 100,000 users are 10,585 req/s: two fifths of this core. The challenge's box is slower by a factor nobody here has measured; under about 2.3 times, and it's past the line. Batch 4 measures it.
- A request's cost is measured at nine tenths of the limit, not half. At half (12,000 req/s) it was 45.6 µs, three runs within a percent, but a fifth of that is the app sleeping and waking between requests, and it lands in the network's share one run (39%) and the runtime's the next (34%). At nine tenths: 38.08, 38.19 and 38.11 µs in three runs, and the layers within two points of each other. So an app with time to spare pays about 8 µs a request for the waiting: nothing to do with the limit, and something to know about a quiet server's CPU.
- One run's layers, at half the limit, were unlike the five after it (queries 62%, JSON 12%, the network 11%), with the same total. Not explained, and not seen again; the profile is kept beside the report now, for the next one.
- The app with everything a new app has (jobs, mail, live) serves 25,300 req/s where the one with them taken out served 26,100: 3%.
- The database the seeds make serves as the challenge's own file does (25,400 and 25,100 req/s under k6), though it's never analysed or vacuumed and its write-ahead log is 245 MB from the seeding.
- Timers wake late: a load generator that sleeps until each request is due adds a millisecond or two to every time it reports (p50 1.8 ms where the answer took 0.1). One goroutine keeps the time and spins through the last of each wait.
- Random generators seeded 0, 1, 2 ... start out alike: eight connections seeded that way sent 49% feed reads where the mix has 46%. Seeds are spread.
- `gantry check --strict` on the app: nothing.
- Editing `bin/bench` while it runs breaks the run's last lines: bash reads a script as it goes.
- Review of the first tip (b94dc7b), Codex's ten findings and my pass, all held and all closed, each with its test where one could be written:
  - a feed with no posts in it left the load all feed reads, and still counted done: now it's a failed request, and posts are found however the JSON is laid out (`load.TestRunNeedsPostsInTheFeed`);
  - the percentiles were of the answered requests alone, where the challenge's are of all of them: a failed request's time counts (`load.TestRunTimesFailures`);
  - the profile was a second shorter than the run, and the CPU counted on either side of both: the profile is the run's seconds now, and the CPU from before both to after both;
  - two `bin/bench` at once took each other's app down: the second is refused;
  - the bench's own requests of the app (is it up, what has it used, its profile) had no time limit;
  - the warm-up failed an app that couldn't serve 1,000 req/s, which the search would have measured: only one that answers nothing is given up on;
  - the search gave up at 2 req/s without trying 1 (`report.TestLimitWithoutACeiling`);
  - the app's token check ignored `nbf` (`feed/app.TestTokenNotYetGood`);
  - nothing showed the bench's port needs the bench tag (`feed/cmd/feed.TestBenchPortNeedsTheTag`), or that the feed is right when it isn't empty, or what `/health` says (`feed/app.TestFeed`);
  - a cancelled run waited out its duration (`load.TestRunStopsWhenCancelled`), two processes seeding one database at once collided (`feed/db/seeds.TestSeedTwiceAtOnce`), and `report.Rate` had no caller: the report's last line is its use.
- Review of the second tip (907f402), Codex: four more, all held and closed. Two runs started at the same moment both got past the check for one running: making the Docker network is the lock now, and Docker makes a name once. At a rate of a request or less a connection, every request was a connection's first, and so a feed read: a feed is read before the run, and the mix is the mix from the first request (`load.TestRunMixesFromTheStart`). The profile ended before the last requests were answered: it runs a second longer. A cancelled run gave requests that were never due times below zero (`load.TestRunStopsWhenCancelled`).
- Not closed, for Batch 3: on a machine with few cores the load generator may be what gives out first, and the limit would read low. It needs a check of its own (what it can send to a server that does nothing).

## Later batches
3. The benchmark in CI: the change against its base in the same job, failing on a regression past the measured noise.
4. The 100,000-user run: the deployed path, proxy in front, on the modest server.
5. Database workers, and SQLite as pure Go, C and WASM measured inside them. This is where "not Go, if something else is better" is decided.
6. Generated JSON for generated row types.
7. Results kept until the database changes (`DB.Version`), switched off by itself when writes are frequent.

## Agent loop checkpoints
- Red tests for every Batch 1 row, reported, then on (this plan's approval is the gate).
- Batch 1 green, with the two gates: scotty-review on the tip.
- Ready: scotty-review again if the tip moved, the full suite, gofmt and vet on touched files.

## Decided
- The 100,000-user line (2026-10-13): gantry by itself on the challenge's box, 1 vCPU and 2 GB. Nothing else shares it.
- Read as: the app has the whole machine, and the load is 100,000 users' requests (about 10,600 a second of the challenge's mix) arriving on a few connections, as a proxy delivers them. 100,000 connections held open by the app itself is a different target: at today's 43 KB a connection that's 4.3 GB, and the challenge's service is allowed 65,535 descriptors. Say so if that's the one meant; it changes Batch 4, not Batch 1.

## Known remainder
- A transaction's `BEGIN` and `COMMIT` are run by the driver itself (`tx.go`), outside what this cache sees, and are compiled each time. Their cost is measured in Batch 2 and removed, if it matters, with the database workers (Batch 5).
