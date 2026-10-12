# Developing Gantry

For agents building applications with Gantry, start with the
[application guide](docs/agents.md). This page covers changes to the framework.

Run the framework tests in Docker with `bin/go go test ./...` and static checks
with `bin/go go vet ./...`. Generator changes must update the golden fixtures
and keep generated apps passing tests and Gantry Strict.

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
