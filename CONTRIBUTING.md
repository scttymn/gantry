# Developing Gantry

For agents building applications with Gantry, start with the
[application guide](docs/agents.md). This page covers changes to the framework.

Run the framework tests in Docker with `bin/go go test ./...` and static checks
with `bin/go go vet ./...`. Generator changes must update the golden fixtures
and keep generated apps passing tests and Gantry Strict.

`db` has SQLite two ways: compiled from C (`db/sqlite_c.go`, gantry's own
binding) when the build has cgo, as an app's does, and translated to Go
(`modernc.org/sqlite`) when it hasn't, as `bin/go` and the `gantry` CLI do. They
answer the same (`TestSQLiteCMatchesTranslated` holds the C build to the
other), and a change to `db` passes both: `bin/go go test ./...`, then
`bin/go sh -c 'CGO_ENABLED=1 CGO_CFLAGS="-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY" go test -tags nodynamic,timetzdata,netgo,osusergo,sqlite_omit_load_extension,sqlite_fts5,sqlite_math_functions,sqlite_dbstat,sqlite_stat4 -ldflags "-linkmode external -extldflags -static" ./...'`
(statically linked, as production is: one test runs the binary in an empty
root). The tags and C flags are an app's Dockerfile's: without the tags a cgo
build has the translated SQLite, without the flags the C one doesn't start, and
`TestSQLiteCCompileOptions` fails on any other difference between the two.

## Performance

`bin/bench` measures the gantry in the checkout: it builds `bench/feed` (the
$12 server challenge's feed API, as a generated app) against it, runs it on one
core with 2 GB, and reports the highest rate of the challenge's requests that
passes its line and what a request costs in CPU, layer by layer. Nothing caps
the rate but the app. `bin/bench against main` measures the checkout and `main`
side by side and says whether a request costs more, less or the same; a pull
request's `bench` job runs that against its base, and fails on a regression.
`bin/bench -quick` is a look, not a measurement. The app and the tools are modules of their own, so their tests are
`GANTRY_DIR=bench bin/go go test ./...` and `GANTRY_DIR=bench/feed bin/go go test ./...`.
The plan, with what each batch measured: [`docs/plans/performance.md`](docs/plans/performance.md).

## Features and Gantry Strict

When adding a feature that improves application performance or development,
include its adoption path in the feature work:

- Update the default generators, shared examples and migration guidance.
- Add a focused Strict rule for detectable bypasses, naming the built-in solution
  and the benefit it preserves. If detection would be unreliable, document that
  limitation and the appropriate measurement or review instead.
- Test compliant apps, bypasses and legitimate alternatives. Make uncertain
  recommendations advisory; reserve strict failures for clear violations.
- Keep terminal and browser diagnostics on the shared rule registry, and document
  new failures in the release notes so apps can adopt upgrades deliberately.

See [Gantry Strict's purpose and extension contract](docs/strict.md). An
intentional exception needs a specific file, rule and reason; avoid silently
reimplementing a framework feature simply to satisfy a warning.
