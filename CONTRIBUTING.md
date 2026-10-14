# Developing Gantry

For agents building applications with Gantry, start with the
[application guide](docs/agents.md). This page covers changes to the framework.

Run the framework tests in Docker with `bin/go go test ./...` and static checks
with `bin/go go vet ./...`. Generator changes must update the golden fixtures
and keep generated apps passing tests and Gantry Strict.

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
