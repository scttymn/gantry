# Gantry: notes for agents

This is the Gantry framework. To build an app with Gantry, read
[docs/agents.md](docs/agents.md) instead.

- [CONTRIBUTING.md](CONTRIBUTING.md): how to run the tests and checks (in
  Docker, with `bin/go`), the second pass a change to `db` needs, and what CI
  runs on a pull request.
- [docs/strict.md](docs/strict.md): what Gantry Strict is for, and how a
  feature brings its generators, docs and Strict rule with it.
- Generators' templates are in `cmd/gantry/templates`. Golden files in
  `cmd/gantry/testdata/golden` pin what they write;
  `bin/go go test ./cmd/gantry -update` rewrites them.
