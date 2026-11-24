# Developing Gantry

For agents building applications with Gantry, start with the
[application guide](docs/agents.md). This page covers changes to the framework.

Run the framework tests in Docker with `bin/go go test ./...` and static checks
with `bin/go go vet ./...`. Generator changes must update the golden fixtures
and keep generated apps passing tests and Gantry Strict. `TestGolden`
(`cmd/gantry/golden_test.go`) pins what every generator writes and fails on a
template no case renders; `bin/go go test ./cmd/gantry -update` rewrites the
goldens. `TestGeneratorMatrix` runs every generator on SQLite and Postgres in
a new app, with its next steps done, and fails on a Strict finding, sqlc or
templ failing, `go vet`, the app's tests, or stale generated code. It builds
the apps only where templ and sqlc are installed, as in `bin/go`; elsewhere
it generates and scans them. A templ that isn't the pinned one fails it.

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

The versions gantry and its apps build with (Go, templ, the toolchain's and
Postgres's images, SQLite's C flags and build tags) are in
[`internal/pins`](internal/pins/pins.go), and `gantry new` writes them into an
app. The copies kept by hand (the Dockerfile, the workflows, `bin/bench`, this
page) are held to them by its test, which says where to change one.

Dependabot ([`.github/dependabot.yml`](.github/dependabot.yml)) bumps the Go
modules and the workflows' actions weekly. A dependency comes in one pull
request for every module that has it (`bench/feed` has gantry's, as indirect
requirements), and `modernc.org/sqlite` and `github.com/mattn/go-sqlite3` come
together: the two are one SQLite release (`TestSQLiteCVersionMatchesTranslated`),
at one version in every module (`TestModulesHaveGantrysSQLite`).
`ci.yml`'s first check, `Modules tidy`, names any module whose `go.mod` or
`go.sum` `go mod tidy` would change, and the command that fixes it.

What it leaves alone has copies it can't change, and is bumped by hand, each in
one pull request with all its copies:

- templ, Go's, sqlc's and Postgres's images: change the pin in
  `internal/pins/pins.go` (templ's is `go.mod`'s: `go get` it at the root and
  in `bench/feed`), then `bin/go go test ./internal/pins` names each copy to
  match, and `bin/go go test ./cmd/gantry -update` brings the generated apps,
  `bench/feed` among them, along. A new templ makes new views too:
  `GANTRY_DIR=bench/feed bin/go sh -c 'templ generate && go mod tidy'`
  regenerates `bench/feed`'s `_templ.go`, which must be the pinned templ's
  (`TestTemplCodeIsPinned`). Postgres stays on its major: a new one needs
  an app's data upgraded, and psql's client with it.
- `modernc.org/libc`: `modernc.org/sqlite` runs on the one its `go.mod`
  requires, and `go get modernc.org/sqlite@<version>` brings it, in each
  module that has it (`TestModulesHaveGantrysSQLite`).

Tests on Postgres take a server from `GANTRY_TEST_POSTGRES_URL`, which `bin/go`
passes in (`internal/testenv`), e.g.
`GANTRY_TEST_POSTGRES_URL=postgres://postgres:pw@host.docker.internal:5432/postgres?sslmode=disable bin/go go test ./...`.
Without it they skip, except under `CI=true`, where they fail.

