# Checking Gantry application patterns during development

[Gantry Strict](strict.md) encourages use of the framework’s performance and
development features. This guide describes the checks currently implemented;
`bin/gantry check --rules` lists their purposes and recommended fixes.

`bin/gantry dev` checks the project before starting Houston. Warnings do not
block the server. Run `bin/gantry check` again after edits, or use
`bin/gantry check --strict` in CI (exit 1 when unsuppressed violations are found; advice does not fail CI).

The checks read local `.html`, `.templ`, `.js`, and Go source files, without starting a
server, executing JavaScript, or making network requests. They skip dependencies,
built assets, Git metadata and test fixtures, and do not follow symlinks.

- **asset-url:** a literal local script or stylesheet URL can keep serving old
  code after a deployment. Use `assets.Path`, `assets.Styles.Tag`, or
  `assets.ImportMap.Tag`. Gantry fingerprints content and sends immutable cache
  headers. External URLs, dynamic template expressions and fingerprinted file
  names are left to their own pipelines. A hand-maintained `?v=1` is still warned
  about: someone has to remember to change it.
- **form-scope:** `document.querySelector('form')` in JavaScript selects the first
  form on the page. Adding a sign-out form in the header can silently change
  what it selects. Use an explicit form ID or a form-scoped Stimulus controller.

These are source heuristics, not a JavaScript type checker or an audit of CDN
settings. Comments and intentionally literal URLs can produce warnings; dynamic
expressions can hide mistakes. Review findings rather than treating a clean run
as proof. Checks run at startup, not continuously during Houston's rebuilds.

## Gantry defaults and strict mode

<!-- rules: generated from devcheck.DefaultRules() by go test ./devcheck -update -->

| Rule | Severity | Purpose | Guidance |
| --- | --- | --- | --- |
| `asset-url` | Violation | Use Gantry's asset pipeline for cache invalidation, compression and reusable assets | local script or stylesheet uses a literal URL; use assets.Path/Styles/ImportMap so deployments replace cached copies; document an intentional alternative |
| `form-scope` | Advice | Reuse form-scoped behavior safely as shared navigation evolves | document.querySelector('form') can select a navigation form; use a form ID or a Stimulus controller scoped to that form |
| `shared-layout` | Advice | Keep asset loading and page enhancements in the shared layout | standalone page shell outside app/shared; prefer the shared layout so navigation, assets and development diagnostics stay consistent |
| `raw-assets` | Advice | Avoid rebuilding asset serving outside Gantry's optimized pipeline | raw file server bypasses Gantry asset handling; use assets.Routes for scripts/styles, or document this route's separate purpose |
| `development-guard` | Violation | Keep development-only middleware (source scans, timing, reload) out of production | web.Development, web.DevelopmentWithRules, web.Timing and web.Reload are development-only: call them inside a positive cfg.Development() guard, or document a custom equivalent guard with an exception |
| `environment-default` | Violation | Use Gantry's production defaults unless development is explicitly selected | GANTRY_ENV should default to production; opt into development explicitly |

<!-- /rules -->

Strict mode fails on violations. Advisory findings require context and stay
visible in both the terminal and browser without failing CI. The Go checks use
syntax trees and imported package names, so import aliases work and comments
are not treated as calls. Generated templ Go and test Go files are skipped.
The guard rule recognizes the generated positive `cfg.Development()` shape;
custom equivalent guards require review and an exception. This checks specific
conventions, not every possible path through a program. It cannot prove that a
custom layout is wrong or that a data fetch delays rendering; those remain
review/performance-measurement questions.

`bin/gantry check --json` prints the same scan as one JSON object, for tools
and agents. Its field names are stable:

```json
{
  "violations": 1,
  "advisories": 0,
  "findings": [
    {"file": "app/editor.html", "line": 2, "rule": "asset-url", "severity": "error", "message": "local script or stylesheet uses a literal URL; ..."}
  ]
}
```

`findings` is in the order the text lists them, and empty, not null, when
there are none. `line` is 0 for a finding about a file as a whole, and
`severity` is `error` (a violation) or `advice`. With `--strict` it still exits
1 on a violation, after printing. `check --rules --json` is the catalog: an
array of objects with `id`, `severity`, `purpose` and `guidance`.

