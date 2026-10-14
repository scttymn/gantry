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

## Batch 3: the benchmark on every change
A pull request is measured against its base, in one job, on one machine: `bin/bench against REF`. No number is the line: the base is. It fails when the change makes a request cost more, past what the runs themselves differ by, and it says how high each side goes.

### Design
- **`bin/bench against REF [-quick]`** builds the bench's app twice, against the checkout and against REF's gantry (a worktree of it; the app and the bench are the checkout's), and runs both on core 0, each with its own seeded database. An app nobody is asking anything of takes next to none of the core.
  1. Each side's limit, and its layers at nine tenths of it, as `bin/bench` alone finds them (shorter trials: 10 s).
  2. Then both at one rate, nine tenths of the lower limit, turn about, three times each: a request's CPU.
  3. `bench/cmd/compare` reads the reports and writes the answer as Markdown (the job's summary): the limits, the cost, the layers, and the verdict. It exits 1 on a regression.
- **The verdict** (`report.Compare`): each side's cost is the median of its runs, and the noise is the wider of the two sides' spreads (highest less lowest, over the median). A change within the noise, or within 3%, is "the same". Past it, slower fails and faster is said. A side with requests failed at a rate the other carried is slower, whatever its CPU. A limit a tenth below the base's fails too (each is found to 5%).
- **The load giving out before the app** would make a limit that's the bench's, not the app's, and hide a faster gantry. It's told from the load's side: the bench used over eight tenths of the cores it had (`report.LoadBound`). Such a limit isn't compared. An app that left much of its own core unused at nine tenths of its limit (`report.Idle`) was stopped by something that isn't its CPU, the load or its own waiting, and the report says that too, as what it is.
- **`.github/workflows/bench.yml`**: on a pull request that touches more than docs, `bin/bench against origin/<base>`, its Markdown to the job summary.
- `cmd/bench` gains `-rate` (measure at this rate, no search), `-trial` and `-for` (how long), and `-cpus` (the app's share of its core, for Idle).

### Contract pin
- `bin/bench against REF [-quick]`: REF is a commit of this repository. Output: Markdown on stdout, progress on stderr, the runs' reports in `bench/out/`. Exit 1: a regression, a side that doesn't come up or passes at no rate, or a REF that isn't a commit.
- `report.Compare(base, head []Run) (Comparison, error)`: an error with fewer than three runs a side, or a run with no requests done: never a verdict from nothing.

### AC ↔ test map (Batch 3)
| AC | Test | Lens |
| --- | --- | --- |
| The same cost within the runs' spread, or within 3%, is the same; more is slower and fails; less is faster | `report.TestCompare` | Contract |
| Runs that differ by more than the change does don't fail it: it's the same, and the noise is said | `report.TestCompare` | Signals |
| A greater share of requests failed than the base's, by more than one in a thousand: slower, whatever its CPU, and whether or not the base failed some | `report.TestCompare` | Contract (unhappy) |
| Fewer than three runs a side, or a run with nothing done, is an error, not a verdict | `report.TestCompareNeedsRuns` | Contract (unhappy), Signals |
| A limit a tenth under the base's fails; within it doesn't; the top rate passing on both is "at least" | `report.TestCompareLimits` | Contract |
| The load used over eight tenths of its cores: its limit is the bench's own | `report.TestLoadBound` | Signals |
| An app using under six tenths of its share of the core at nine tenths of its limit is idle; a share of half a core is judged against the half | `report.TestIdle` | Signals |
| A limit the load gave out at, on either side, or one that's "at least", isn't a fall | `report.TestCompareLimits` | Contract |
| A side's runs are its numbered reports and no other file, each at a rate, and all at one | `cmd/compare.TestSide` | Contract (invalid input) |
| The Markdown has both sides' limits and users, the cost and its change, the layers, and the verdict's sentence | `report.TestMarkdown` | Contract |

Evidence, each run here and its output kept in this file: the checkout against itself is the same; this branch against `main` (no statement cache) is faster; a gantry made slower on purpose fails. And the workflow's first run, on its own pull request.

### Batch 3: what was found (2026-10-13)
`bin/bench against REF`, on one M5 core, three times, each about seven minutes:

| The checkout | against | Verdict | Exit |
| --- | --- | --- | --- |
| this branch | itself | "The same: a request's CPU differs by −0.6%, inside the 3.0% that can be told here." Limits 24,000 and 25,000 req/s. | 0 |
| this branch | `main`, which has no statement cache | "Faster: a request costs 12.9% less CPU (the runs themselves differ by 0.9%)." 46.8 and 40.8 µs at 18,000 req/s; limits 20,000 and 24,000, +20%. | 0 |
| this branch with the cache keeping nothing, on purpose | this branch | "Slower: a request costs 16.3% more CPU (the runs themselves differ by 0.2%)." Limits 25,000 and 20,000, −20%. | 1 |

- The runs of one side differ by under a percent here (0.2%, 0.6%, 0.9%): the 3% floor is what decides, on this machine. A shared runner will be noisier, and says by how much each time.
- Both sides run at one rate, nine tenths of the lower limit, so the faster side is further from its own limit there and has more of its core to spare: against `main` the cost reads 13% less where each at its own nine tenths reads 18% less. The limits' row has the whole gain. The summary says so.
- Two apps on one core, one of them idle, measure as one does alone: the base's and the head's numbers here are the ones `bin/bench` alone gave.

- **The job's first run, on GitHub's runner** (an AMD EPYC 7763, four cores; the job took seven minutes): this branch against `benchmark`, which is the same gantry.

  | | `benchmark` | this branch |
  | --- | --- | --- |
  | The limit | 5,750 req/s (54,000 users) | 5,750 req/s (54,000 users) |
  | A request's CPU, at 5,175 req/s | 186.0 µs | 186.1 µs |

  "The same: a request's CPU differs by +0.1%, inside the 3.0% that can be told here." The six runs differ by 1.6%: a shared runner is steady enough that the 3% floor decides there too.
- **That's the first measure on a cloud's core, and it's the one that bears on the goal.** A request costs 186 µs there where it costs 38 µs here: 4.9 times. One of those cores carries 54,000 of the challenge's users, with the statement cache: 100,000 are 184% of its limit. The earlier guess, that the challenge's box might be within 2.3 times of this laptop's core, was wrong by half. To carry 100,000 on a core like that, a request has to cost under about 95 µs there: half of what it does.
- Where it goes there: running queries 79 µs (43%), the network 60 µs (33%), JSON 11 µs (6%), the runtime 10 µs (5%), gantry's web 4 µs (2%). The network's share is a third on a cloud's core, where it's under a quarter here: syscalls cost more in a virtual machine. Queries and the network are the two things between here and 100,000.
- Review of the first tip (312aefb), Codex's ten findings, all held and closed:
  - the job passed whatever the bench found: a step's default shell has no `pipefail`, so `tee` had the last word. The step says `shell: bash`, which has;
  - the head could still be seeding, on the base's core, while the base's limit was looked for: both are up before either is measured, and the one not being measured is paused;
  - a base that failed any requests hid a head that failed many: it's the share failed that's compared, and one lost in hundreds of thousands isn't a verdict;
  - "starved" took an app waiting on its disk for a load that gave out: the load's own CPU says whether it did, and an app that left its core unused is said to be that, no more;
  - a limit the load gave out at, or one that's "at least", was compared as if it were the app's;
  - a run killed while the load was on left the network, which is the lock, behind: every container of the bench's is removed first;
  - a file such as `base.1.backup.json` was read as a run, and runs with no rate got past the check that both sides ran at one;
  - 3% more, to the digit, failed where "within 3%" is the same.
- Review of the second tip (9b1d66e), Codex: five more, all held. The bench still ended on any failed request, so the comparison never saw the shares it was changed to compare: a run is given up on only when nothing was done (or, alone, when nine tenths of the limit doesn't pass the line). The load's timekeeper spins on purpose, and counting that would call a load on one core out of CPU when it wasn't: a run says how long it spun (`load.Result.Spun`), and that's left out. One in a thousand more failed, to the digit, was a regression by a float's hair. The failed row gave both sides the head's total. And the load's cores are whole ones, as `bin/bench` gives it: a share of cores would need more than `GOMAXPROCS` to judge.
- After them, the branch against itself again: "The same: a request's CPU differs by −0.2%", limits 25,000 and 25,000.

## Batch 4: on a real server, deployed (2026-10-13)
The bench's app on its own (`scttymn/twelve-dollar`, private), built against the statement cache, deployed with Houston to `twelve-dollar.svnmns.com`: one CPU and 2 GB in its `compose.yml`, beside four live apps. The server is a virtual machine of 8 cores on an AMD Ryzen 9 7950X, 15 GB. Its seeds run once it's listening, as a deploy's health check doesn't wait for them (and a release hook can't run them: it's run with `sh`, which the production image doesn't have).

| From | Through | The limit | The app's core |
| --- | --- | --- | --- |
| a laptop on wifi, by Starlink | Cloudflare, the tunnel, kamal-proxy | 1,150 req/s | under a fifth used |
| the laptop on wifi, by fibre | the same | 1,550 req/s | a quarter used |
| the server itself | nothing: the app's container | 14,500 req/s (137,000 users); nearer 13,000 sustained | all of it |
| the server itself | kamal-proxy, by the app's name | 11,000 req/s (104,000 users) | all of it |
| the server itself | out to Cloudflare, and back by the tunnel and kamal-proxy | 7,000 req/s (66,000 users); 8,000 doesn't pass | all of it |

- **On this server, one core carries the 100,000, when the requests are the bench's.** The app alone: a request costs 78 µs (three runs at 13,050 req/s: 77.8, 77.8, 74.5), where it's 38 µs on the M5 and 186 µs on GitHub's EPYC. 100,000 users are 10,585 req/s: 73% of the limit found, about 81% of what it holds (at nine tenths of the limit found the core was full, and one run of three went slow: 20 s trials read a little high against a quota).
- **A request that came the whole way costs the app far more: about 133 µs, not 78.** Behind kamal-proxy alone it's 91 µs (11,000 req/s with the core full), and through Cloudflare, the tunnel and the proxy, asking for gzip as a browser does, the core is full at about 7,500. What's added on the way is headers (Cloudflare's dozen, the proxy's), and the answer compressed (15 µs, measured straight to the app at 6,000 req/s: 122 and 137 µs). So through its real front door this app, on one core, carries 66,000 of the challenge's users, not 100,000. Seventy per cent more a request than the bench sends is gantry's to look at: it's the request the bench doesn't make.
- **The front costs more than twice what the app does.** At 7,000 req/s the tunnel (cloudflared) had a core and a half and kamal-proxy one, for the app's one: about 215 µs and 145 µs a request, to the app's 133. All told a request through the front door is half a millisecond of this server's CPU, so its eight cores are about 16,000 req/s for everything on it.
- **Asked for more than it can pass, the tunnel takes the machine.** At 10,000 req/s, with the app's core the limit at 7,500, cloudflared was using six and a half cores, and next to nothing was getting through (the proxy at a tenth of a core, the app less). Every other app on the server would be starved by one app's overload. Nothing in front says no. That's Houston's to fix: a limit on the tunnel's CPU, at the least.
- **There was no cap at 1,600 req/s. That was a laptop's wifi.** The first two rows were loaded from a laptop, whose network now and then loses packets: a probe of a live app's `/up` every second and a half found one answer in seventy that took 7 s, all of it opening the TCP connection (three tries lost: 1, 2 and 4 s). At a high rate on few connections, a lost packet holds up everything behind it. The tunnel's own log has no error in any of it. Loaded from the server, the same front door passes 7,000.
- The same probe is why a live app's odd slow answer (1 to 8 s, with the server loaded and with it idle) isn't the server's either.
- At a rate well under its limit a request costs the app much more than at it: 122 µs at 6,000 req/s straight to the app, 78 µs at 13,000. An app with time to spare sleeps and wakes, and under a CPU quota in a virtual machine that's dear. It's why the app's CPU at a middling rate (85% of its core at 6,000 through the front) doesn't say where it gives out.
- A release hook can't seed or migrate a gantry app: Houston runs it with `sh`, which the production image doesn't have. And a bench that asks a proxy for an app by name has to ask for `/up` by that name as well: the first run through the proxy waited, sending nothing, for a 404 to turn into a 200.

## Batch 5: database workers, and SQLite three ways
In the challenge's own test (every user its own connection, straight to the app) gantry is stopped by memory, not CPU: about 45 KB an open connection, 2 GB at some 44,000 of them. 8 KB of that is `net/http`'s buffers, measured. The rest is taken to be the connection's goroutine stack, grown by SQLite's deep calls and kept: taken to be, from the totals and from stacks being copied in the CPU profile, and not yet measured. And "running queries" is half of a request's CPU, on an SQLite that's C translated to Go.

Three steps, each short, each reported as it lands.

### 5a. What a connection costs, measured
A test in the bench's app opens idle keep-alive connections to the real handler, each having made one request, and reads the runtime's own account: stack and heap, a connection. It's the instrument for 5c, and it settles what the 45 KB is.

### 5b. SQLite three ways
The app's own queries (the feed, a post, a like, a new post), on the seeds' data, each run from one goroutine as a worker would run them, with the statement kept: on `modernc.org/sqlite` (C translated to Go: today's), on SQLite compiled from C (`mattn/go-sqlite3`, cgo), and on SQLite compiled to WebAssembly (`ncruces/go-sqlite3`, on the wazero gantry already has). Time a query, memory, and how deep each leaves the goroutine's stack. An experiment in the bench's module: it changes nothing in gantry, and its numbers decide what does.

### What 5a and 5b found
Measured on the M5, in the toolchain's container (linux/arm64).

**5a: the stack isn't it.** `feed/app.TestMemoryPerConnection`, 2,000 idle connections of each kind:

| A connection that | Stack | Heap |
| --- | ---: | ---: |
| has asked for nothing | 4.1 KB | 10.1 KB |
| has had an answer with no query | 6.7 KB | 9.6 KB |
| has read the feed and a post | 8.3 KB (7.6 KB once collected) | 9.5 KB |

So the guess this batch started from is wrong: SQLite's calls leave a connection's goroutine at 8 KB, not 35, and a worker for them would save 2 to 4 KB of some 40. What a connection holds is 18 KB, and 10 of it is `net/http`'s, taken before the connection has asked for anything (its two 4 KB buffers, and the connection's own). The container's 40 to 45 KB a connection is, most likely, that 18 KB with the collector's headroom over it (by default the heap may grow to twice what's live before it's collected) and the kernel's socket: to be confirmed by a run with the memory limit set.

**5b: compiled C is the fastest, by a lot on the feed.** `bench/sqlite3way`, each through database/sql on one connection with its statement kept, on the challenge's database, all three SQLite 3.53.4:

| Query | Translated to Go (today) | WebAssembly | Compiled C |
| --- | ---: | ---: | ---: |
| The feed, 20 rows | 28.9 µs | 22.1 µs | 14.0 µs |
| A post | 4.95 µs | 4.6 µs | 3.7 µs |
| A like, committed | 80 µs | 78 µs | 73 µs |
| A new post, committed | 44.5 µs | 43.7 µs | 38 µs |
| A request of the challenge's mix | 21.5 µs | 18.1 µs | 13.6 µs |

"WebAssembly" is `ncruces/go-sqlite3` v0.35.6, which no longer runs on wazero: its WebAssembly is translated to Go ahead of time. "Compiled C" is `mattn/go-sqlite3` v1.14.52, which crosses from Go to C twice a row (a step, then every column at once: read in its source), some 45 times for the feed: under 2 µs of its 14, so a binding of gantry's own that crossed once a query would gain little over it. A request costs 38 µs here today, so compiled C as it stands is about a fifth off a request.

**The headroom is confirmed, and it's most of it.** The challenge's own `load.js`, every user a connection of its own, the entry on one M5 core with 2 GB, 30 s up and a 60 s hold (not the 5 minutes the score is):

| Users | Go's memory limit | | p95 | p99 | Failed | Memory |
| ---: | --- | --- | ---: | ---: | ---: | ---: |
| 50,000 | none (today) | fails: out of memory | | | 27.85% | 2 GB |
| 55,000 | 1,500 MiB | passes | 3.1 ms | 14 ms | 0 of 570,213 | 1.42 GB |
| 64,000 | 1,640 MiB | passes | 186 ms | 717 ms | 0 of 648,470 | 1.54 GB |

With the limit set the collector keeps the heap under it, where by default it lets it reach twice what's live: 40 KB a connection becomes 24 to 26. At 64,000 it passes, but its p99 is near the line (1 s) where at 55,000 it's 14 ms, on 48% of the core: the slowest request took 7.1 s, which is what three lost connection attempts take (1 + 2 + 4 s), so the queue of connections waiting to be accepted is the first suspect, and the collector working against the limit the second. Not yet told apart.

The challenge gives an entry 65,535 descriptors, so 64,000 users (the chart's top two) is as many as anything can hold: that's the ceiling, and the question for gantry is how near it comes.

### 5c. Workers: not built
The design was a goroutine for each database connection, so a request's goroutine kept a shallow stack. 5a measured what it would save: 2 to 4 KB of a connection's 40. It isn't built. What 5a and 5b point at instead is 5d to 5f.

### 5d. The memory limit, from what the process may have
At start (`web`'s `init`: every app has it, and none has to change), gantry finds the least the process may have (its cgroup's `memory.max` and `memory.high`, every cgroup's above it, cgroup v1's `memory.limit_in_bytes`, and the machine's memory, which is the limit for a server of its own, as the challenge's is) and tells the runtime four fifths of it (`debug.SetMemoryLimit`). The fifth is for what the runtime doesn't count: the kernel's side of every connection, and SQLite's own memory. It's a ceiling the collector works to stay under, not a reservation: a heap far below it is collected as it always was. `GOMEMLIMIT`, when set, is left as it is. The router says what was set, once, when its handler is built.

Contract pin: `memoryLimit(memoryFiles) (limit, container int64)`: 0, 0 when nothing can be read, or nothing read is a number from 1 byte to 1 TiB (`max`, and v1's "none", 2^63 rounded to a page, aren't).

### 5e. The slow tail at 64,000
At 64,000 the p99 is 717 ms where at 55,000 it's 14 ms. One run with the kernel's counters of dropped connection attempts read before and after, the collector's trace on, and k6's own CPU watched, tells the three suspects apart: the accept queue, the collector, the load generator.

### 5e found: it's the load, mostly
At 64,000 users, with the kernel's counters read before and after, the collector traced, and a probe of its own (a new connection and `GET /feed` every 50 ms, on a core neither the app nor k6 has):

| | k6 says | the probe says |
| --- | ---: | ---: |
| p95 | 216 ms | 16 ms |
| p99 | 859 ms | 75 ms |
| the slowest | 2.7 s | 276 ms |

No connection attempt was dropped (`ListenOverflows`, `ListenDrops`: 0 before and after), the collector took 4% of the core (69 collections in two minutes, 630 MB live, 512 MB of it stacks), and k6 had 10 to 11 GB and spiked to 13 cores. So most of the tail is the load generator's, on this machine, holding 64,000 users; the app's own p99 at 64,000 is some 75 ms, against the challenge's 1 s.

### 5f. SQLite compiled from C
`db` has SQLite compiled from C when the build has cgo (`db/sqlite_c.go`), and translated to Go (`modernc.org/sqlite`, `db/sqlite_go.go`) when it hasn't. A build without cgo (the `gantry` CLI, cross-compiled; `bin/go`; an app whose Dockerfile still says `CGO_ENABLED=0`) is what it was. `gantry new`'s Dockerfile turns cgo on, keeps Go's build cache between deploys (SQLite's C is 16 s to compile), and links the binary statically, so the production image stays an empty one.

The C build is gantry's own binding, about 600 lines, over the SQLite that `mattn/go-sqlite3` compiles (imported for its C alone). That driver was tried first, behind an adapter, and it answers differently in ways an app would meet: `$1, $2` bound by where they appear, not their number (gantry's own SQL is written so, for Postgres); a statement asked as a query not run until its rows are read; a script's statements before the last skipped when asked as a query; times written in their own zone; and a kept statement's columns stale after the schema changes. The adapter for all that was growing past the binding it would have saved. The binding answers as `modernc.org/sqlite` does, by construction (its rules were read from that driver's source and are written the same way), and by test: `db.TestSQLiteCMatchesTranslated` opens two databases made the same, one with each, asks both the same things (every type of value, times in each format either reads, `?`, `?NNN`, `$NNN` out of order, names, missing and extra arguments, scripts in each shape, empty SQL, a statement asked as a query and not read, results, and errors), and the answers must be equal. What it does that the driver it replaces can't: each connection is opened without SQLite's lock (database/sql has one goroutine at a time use a connection), SQLite doesn't count its allocations under a lock all connections share (`TestSQLiteCUncounted`), and a row is read in one call from Go to C.

Measured with `bin/bench against` the commit before this batch (M5, the app on one core):

| | before | `mattn/go-sqlite3`, behind the adapter | gantry's own binding |
| --- | ---: | ---: | ---: |
| The limit | 24,000 req/s | 30,000 req/s (+25%) | 31,000 req/s (+29%) |
| A request's CPU, at 21,600 req/s | 38.1 µs | 32.7 µs (−14%) | 31.7 µs (−16%) |
| of it, running queries | 20.6 µs | | 9.0 µs |

The C build is compiled with the features the translated one has (full-text search, math functions, `dbstat`, `STAT4`): build tags, in the toolchain's `GOFLAGS` so dev, test and production builds all have them, and `db.TestSQLiteCCompileOptions` fails on any difference between the two builds that isn't listed there with its reason.

Review (my pass and Codex's): seven findings, all fixed, each with a test on both builds where the two should agree.

| Finding | Fix | Test |
| --- | --- | --- |
| A prepared statement run again while its last run's rows were open reset it under them: the old rows read freed memory | the old rows are cut off, and say so | `db.TestSQLiteCRunAgain` |
| A query's context, done after the query answered, interrupted whatever the connection ran next | the context watches a query until its first row, as the translated driver's does; after, database/sql closes the rows | `db.TestSQLiteCancelAfterAnswer` |
| Closing rows dropped what ending the statement came to (a RETURNING's write that fails at its end) | Close returns it | `db.TestSQLiteReturningFailsAtClose` |
| The C build lacked FTS5, math functions, `dbstat`, `STAT4` | build tags | `db.TestSQLiteFeatures`, `db.TestSQLiteCCompileOptions` |
| A NUL in SQL looped for ever | SQL ends at a NUL | `db.TestSQLiteNUL` |
| A read-only transaction took the write lock | `BEGIN` for one | `db.TestSQLiteReadOnlyTxWhileWriting` |
| The memory limit read only the root cgroup: a service's, a nested container's, a server's own memory missed | the process's cgroup and every one above it (v1 and v2), and the machine's memory | `web.TestMemoryLimit` |

A second review (Codex's) confirmed five of those, and found the rest incomplete, and more. All fixed:

| Finding | Fix | Test |
| --- | --- | --- |
| A statement closed while its rows were open freed what they read | its rows are cut off, and say so | `db.TestSQLiteCStmtClosedUnderRows` |
| A run cut off by the next lost what ending it came to | the cut rows' Close returns it | `db.TestSQLiteCRunAgainKeepsItsError` |
| A script dropped an earlier statement's failure at its end (the translated driver does too) | the script fails: the C build is the stricter | `db.TestSQLiteCScriptFailsOnAnEarlierStatement` |
| A cgo build without the tags had the C build without its features | the C build is chosen by the tags; any other build has the translated SQLite | `go test` with cgo and no tags: `db.TestSQLiteFeatures` passes |
| Differences an app's SQL would meet were allowed (`soundex()`, `LIKE` on a blob, `sqlite_offset()`, `sqlite_dbpage`, geopoly) | compiled in, by `CGO_CFLAGS`; the C build doesn't start without them | `db.TestSQLiteFeatures`; the start-up check, seen to refuse a build with the tags and not the flags |
| An app's `interface{ Code() int }` found no code on the C build's errors | `Code()` | `db.TestSQLiteErrorCode` |
| SQL of 2 GiB or more would overflow C's int | refused | none: by reading (a test would need 2 GiB) |
| cgroup paths read as if every mount's root were the hierarchy's; v1's limits above the process's missed; a machine past 1 TiB taken for no limit | the process's cgroups mapped through `/proc/self/mountinfo`, limits walked up on v1 and v2 alike, only v1's "none" is none | `web.TestMemoryLimit`, `web.TestMemoryLimitNone` |

A failed commit now rolls back, as the translated driver's does, so database/sql never takes back a connection still in a transaction. `web.TestFailureMidStream` races under `-race` on the commit before this batch too: not this batch's (spun off as a task of its own).

### AC ↔ test map (Batch 5)
| AC | Test | Lens |
| --- | --- | --- |
| A connection that has served a request costs what the test says it does, in stack and in heap | `feed/app.TestMemoryPerConnection` | Scale, Signals |
| The least the process may have becomes four fifths of it: its cgroup's `memory.max` or `memory.high`, any above it, cgroup v1's, the machine's memory | `web.TestMemoryLimit` | Contract |
| No limit (`max`, v1's 2^63, nothing to read), a file that isn't a number, a path out of the cgroup root: nothing set | `web.TestMemoryLimitNone` | Contract |
| `GOMEMLIMIT` set is left alone | `web.TestMemoryLimitLeavesGOMEMLIMIT` | Config |
| The router says what it set, once | `web.TestMemoryLimitLogged` | Signals |
| Every gantry test passes on both builds, statically linked on the C one (as CI now runs them) | `go test ./...`; `CGO_ENABLED=1 go test -tags netgo,osusergo,sqlite_omit_load_extension -ldflags "-linkmode external -extldflags -static" ./...` | Parity |
| The C build answers as the translated one: values, times, arguments, scripts, empties, results, errors | `db.TestSQLiteCMatchesTranslated` | Parity, Contract |
| A script, or SQL that's only comments, isn't kept: each run compiles it, as the translated driver does | `db.TestStatementCacheScriptsAndErrors`, `db.TestStatementCacheCommentedSQL` (both builds) | Contract |
| SQLite doesn't count allocations under its shared lock | `db.TestSQLiteCUncounted` | Scale |
| `IsUnique` and `IsForeignKey` on both | `db.TestConstraintErrors` (both builds) | Parity |
| The bench's report puts the C calls in running and compiling SQL | `report.TestLayersCompiledSQLite` | Signals |
| A generated app builds with cgo, static, and runs on the empty image | `gantry new` goldens; the bench's app built that way and run `FROM scratch` as 65532 (it set its own memory limit, 1.6 GiB of 2, and served `/feed`, a post and `/health`) | Migrate & deploy |

Evidence, each a short run: `bin/bench against` the commit before 5f; the challenge's own script at 55,000 and 64,000 with nothing set by hand.

## Batch 6: the write-ahead log, cut back
The deployed app's disk was 515 MB: 299 MB of database and 246 MB of write-ahead log. Seeding wrote the whole database in one transaction, the log grew to hold it, and SQLite reuses a log from its start but never gives back what it grew to.

Every connection gantry opens now sets `journal_size_limit` to what SQLite's own checkpoints fill: `wal_autocheckpoint` pages, each a frame of the database's own page size and its 24-byte header, after the log's 32-byte header (4,120,032 bytes for 4 KB pages). SQLite cuts the log back to that when it starts it again, at the next write after a checkpoint: after a seed, a bulk import, a migration rewriting a table. No app sets a number. And `db.Migrate`, gantry's own big writes, empties the log when it's done (`wal_checkpoint(TRUNCATE)`), whether or not the app writes again soon.

| AC | Test | Lens |
| --- | --- | --- |
| After 16 MB written in one transaction, the next write leaves the log at no more than the limit | `db.TestSQLiteLogShrinksAfterABigWrite` (both builds) | Scale |
| The limit follows the database's page size (4 KB and 8 KB), on the writer and the readers | `db.TestSQLiteLogLimitFollowsThePageSize` (both builds) | Contract |
| Migrations leave no log | `db.TestMigrateLeavesNoLog` (both builds) | Contract |
| An app's statements still go through the statement cache: what a connection runs setting itself up isn't the app's | `db.TestStatementCachePreparesOnce` (counts from the connection's opening) | Parity |

## Later batches
6. Generated JSON for generated row types.
7. Results kept until the database changes (`DB.Version`), switched off by itself when writes are frequent.
8. The request as it arrives through a proxy and Cloudflare: its headers and its compressed answer, in the bench, and what they cost.

## Agent loop checkpoints
- Red tests for every Batch 1 row, reported, then on (this plan's approval is the gate).
- Batch 1 green, with the two gates: scotty-review on the tip.
- Ready: scotty-review again if the tip moved, the full suite, gofmt and vet on touched files.

## Decided
- The 100,000-user line (2026-10-13): gantry by itself on the challenge's box, 1 vCPU and 2 GB. Nothing else shares it.
- Read as: the app has the whole machine, and the load is 100,000 users' requests (about 10,600 a second of the challenge's mix) arriving on a few connections, as a proxy delivers them. 100,000 connections held open by the app itself is a different target: at today's 43 KB a connection that's 4.3 GB, and the challenge's service is allowed 65,535 descriptors. Say so if that's the one meant; it changes Batch 4, not Batch 1.

## Known remainder
- A transaction's `BEGIN` and `COMMIT` are run by the driver itself (`tx.go`), outside what this cache sees, and are compiled each time. Their cost is measured in Batch 2 and removed, if it matters, with the database workers (Batch 5).
