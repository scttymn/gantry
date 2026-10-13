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

## Later batches
2. The standing benchmark, run by hand: the feed app in the repo (`bench/feed`, built against the checkout), load on a few connections at a set arrival rate, and a report: CPU a request by layer, and users at the limit.
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