For an intentional departure, add an exception to `.gantry-check.json` in the
project root. A new app has the file, with no exceptions:

```json
{
  "requireIssue": true,
  "exceptions": [
    {
      "rule": "raw-assets",
      "file": "app/downloads.go",
      "reason": "This authenticated route serves exports, not scripts or styles (#214)."
    }
  ]
}
```

Exceptions match exactly one file and rule, apply to terminal and browser
checks, and require a reason. Wildcards, unknown rules, unknown config fields,
and empty reasons are rejected. Keep exceptions narrow and review this file
alongside code changes. They suppress all findings of that rule in the named
file; line numbers are intentionally not used because edits move lines.

An exception that suppresses nothing, because the file was fixed, moved or
deleted, or an upgrade stopped the rule firing there, is reported as
`stale-exception` advice on `.gantry-check.json`. Remove it: left in place, it
would hide the next violation of that rule in that file. It is advice so that
an upgrade doesn't fail CI.

A reason should point at where the exception was decided: an issue (`#123`) or
a link to an issue or decision record (`https://...`). With `"requireIssue":
true`, as a new app has, the scan fails on a reason without one. Without it, as
in a file from before the setting existed, each such reason is reported as
`exception-reason` advice instead.

## Routes and the feature map

`bin/gantry routes` lists the app's routes as its router has them, by running
the app's own `routes` command (`myapp routes`) in its container, or on this
machine with `--local`. It's the app's answer rather than a reading of
`app/routes.go`, so a route a package adds (auth's `Routes`) or one only
development has (the mail inbox) is in it. `--json` prints an array of
`web.Route`, whose field names are stable:

```json
[
  {"method": "GET", "path": "/admin/posts/{id}", "scope": "/admin", "filters": ["auth.(*Auth).Required"], "cached": false, "mounted": false, "constrained": false}
]
```

`path` is the whole path, its scopes' prefixes and its host included;
`method` is empty for a route of any method. `filters` are the scope's
pipeline, in order, named by their functions (a closure's name is the
compiler's). `cached` is a `Cached` route the page cache serves; `mounted`, a
plain `http.Handler` from `Mount` (files, a mounted package); `constrained`,
a route only a `Constraint`'s requests reach. A Go test has the same list from
`Router.Routes`.

### The feature map

`test/features.yaml` says what the app's features are: the routes each is made
of, who it's for, and pages to check it by. A new app has one, with its home
page, `/up` and `/live`:

```yaml
features:
  - name: posts
    routes:
      - /posts
      - /posts/
    role: public
    cacheable: false
    visit:
      - path: /posts
        selector: h1
```

- `routes` are as `bin/gantry routes` lists them: `GET /posts`, or `/posts` for
  any method. One ending in `/` is every route under it too, as a ServeMux
  pattern is: `/posts/` has `GET /posts/{id}` and `DELETE /posts/{id}`. In a
  `[list]`, quote a route with `{` in it.
- `role` is `public`, `signed-in` or `admin`. Each can see what the one before
  it can.
- `cacheable` says its routes are `Cached` and its pages are the same for
  everyone its role lets in.
- `visit` lists pages to request, each with the status it answers (200 when
  left out) and a CSS selector for an element the page has.

`app/features_test.go`'s `TestFeatures` passes the app's router to
`testkit.Verify`, so `bin/gantry test` fails when:

- a route is in no feature. Routes from `Mount` (files, the development inbox)
  are exempt.
- a cacheable feature has a route that isn't `Cached`, or a `Cached` route is
  in a feature that isn't cacheable.
- a feature's role has no way to sign in (below).

`bin/gantry verify` runs that test alone, verbosely: in the app's container, or
on this machine with `--local`. The app runs in the test's process, on a test
database, through testkit's browser. For each feature's visits, as each role
it can sign in as:

- A role the feature lets in gets the visit's status and selector, and
  everything the page refers to loads (`testkit.Links`).
- A role before the feature's is turned away: any answer but a 2xx.
- On a cacheable feature, the page comes from the page cache: an `ETag` and a
  `Last-Modified`, and a 304 when the browser sends the `ETag` back.
- Before anything else requests a cacheable page, user 0 of each signed-in role
  fetches it, then user 1 of that role and a public visitor do. If either is
  shown what only user 0's pages show, a signed-in visitor's copy was cached
  for everyone, and the run fails. Each role does this at the page's URL with
  a `gantry-verify=<role>` query added, a cache key of its own. So each role's
  user 0 renders the page rather than getting another role's copy, and a leak
  only admins trigger is caught too.

`g resource` and `g auth` print their feature among their next steps, to paste
under `features` once its routes are routed. A namespaced resource's role is
whoever its scope's filter lets in, which the generator can't know.

#### Signing in as a role

A new app has no sign-in, so `TestFeatures` passes `testkit.Roles{}` and the
feature map can have only public features. After `g auth`, give it a
`testkit.SignIn` for each role the app has. Each one signs a `testkit.Visitor`
in as the nth (0 or 1) of two test users of that role, and returns what only
that user's pages show, such as their email, for the cache check. Returning
`""` skips the cache check for that role:

```go
testkit.Verify(t, rt.Handler(), rt.Routes(), "../test/features.yaml", testkit.Roles{
	testkit.SignedIn: func(v *testkit.Visitor, n int) string {
		email := fmt.Sprintf("member%d@example.com", n)
		// create the user (a fixture, or the app's own query), then:
		v.Get("/login").Submit(`form[action="/login"]`, map[string]string{"email_address": email, "password": "…"})
		return email
	},
})
```

How a role signs in is the app's: its users, its sign-in form, and what makes a
user an admin.

#### An app made before the feature map

Copy a new app's `test/features.yaml` and `app/features_test.go`, then:

- split `App.Handler` in `app/routes.go` into `Router`, which returns the
  `*web.Router`, and `Handler`, which returns `a.Router().Handler()`;
- add the `routes` case and function from a new app's `cmd/<app>/main.go`.

`bin/gantry test` then lists the routes the map is missing.

## Browser warnings

Gantry's router automatically injects a full-width bottom warnings panel when
`GANTRY_ENV=development` and findings exist. Existing apps receive it when they
upgrade Gantry; their templates need no changes. The panel starts collapsed.
Opening it reduces the scrollable content area above it instead of covering the
page. It uses native details/summary and works without JavaScript.

The findings are project-wide, not limited to the current page. Reload after a
fix to rerun the checks. No panel appears when the scan finds no issues.

Set `GANTRY_DEV_WARNINGS=false` and restart development to disable the panel and
source scan. The generated Compose configuration passes this setting through.
CLI checks and timing remain available. Production and test environments do not
activate diagnostics. Apps with a custom HTTP stack outside Gantry's router can
opt into `web.Development` explicitly inside a development guard.

A new app's Dockerfile builds production with `-tags nodevcheck`, so the
deployed binary contains no source scanner at all; the dev stage keeps it.
Under the tag `web.Development` is `web.Timing` alone and
`web.DevelopmentWithRules` doesn't exist. The test stage compiles the app with
the tag too, so `bin/gantry test` fails on such a call before a deploy would.
An app that calls `web.DevelopmentWithRules` passes it devcheck's rules, so it
links devcheck whatever the tag; leave `nodevcheck` out of its Dockerfile.

An existing app adopts the tag by adding this line to its Dockerfile's build
stage, after `FROM toolchain AS build`, and
`GOFLAGS="$GOFLAGS,nodevcheck" go build ./...` to its test stage's command,
before `go test`:

```dockerfile
ENV GOFLAGS="${GOFLAGS},nodevcheck"
```

Both extend the toolchain stage's `-tags=` list, so they work only while
`-tags=` is the last flag in its `GOFLAGS`. Otherwise, spell out the whole
`-tags=` list with `nodevcheck` added.

Injection happens before Gantry compresses the response. Only complete HTML
pages with a body are transformed; streams, fragments, encoded responses,
upgraded connections, ranges and pages larger than 2 MiB pass through unchanged.
Transformed pages are not cached. The panel's styles are a stylesheet at
`/_gantry/development.css`, from the app itself, so the app's
[Content-Security-Policy](security.md#content-security-policy) lets them in. The
disable setting is available for layouts that do not work well inside the
development content area.

## Reload on save

Under `bin/gantry dev`, open pages reload themselves when you save. Like the
panel, it comes with Gantry's router when `GANTRY_ENV=development`; templates
need no changes.

Each page knows the version of the code that rendered it: the server's boot
ID and a count of template text changes. It follows a stream at
`/_gantry/reload` that says the current version, and reloads when that differs.

- **Go, CSS, JavaScript, SQL, or a template's Go code:** templ's watcher
  rebuilds and restarts the server. The stream reconnects to the new server,
  and the page reloads. A build that fails leaves the page as it was; it
  reloads once a build succeeds.
- **A template's text only:** templ rewrites the file it keeps the text in,
  without restarting. The server notices within a fraction of a second, and
  the page reloads.
- **Data:** rows changed by the console, another tab or a job change no code,
  so nothing reloads. Pages that should follow their data use the `live`
  package's streams, in production too.

The script (`/_gantry/reload.js`) is added at the end of every complete HTML
page, with the same limits as the panel. Only visible pages hold a stream, so
open tabs don't use up the browser's connections to the app; a tab shown again
reconnects and reloads if the code changed while it was hidden. A reload
discards unsaved form input. A page reloaded by hand while the server is
restarting shows Houston's proxy error; reload it again once the server is up.

Set `GANTRY_DEV_RELOAD=false` and restart development to turn reloading off;
the generated Compose configuration passes it through. Production and test
environments never reload. Other HTTP stacks can opt into `web.Reload(handler)`
inside a development guard.

## Timing

The router also enables request timing automatically in development. Other HTTP
stacks can opt into `web.Timing(handler, logger)` inside a development guard.

Browser Network tools show a `Server-Timing` entry named `gantry`: elapsed time
until the response headers are committed. Debug-level request logs separately
record full handler duration, including streamed responses. Enable debug logging
to see those logs. The middleware does not buffer responses, and leaves existing
Server-Timing entries intact. Do not enable it in production by default.

This measures server work, not network latency, JavaScript execution or paint.
Use the browser Network waterfall and Performance panel to look for a chain like
HTML → JavaScript → initial-data fetch → visible editor. Compare a first visit
with a warm-cache navigation. A fast server can still have a slow-feeling editor.

## Editor example: render the initial state and scope enhancements

Use the generated layout, which already includes the fingerprinted styles and
import map. Load data in the GET controller and pass it into the template; do
not show an empty editor that must fetch the same data again after JavaScript
loads. Keep authentication and write validation on the server.

For example, an editor template can receive the current shop name directly:

```templ
package editor

import "your/module/app/shared/layout"

templ Page(shopName string) {
    @layout.Layout(layout.Page{Title: "Edit shop"}) {
        <nav aria-label="Account">
            <form action="/logout" method="post">
                <button type="submit">Sign out</button>
            </form>
        </nav>
        <form id="shop-form" action="/admin/shop" method="post"
            data-controller="shop" data-action="input->shop#changed">
            <label>
                Shop name
                <input name="name" value={ shopName } required/>
            </label>
            <button type="submit">Save shop</button>
            <p data-shop-target="status" role="status"></p>
        </form>
    }
}
```

The controller loads the name from the database and renders
`web.Render(w, r, http.StatusOK, Page(shop.Name))`. The POST route validates and
saves the form, then redirects back to the editor. The form works without custom
JavaScript; Turbo in the generated layout enhances navigation and submissions.

An optional `assets/js/controllers/shop_controller.js` enhances only this form:

```js
import { Controller } from "@hotwired/stimulus"

export default class extends Controller {
  static targets = ["status"]

  changed() {
    this.statusTarget.textContent = "You have unsaved changes."
  }
}
```

The generated import map discovers the controller. Do not add a literal script
URL or a page-wide submit handler. If a richer editor needs initial JSON, use a
JSON-aware escaping helper such as `templ.JSONScript`, never concatenate user
input into a script or cast it to trusted HTML/JavaScript.

Before shipping, exercise Sign out from every authenticated page, verify new
asset URLs after a script edit, and check that the initial editor does not need
an extra API request to become useful.
