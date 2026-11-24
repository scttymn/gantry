# Gantry Strict

Gantry Strict helps applications use the framework's built-in features for
performance and faster development. Its purpose is to prevent apps from
rebuilding capabilities Gantry already provides or bypassing optimizations
without realizing it. It is an adoption check for Gantry's recommended patterns,
not just a collection of browser bug checks or a code-style linter.

As Gantry gains capabilities, Strict should gain corresponding checks where
reliable detection is possible. Asset caching and the pipeline are the starting
point. The same approach applies to page caching, image processing, shared
layouts, data loading, generators and future features. Applicability matters:
private pages must not be encouraged into a public cache, and custom download
routes are not necessarily replacements for the asset pipeline.

## One rule registry

`devcheck.DefaultRules()` is the catalog used by `gantry check`, strict CI checks,
and the development browser panel. `gantry check --rules` lists the installed
rules, their severity, purpose and guidance. Each entry declares:

- A stable rule ID used by reports and documented exceptions.
- The Gantry pattern it promotes and its performance or development benefit.
- Severity: a definite convention violation or contextual advice.
- File extensions to inspect and a detection function.
- Guidance pointing to the existing Gantry solution.

The scanner handles traversal, ignored directories, source locations,
deterministic ordering, errors and exceptions. Rules do not implement their own
output or strict-mode behavior. Go rules share a lazy syntax-tree parse per file.
There is no mutable process-global registration or dynamically executed project
configuration. Adding a built-in entry automatically reaches all consumers.

## Adding a check for a new feature

1. State which built-in capability the application should use and why. Show a
   concrete bypass and the recommended implementation, including the cases where
   the recommendation does not apply.
2. Implement a focused rule and add it to `DefaultRules()`. Prefer syntax or
   framework metadata to text matching. Use advice when context is needed; do not
   turn guesses into strict failures.
3. Test the bypass, recommended pattern, legitimate alternatives, source location
   and severity. Keep diagnostics deterministic and avoid network access or file
   mutation. Do not put secrets or entire source contents in a diagnostic.
4. Update generators and examples to use the feature. Generated apps must pass
   Strict. Add a migration example so existing apps can adopt it.
5. Regenerate the development guide's rule table with
   `go test ./devcheck -update` (the test fails until you do) and describe new
   strict failures in the release notes. Apps pick up rule changes when their
   pinned Gantry version is upgraded; exceptions stay explicit and reviewable.

For every new performance or development feature, record whether Strict coverage
was added. If reliable detection is unavailable, document the gap and how to
measure or review it instead. Do not claim a clean scan proves good performance.

## Compiled extensions

Additional checks can use the same engine without changing Gantry's scanner:

```go
rules := append(devcheck.DefaultRules(), devcheck.Rule{
    ID:         "project-legacy-helper",
    Severity:   devcheck.Advice,
    Purpose:    "Adopt the project's Gantry-backed helper",
    Guidance:   "Replace the legacy helper with the shared Gantry implementation.",
    Extensions: []string{".js"},
    Check: func(source *devcheck.Source, report func(int, string)) error {
        if offset := strings.Index(source.Text, "legacyHelper("); offset >= 0 {
            report(offset, "") // byte offset; empty message uses Guidance
        }
        return nil
    },
})
findings, err := devcheck.ScanWithRules(projectRoot, rules)
```

This small text-match example illustrates the API, not a built-in recommendation.
An HTTP stack outside Gantry’s router can pass the same registry to
`web.DevelopmentWithRules(handler, projectRoot, logger, rules)` inside its
`cfg.Development()` guard. A build with `-tags nodevcheck`, as a new app's
production build is, has no `web.DevelopmentWithRules`. The standard CLI uses
the built-in registry; project extensions require a compiled caller rather than
arbitrary code in JSON.

Registries reject duplicate IDs, unknown severities and missing metadata or
checks. Invalid source offsets and rule execution errors fail the scan with the
rule and file named. Exception validation uses the supplied registry, so a
custom rule gets the same exact-file, reason-required exception policy, and an
exception for it that matches nothing is reported stale.

## Current coverage

