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
5. Update the rule catalog documentation and describe new strict failures in the
   release notes. Apps pick up rule changes when their pinned Gantry version is
   upgraded; exceptions stay explicit and reviewable.

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
`cfg.Development()` guard. The standard CLI uses the built-in registry; project
extensions require a compiled caller rather than arbitrary code in JSON.

Registries reject duplicate IDs, unknown severities and missing metadata or
checks. Invalid source offsets and rule execution errors fail the scan with the
rule and file named. Exception validation uses the supplied registry, so a
custom rule gets the same exact-file, reason-required exception policy.

## Current coverage

The current six rules cover assets, broad form selection, shared page shells,
raw file servers, development guards and environment fallbacks. Their exact
limits and exception syntax are in [the development guide](development.md).
Page-cache eligibility, image-pipeline adoption and render-blocking data fetches
are future coverage areas, not checks that exist today. Some will need runtime
measurement or richer analysis instead of static source matching.
