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

| Rule | Severity | Default being checked |
| --- | --- | --- |
| `asset-url` | Violation | Content-versioned local scripts and styles through the asset pipeline |
| `development-guard` | Violation | Calls to `web.Development` and `web.Timing` inside a positive `cfg.Development()` guard |
| `environment-default` | Violation | The config helper's `GANTRY_ENV` fallback is `production` |
| `shared-layout` | Advice | Full HTML page shells belong in `app/shared`, rather than individual app views |
| `raw-assets` | Advice | Review `http.FileServer`/`FileServerFS` routes that bypass the asset pipeline |
| `form-scope` | Advice | Form enhancements target an explicit form rather than the page's first form |

Strict mode fails on violations. Advisory findings require context and stay
visible in both the terminal and browser without failing CI. The Go checks use
syntax trees and imported package names, so import aliases work and comments
are not treated as calls. Generated templ Go and test Go files are skipped.
The guard rule recognizes the generated positive `cfg.Development()` shape;
custom equivalent guards require review and an exception. This checks specific
conventions, not every possible path through a program. It cannot prove that a
custom layout is wrong or that a data fetch delays rendering; those remain
review/performance-measurement questions.

For an intentional departure, commit `.gantry-check.json` in the project root:

```json
{
  "exceptions": [
    {
      "rule": "raw-assets",
      "file": "app/downloads.go",
      "reason": "This authenticated route serves exports, not scripts or styles."
    }
  ]
}
```

Exceptions match exactly one file and rule, apply to terminal and browser
checks, and require a reason. Wildcards, unknown rules, unknown config fields,
and empty reasons are rejected. Keep exceptions narrow and review this file
alongside code changes. They suppress all findings of that rule in the named
file; line numbers are intentionally not used because edits move lines.

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

Injection happens before Gantry compresses the response. Only complete HTML
pages with a body are transformed; streams, fragments, encoded responses,
upgraded connections, ranges and pages larger than 2 MiB pass through unchanged.
Transformed pages are not cached. A strict CSP can block the panel's inline
styles; terminal checks remain available. The disable setting is available for
layouts that do not work well inside the development content area.

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