[The development guide](development.md#gantry-defaults-and-strict-mode) lists
the current rules, generated from `DefaultRules()`, with their limits and
exception syntax. Page-cache eligibility, image-pipeline adoption and
render-blocking data fetches are future coverage areas, not checks that exist
today. Some will need runtime measurement or richer analysis instead of static
source matching.

A rule may follow a name out of its file. `cached-scope` and
`cache-set-cookie` start at a route the page cache serves and follow its
handler, often in another package, and what that calls. The scan's packages
are parsed once, when a rule first needs one, and a name is followed only
when its declaration says what it is: a parameter's type, `x := T{...}`, a
field, a function's one result, the type an alias stands for. A handler the rules can't follow that way
isn't reported, so a clean scan doesn't prove a cached page is the same for
everyone. The page cache guards that at runtime too (it keeps no response
that sets a cookie, and serves no request with a session), and
`bin/gantry verify` tests it. These two rules have no golangci-lint
counterpart: `forbidigo` and `depguard` match a call or an import wherever it
is, and can't tell that the handler it's in is cached.

Strict reads gantry too: `TestGantryPassesStrict` scans the framework, where
the rules that aren't about an app's layout apply, and `bench/feed`, an app
`gantry new` wrote, with no findings but the exceptions in each one's
`.gantry-check.json`. Checks only the framework needs are tests in their own
package, as `jobs`' `TestJobUpdatesAreFenced` is.

The [feature map](development.md#the-feature-map) is one such check. A route
with no feature fails an app's tests, not `check --strict`. Routes are what the
running router has, including those a package's `Routes` adds, and Strict reads
source without running the app. `testkit.Verify` checks the map against
`Router.Routes` in the app's `TestFeatures`. `bin/gantry verify` runs that test
to check each feature's pages, its caching headers, and that cached pages
aren't shown across users. So coverage and leaks are enforced by
`TestFeatures`, which `bin/gantry test` runs, and by the `bin/gantry verify`
step in a new app's CI. `check --strict` and the warnings panel never read
`test/features.yaml`. An app without the file passes them as before
(`TestCheckWithoutFeatureMap`). Only `bin/gantry verify` asks for the map, and
it says how to add one ([an app made before the feature map](development.md#an-app-made-before-the-feature-map)).

## Comments

Comments go stale, nothing checks them, and they give a workaround a place to
explain itself instead of being fixed. Names, types and tests are checked by
the compiler and CI, so that's where intent goes. The `comments` rule holds
gantry and its apps to that:

- Gantry's own Go has doc comments on exported names (functions, methods,
  types, constants, variables, struct fields and interface methods) and on
  package clauses, and no other comment: none inside a function, none
  trailing a line, none on an unexported name, none in a `_test.go` file
  (a test isn't documented API: its name says what it checks).
- An app, and every template in `cmd/gantry/templates` that `gantry new` and
  the generators write it from, has no comments at all, doc comments and
  tests included.
- `TODO`, `FIXME`, `HACK` and `XXX` are reported anywhere, doc comments
  included: a plan is an issue, linked from the pull request. Commented-out
  code is reported too: git keeps the old version.
- What stays is what a tool reads: `//go:` directives, `//nolint:<linter> //
  <reason>` (a bare `//nolint` is reported), sqlc's `-- name:` and goose's
  `-- +goose Up`, `Down`, `StatementBegin` and `StatementEnd`, cgo's preamble,
  a script's `#!` line and `go.mod`'s `// indirect`. A license header is the
  one other exception the policy allows, when gantry adopts one (#37); the
  packages `importmap pin` vendors keep theirs, and aren't read.

Go is parsed with its comments, and each comment group belongs to what it
documents or the function it's in. Templates, SQL, `.templ`, YAML, shell,
JavaScript and CSS are read a line at a time, with each one's comment markers:
a marker counts at the start of a line or after a space, so `https://` and
`"#fff"` aren't comments. Go that says it's generated (sqlc's, templ's) isn't
read. `bench/` is modules of its own, and `bench/feed` is checked as the app it
is.

The rule takes no exceptions in `.gantry-check.json`: an exception for it
fails the scan. There's no baseline either; gantry and `bench/feed` pass it
(`TestGantryPassesStrict`), and every generated app does
(`TestGeneratorMatrix`, `TestGolden`).