Every pull request and push to `main` runs the `ci` workflow: both passes on
SQLite and Postgres, the cgo one again with `-race`, every module tidy, `go vet`, gofmt,
golangci-lint and govulncheck (`go vet`, golangci-lint and govulncheck on the
cgo build too, golangci-lint on the bench modules), shellcheck on every shell
script git tracks (`bin/shellcheck`, which finds them by their `#!` line or
name, an app's `bin/gantry` template and goldens among them), TLA+ model checking
on every spec in `spec/` (`bin/tlc`), a short fuzz smoke pass over the framework's
`testing.F` targets, the bench
modules' tests (below), and the release's build and its probe on a made-up
tag. Run
golangci-lint and govulncheck with
`bin/go go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<version> run ./...`
(`GANTRY_DIR=bench` or `bench/feed` before it for a bench module) and
`bin/go go run golang.org/x/vuln/cmd/govulncheck@<version> ./...`, at the
versions [`ci.yml`](.github/workflows/ci.yml) pins.

golangci-lint's linters ([`.golangci.yml`](.golangci.yml)) are staticcheck's
checks (`staticcheck.conf`'s, which gopls reads) and one for each bug class
gantry has had: errors dropped (errcheck), `rows.Err` and `Close`
(rowserrcheck, sqlclosecheck) and response bodies left open (bodyclose), with
`rowserrcheck`, `sqlclosecheck` and `bodyclose` on tests too, and three calls
with one right place (forbidigo).
GANTRY_ENV is read by `env.Development` and `env.Test` (`internal/env`),
forwarding headers by `web/proxies.go`, and HTTP goes through a client with a
timeout, never `http.DefaultClient`.
depguard keeps `devcheck` out of the packages an app links (#34), and new apps'
own `.golangci.yml` (including `bench/feed`'s) holds an app to its import
boundaries (#33) alongside `errcheck`, `bodyclose`, `rowserrcheck`,
`sqlclosecheck` and `staticcheck`. An exception names the
file, the linter and why, in `.golangci.yml` or a `//nolint:<linter> // why`.

`bin/ci` runs those checks here, step by step under the workflow's names, and
stops at the first that fails; `bin/ci --fast` leaves out the race detector and
the release's build. Its Postgres is `GANTRY_TEST_POSTGRES_URL`'s, or a
throwaway `postgres:17` it removes when it's done; with neither, it says loudly
that the Postgres tests skipped. Go's caches are one for every checkout
(`~/Library/Caches/gantry` on macOS, `~/.cache/gantry` elsewhere), cleaned once
the build cache passes 5 GB, though never while another checkout's `bin/ci`
is running (a clean under a build takes its files), and `bin/ci` won't start
with less than 20 GB free there. It needs Docker running, `git`, `jq`, `timeout` and `curl` on the PATH, and
checks for them before it starts; every Docker call it makes has a time limit.
Have every push run it first, once per clone:

```sh
git config core.hooksPath .githooks
```

The hook (`.githooks/pre-push`) runs `bin/ci` and blocks the push when it
fails. `bin/ci` checks the checkout, so the hook refuses a push of any commit
but `HEAD`, or from a tree with changes or untracked files; a push that only
deletes refs skips it.
`TestBinCIRunsTheCIWorkflow` fails when `ci.yml` has a step, tool version or
flag that `bin/ci` hasn't, so a step added to the workflow (shellcheck in #78,
golangci-lint in #79) has to be added to `bin/ci` too, under the same name.

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
`bench/feed` is held to what `gantry new` writes, apart from the files the
challenge needs of its own (`TestBenchFeedIsANewApp`): `go test ./cmd/gantry
-update` brings it along with a change to the generator.
The plan, with what each batch measured: [`docs/plans/performance.md`](docs/plans/performance.md).

## Fuzzing and formal verification

- **Go fuzz targets (`testing.F`):** `sign`, `web` (`Sent`, `Proxies`, `PageCache`),
  `images`, `devcheck` and `internal/featuremap` carry fuzz targets. `bin/ci` runs
  each for `-fuzztime=1s`; for a longer local run, pass e.g.
  `bin/go go test -fuzz=^FuzzPageCache$ -fuzztime=30s ./web`.
- **TLA+ specifications (`spec/`):** `spec/Jobs.tla` (fenced settles, lease expiry,
  bounded retries, atomic recurring ticks), `spec/PageCache.tla` (no personalized
  entries stored, no cross-visitor leak, no stale version served) and
  `spec/Auth.tla` (single-use first-install setup, no unverified social takeover,
  bounded concurrent reset sends, no revoked session active). Run `bin/tlc` to
  model-check every `spec/*.tla` (with its paired `spec/*.cfg`) in the pinned
  JRE container (`internal/pins.TemurinImage`) using the SHA-256-verified
  `tla2tools.jar` (`internal/pins.TLAVersion` / `TLASHA256`). Adding a new
  `spec/<Name>.tla` and `spec/<Name>.cfg` is picked up automatically by `bin/tlc`
  and CI.

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
