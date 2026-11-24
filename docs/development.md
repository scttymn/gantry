# Checking Gantry application patterns during development

[Gantry Strict](strict.md) encourages use of the framework’s performance and
development features. This guide describes the checks currently implemented;
`bin/gantry check --rules` lists their purposes and recommended fixes.

`bin/gantry dev` checks the project before starting Houston. Warnings do not
block the server. Run `bin/gantry check` again after edits, or use
`bin/gantry check --strict` in CI (exit 1 when unsuppressed violations are found; advice does not fail CI).
`bin/gantry check --generated` is a separate check: templ's and sqlc's Go,
generated again in a copy of the app, must be what the app has, so a hand
edit to generated code fails it rather than being lost to the next generate,
and so does a generated file whose `.templ` or `.sql` is gone. It runs the
`templ` and `sqlc` on the machine, which should be the versions the app's
Dockerfile installs: a file another version made is reported as such.

Upgrading: this release builds with templ v0.3.1118, so an app's go.mod gets
it too. Code an older templ made doesn't compile against it (it calls
`templ.JoinURLErrs` and `templ.ResolveAttributeValue`, which are gone). Change
the app's Dockerfile to `go install github.com/a-h/templ/cmd/templ@v0.3.1118`,
then run `templ generate` and commit what it rewrites;
`bin/gantry check --generated` names any file still made by the old one.

The checks read local `.html`, `.templ`, `.js`, Go source and `.sql` files, without starting a
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
- **js-timer:** imperative timers (`setTimeout`, `setInterval`,
  `requestAnimationFrame`, `requestIdleCallback`) often patch over reactivity or
  polling problems and accumulate into hard-to-debug lag. Use Turbo Streams,
  `live` updates or Stimulus events instead, or document a deliberate timer in
  `.gantry-check.json`.
- **raw-dom-html:** assigning `innerHTML` or `outerHTML`, or calling
  `insertAdjacentHTML` or `document.write`, bypasses server-side escaping.
  Render HTML with templ and Turbo Frames/Streams, or set `textContent`.
- **stimulus-listener:** adding a `document` or `window` event listener in a
  Stimulus controller's `connect()` without removing it (or aborting an
  `AbortController`) in `disconnect()` leaks listeners across Turbo navigations.
  Clean it up in `disconnect()` or use a Stimulus `data-action`.

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
| `js-timer` | Violation | Use Turbo Streams, live updates or Stimulus events instead of imperative timer workarounds | setTimeout, setInterval, requestAnimationFrame or requestIdleCallback in JS; use Turbo Streams, live updates or Stimulus events, or document an intentional timer with an exception |
| `raw-dom-html` | Violation | Keep server-rendered escaping by avoiding raw HTML injection in JavaScript | innerHTML, outerHTML, insertAdjacentHTML or document.write bypasses escaping; render a Turbo Stream or Frame from templ, or set textContent |
| `stimulus-listener` | Violation | Clean up global event listeners when a Stimulus controller disconnects so Turbo navigations don't leak them | document.addEventListener or window.addEventListener in a Stimulus controller's connect() without matching removeEventListener or AbortController abort() in disconnect(); clean it up in disconnect() or use a Stimulus data-action |
| `shared-layout` | Advice | Keep asset loading and page enhancements in the shared layout | standalone page shell outside app/shared; prefer the shared layout so navigation, assets and development diagnostics stay consistent |
| `raw-assets` | Advice | Avoid rebuilding asset serving outside Gantry's optimized pipeline | raw file server bypasses Gantry asset handling; use assets.Routes for scripts/styles, or document this route's separate purpose |
| `development-guard` | Violation | Keep development-only middleware (source scans, timing, reload) out of production | web.Development, web.DevelopmentWithRules, web.Timing and web.Reload are development-only: call them inside a positive cfg.Development() guard, or document a custom equivalent guard with an exception |
| `environment-default` | Violation | Use Gantry's production defaults unless development is explicitly selected | GANTRY_ENV should default to production; opt into development explicitly |
| `import-boundary` | Violation | Keep the app's layers one way (routes.go → resources → services → models, and app/shared for all but models, which import no other app package) so each package is reusable and testable alone | app/models imports no web, templ or other app package; app/services no web or resource; app/shared no resource; a nested resource not its parent; nothing under app/ imports the routes package |
| `view-db` | Violation | Keep pages cacheable and testable without a database: controllers load, views draw | a view uses models.New, models.Queries, db.DB or a DB's Read/Write pool; load the data in the controller's action and pass plain values to the component |
| `raw-html` | Violation | Keep templ's escaping, which stops a visitor's text running as the page's HTML or script | templ.Raw and templ.JSUnsafeFuncCall skip escaping; write { value } (templ.URL for links), or document the escaping with an exception |
| `env-outside-config` | Violation | Read the environment once, in config, so it's checked at start and tests set it as a value | os.Getenv in app/; add a field to config.Config and give it to what needs it |
| `background-work` | Violation | Use jobs for work after the response, so a deploy or a crash doesn't lose it and failures are retried | a go statement, time.Sleep or time.AfterFunc in app/; enqueue a job (EnqueueIn, EnqueueAt), broadcast with live, or use sync.WaitGroup.Go for work the request waits for |
| `http-client` | Violation | Bound every outbound request, so a slow server can't hold requests and jobs forever | http.DefaultClient, http.Get, Head, Post and PostForm have no timeout; send with an &http.Client{Timeout: ...} of your own |
| `sql-outside-models` | Violation | Keep SQL in app/models, where sqlc checks it against the schema | raw SQL or a *sql.DB in app/ outside app/models; write the query in app/models/<table>.sql and call models.New(c.DB.Read).Name(ctx), or models.New(tx) in c.DB.Tx |
| `cross-origin-bypass` | Violation | Keep cross-site request protection on every state-changing route but the callbacks that prove themselves | web.CrossOrigin's bypass patterns exempt every method and request they match; route the one callback with rt.CrossOriginCallback("POST /path", h), and have it check its own proof (OAuth state, a signature) |
| `tx-defer` | Violation | Never leave a transaction open, holding its connection (on SQLite, the one writer) | a transaction is begun and committed with no deferred Rollback, so an early return or a panic leaves it open; defer tx.Rollback() right after Begin (it does nothing once committed), or use db.DB.Tx |
| `read-then-write` | Advice | Save edits without losing one made meanwhile | a Get on the Read pool, then an Update or Delete on the Write pool, in one function; read and write in one c.DB.Tx with models.New(tx) |
| `get-mutates` | Advice | Keep GETs safe to repeat, as prefetching browsers and crawlers assume | a GET handler (Index, Show, New, Edit, or a GET route's function) writes to the database; route it as POST and send it from a button in a form |
| `inline-download` | Advice | Send uploads as downloads, so an uploaded HTML or SVG file can't run as the app's own page | http.ServeContent or ServeFile with no Content-Disposition serves a file inline; send uploads with web.Download or storage's routes, and serve inline only types checked to be images |
| `cached-scope` | Violation | Cache only pages that are the same for every visitor, so no one is shown a page drawn for someone else | a route the page cache serves (Cached, PageCache.Handler, or Resources wrapped in the cache's Handler) has a handler that reads auth.Current, SignedIn, a cookie or the flash; draw that part where it isn't cached, or add the route with Handle |
| `cache-set-cookie` | Violation | Set cookies and sessions on routes whose handler runs for every visitor | a route the page cache serves has a handler that sets a cookie, the flash or a session, which a cached copy never sends; set it on a route added with Handle |
| `comments` | Violation | Keep intent in names, types and tests, which the compiler and CI check, not in comments, which go stale and give a workaround a place to explain itself instead of being fixed | a comment outside a Go doc comment on an exported framework name or package clause, or any comment in an app or a generator's template; put the intent in a name, type or test, and fix a workaround's root cause instead of explaining it. Directives stay: //go:, //nolint:<linter> // <reason>, sqlc's -- name: and goose's -- +goose lines |
| `migration-down` | Violation | Keep rollback and testkit.Migrations checking every migration's undo | migration has no -- +goose Down section, so rolling back stops at it; add one that undoes the up, or leave it empty to mark the migration irreversible |

<!-- /rules -->

Strict mode fails on violations. Advisory findings require context and stay
visible in both the terminal and browser without failing CI. The Go checks use
syntax trees and imported package names, so import aliases work and comments
are not treated as calls. Generated templ Go files are skipped, and test Go
files by every rule but `comments`.
The guard rule recognizes the generated positive `cfg.Development()` shape;
custom equivalent guards require review and an exception. This checks specific
conventions, not every possible path through a program. It cannot prove that a
custom layout is wrong or that a data fetch delays rendering; those remain
review/performance-measurement questions.

The layout rules hold an app to [its layout](plans/gantry.md#an-apps-layout)
and read only `app/`: `import-boundary`, `view-db`, `env-outside-config`,
`background-work`, `sql-outside-models`, `read-then-write`, `get-mutates`,
`inline-download`, `cached-scope` and `cache-set-cookie`. `import-boundary` tells the app's own packages by the app's
import path, from the nearest `go.mod` at or above it (an `--in-module` app's
is under its module's path), and sorts them by folder: `app/models`,
`app/services/*`, `app/shared/*`, `app/mailers` and `app/jobs`, and every other
folder is a resource. Anything but models may import `app/shared`: models
import no other app package, and shared's page shells and helpers import web
and templ. `view-db` and `raw-html` read a `.templ` file's components as text
after its Go imports, so they follow the import's alias but not a value passed
through a helper. `sql-outside-models` allows `database/sql`'s types sqlc's
code uses (`sql.NullTime`, `sql.ErrNoRows`); it flags `*sql.DB`, `sql.Open`,
and SQL run on a DB's `Read` or `Write` pool or a `*db.Tx` outside
`app/models`. `background-work` allows `sync.WaitGroup.Go`, whose goroutines
the request waits for. `http-client`, `raw-html`, `cross-origin-bypass` and
`tx-defer` read every Go file, gantry's own included.

`comments` holds an app to no comments at all: Go, tests included, `.templ`,
SQL, YAML, shell, JavaScript, CSS, HTML, `.gitignore` and `.dockerignore`.
Intent goes in a name, a type or a test, which the compiler and the tests
check; a comment that explains a workaround is a sign to fix its cause. What
stays are directives a tool reads: `//go:` lines,
`//nolint:<linter> // <reason>`, sqlc's `-- name: Name :one` (`:many`, `:exec`
and the rest), goose's `-- +goose Up`, `Down`, `StatementBegin` and
`StatementEnd`, a script's `#!` line and `go.mod`'s `// indirect`. Go that says
it's generated (`// Code generated ... DO NOT EDIT.`, as sqlc's is) and the
packages `importmap pin`
vendors into `assets/js/vendor/`, which keep their license notices, aren't
read. Files without an extension, such as the `Dockerfile` and
`bin/gantry`, aren't either: Strict picks files by extension, and gantry's own
templates for them are checked instead. `.gantry-check.json` can't except a
file from it.

The advice rules read one function at a time: a handler routed inline is a
function of its own, and a `c.DB.Tx` closure is part of the function that
calls `Tx`. `read-then-write` is a `Get…` on
`models.New(c.DB.Read)` followed by an `Update…` or `Delete…` on
`models.New(c.DB.Write)`. `get-mutates` is a write (`models.New(….Write)` or a
`Tx`) in a GET action (the `Index`, `Show`, `New` and `Edit` that `Resources`
routes to GET) or in a function routed inline to a GET pattern; a handler
routed by name from another file isn't followed. Logout is POST-only in `auth`
itself. `inline-download` is `http.ServeContent` or `ServeFile` in a function
that never names `Content-Disposition`.

`cached-scope` and `cache-set-cookie` start at each route the page cache
serves. That's `Cached` on a router or a scope, and `PageCache.Handler` around
a function (given to `http.HandlerFunc` or the router's `Wrap`) or a type's
`ServeHTTP`. It's also the `Index`, `Show`, `New` and `Edit` of `Resources`
wrapped in the cache's `Handler`. They follow the handler, and what it calls,
into the app's other files and packages, as far as the code says what each
name is: a parameter's type, `x := T{...}`, a struct's field (an embedded
one's too), or a function's one result. An alias is the type it stands for,
so `g auth`'s `app/auth.Auth` is gantry's. So a resource's `Index`, through
the `Render` of the `resource.Controller` it embeds, is found taking the
flash.
The finding is at the route, with the way there. A handler they can't read
isn't reported: gantry's own (`a.Live.Serve`), another module's, or one a
call returns (`rt.Cached("GET /x", a.page())`). `cached-scope` looks for
`auth.Current`, `Auth.SignedIn` or `HasSessionCookie`, a request's cookies
(`r.Cookie`, `web.SignedCookie`) and the flash (`Flash.Take`).
`cache-set-cookie` looks for `http.SetCookie`, `web.SetCookie` or
`ClearCookie`, the flash's `Set`, `Redirect` or `Take` (which clears it),
`Auth.StartSession`, and a `Set-Cookie` header. The page cache still guards
both at runtime: it keeps no response that sets a cookie, and serves no
request with a session from the cache. `bin/gantry verify` checks that no
signed-in user's cached page is shown to anyone else.

Upgrading: these rules are new in this release. An app that fails one fixes
the code the message names, or adds an exception with its reason. Gantry's
generators write code that passes them, so a new app, its resources and its
recipes are clean. `comments` is new too, and has no exceptions: an app made
before it fails `check --strict` until its comments are gone, each one's
intent in a name, a type or a test.

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
empty reasons and an exception for `comments`, which takes none, are rejected. Keep exceptions narrow and review this file
alongside code changes. They suppress all findings of that rule in the named
file; line numbers are intentionally not used because edits move lines.

An exception that suppresses nothing, because the file was fixed, moved or
deleted, or an upgrade stopped the rule firing there, is reported as
`stale-exception` advice on `.gantry-check.json`. Remove it: left in place, it
would hide the next violation of that rule in that file. It is advice so that
an upgrade doesn't fail CI.

A reason should point at where the exception was decided: an issue (`#123`) or
a link to an issue or decision record (`https://...`). With
`"requireIssue": true`, as a new app has, the scan fails on a reason without one. Without it, as
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

`bin/gantry routes --diagram` (with `--local` on this machine) prints a
deterministic Mermaid flowchart joining the router's routes with
`test/features.yaml`: features grouped under `public`, `signed-in` and `admin`,
showing each route's filters and `Cached` status, mounted routes, and any
unmapped routes highlighted for review. `--diagram` and `--json` can't be used
together.

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

`bin/gantry verify` runs that test alone, verbosely, as `GANTRY_ENV=test`. It
runs on Houston's test stack, the one `bin/gantry test` uses: the Dockerfile's
`test` stage, in a throwaway copy of the project. To get there, it runs
`houston -f` with a copy of `compose.yml` whose `x-houston.commands.test` is
the `TestFeatures` run, and it removes the copy afterwards. The copy,
`.gantry-verify-*.yml`, sits beside `compose.yml`, and a new app's
`.gitignore` and `.dockerignore` leave it out. With `--local`, it runs on this
machine. Without a `TestFeatures` in `app/`, it fails rather than run no test
and pass. A new app's `.github/workflows/ci.yml` runs it after
`bin/gantry test`. `testkit.Verify` refuses `GANTRY_ENV=development`, where
the reload script takes the page cache's headers off every page. The app runs
in the test's process, on a test
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

`bin/gantry verify --json` (in Houston's test stack or with `--local`) runs the
same `TestFeatures` check and prints a single JSON object (`testkit.VerifyReport`)
with stable field names, exiting nonzero on a failure after printing:

```json
{
  "passed": true,
  "failures": 0,
  "routes": 3,
  "unmapped_routes": [],
  "features": [
    {
      "name": "home",
      "role": "public",
      "cacheable": false,
      "passed": true,
      "routes": ["GET /{$}"],
      "visits": [{"path": "/", "role": "public", "status": 200, "allowed": true, "passed": true}],
      "cache": [],
      "leaks": [],
      "errors": []
    }
  ]
}
```

`g resource` and `g auth` print their feature among their next steps, to paste
under `features` once its routes are routed. A namespaced resource's role is
whoever its scope's filter lets in, which the generator can't know.

#### Adding the routes in no feature

`bin/gantry g features` adds a feature to `test/features.yaml` for each route
that is in none. It gets the routes as `bin/gantry routes --json` lists them,
from the app's own router, in its container, or on this machine with
`--local`. Mounted routes are left out, as `testkit.Verify` leaves them out.

- Each feature it adds has one route and is named after it. It is public,
  cacheable only when its route is `Cached`, and has a visit at the route's
  path when the route is a `GET` with no wildcard in its path.
- An unmapped home page, `/up` or `/live` gets the feature a new app's map has
  for it instead.
- It writes after the features already in the file and changes none of them.
  With no `test/features.yaml`, it writes one.
- It prints each route it adds, with the route's filters, so you can see which
  ones aren't public.

Then set each feature's role, give its visits a selector, and merge features
as you like. `bin/gantry test` fails until each visit answers as its role says.
To run it with `--local`, the app needs a database it can open on this
machine: a SQLite app's is under `DATA_DIR`, which is `/data` unless you set
it.

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

An app without `test/features.yaml` works as it did. The map isn't read by
`bin/gantry check --strict`, by `bin/gantry test` or by the app's CI. Only
`bin/gantry verify` asks for it. To opt in:

1. In `app/routes.go`, split `App.Handler` into `Router`, which returns the
   `*web.Router`, and `Handler`, which returns `a.Router().Handler()`.
2. Add the `routes` case and function from a new app's `cmd/<app>/main.go`.
   `bin/gantry routes` then lists the app's routes.
3. Run `bin/gantry g features`. It writes `test/features.yaml`, with a feature
   for each route that isn't Mounted.
4. Copy a new app's `app/features_test.go`. If the app has signed-in or admin
   pages, give its `testkit.Roles` a `SignIn` for each role (above).
5. Set each feature's role, cacheable and selectors, and merge features until
   `bin/gantry test` passes. `bin/gantry verify` runs the same check verbosely.
6. Add `.gantry-verify-*.yml` to `.gitignore` and `.dockerignore`, as a new
   app has it. `bin/gantry verify` writes that copy of `compose.yml` while it
   runs.
7. To have CI check it as a new app's does, add `- run: bin/gantry verify`
   after `- run: bin/gantry test` in `.github/workflows/ci.yml`.

Run `bin/gantry g features` again after adding routes, or paste the feature a
generator prints.

#### After a deploy

`bin/gantry verify` checks a commit, not a server. It runs the commit's
`TestFeatures` on Houston's test stack, or with `--local` in the test's own
process, on a test database with test users. It sends no request to a deployed
app, and it takes no URL.

So in a pipeline it's the check on the commit the deploy ships. A new app's CI
runs it on each pull request and each push to `main`. Houston's push to deploy
runs `commands.test`, which includes `TestFeatures`, before it deploys. Houston
moves traffic to the new version only once `/up` (the `health` path in
`compose.yml`) answers 200.

A pipeline that deploys to a staging server first, a non-production stack with
its own data, can run `bin/gantry verify` between the staging deploy and the
production one. Run it on the commit staging runs, and promote that commit
only when it passes. Verify still runs on the test stack there, not against
staging's URL.

Don't point a crawl of your own at a production app. Verify signs in as two
test users of each role and requests every feature's pages. Against
production, those users and requests would be in real data.

### Stateful property walking (`testkit.Walk`)

For fuzz-style stateful testing of an app's own routes and forms, `testkit.Walk`
drives an `http.Handler` through a seeded random sequence of link visits, role
switches (`testkit.Roles`), valid and invalid form submissions, and stale
`lock_version` replays. At every step it asserts no panics or 5xx responses,
optional `testkit.Links` resolution (`CheckLinks: true`), no public leakage of
signed-in role markers, and 409 conflicts on stale `lock_version` edits. Any
failure prints the seed and step trace so the exact sequence can be replayed.

## Edit forms and lock_version

A table that `g resource` or `g migration create_<table>` makes has a
`lock_version` column, `integer NOT NULL DEFAULT 0` on both engines. A
resource's edit form sends the row's `lock_version` in a hidden field, and
`Update` compares it with the row's in the transaction that reads and writes
the row. When they match, it saves the form and adds one to `lock_version`.
When they don't, someone saved the record after the form was drawn: `Update`
writes nothing, and answers 409 with the form again. The fields are what was
sent, the hidden `lock_version` is the row's, and the form's errors say:

> Someone else changed this record while you were editing. Your values are
> still in the form; submitting again will save them.

The form doesn't show the other person's values. Submitting it again saves the
user's over them. A form sent without `lock_version` gets the same answer.

Upgrading: tables made before this release have no `lock_version`, and their
resources' code doesn't use one. They're unchanged, and an edit form still
saves over whatever was saved while it was open. A resource that `g resource`
makes in an older app has the column and the check. To opt an existing
resource in:

1. `g migration add_lock_version_to_<table>` writes an empty migration. Write
   in it, for either engine:

   ```sql
   -- +goose Up
   ALTER TABLE "posts" ADD COLUMN "lock_version" integer NOT NULL DEFAULT 0;

   -- +goose Down
   ALTER TABLE "posts" DROP COLUMN "lock_version";
   ```

2. In `app/models/<table>.sql`, the `Update…` query sets
   `lock_version = lock_version + 1` beside `updated_at`.
3. In the resource's `controller.go`, as a resource generated now has them:
   `editFields`, which is `fields` and
   `resource.F("lock_version", resource.Type("hidden"))`, as the form's
   fields when it edits a row; `"lock_version"` in `formValues`; and in
   `Update`, the comparison with the row's `lock_version` and the 409 before
   it saves.
4. `bin/gantry db migrate`, then `bin/gantry exec sqlc generate` and
   `bin/gantry exec templ generate`.

Generating the resource again instead still means moving its files aside
first, and `g resource` then writes a `create_<table>` migration too: delete
it, since the table is there.

An app's `app/shared/resource/pages.templ` is its own, and `g resource` keeps
it. One from before this release draws the hidden field with its label, "Lock
version", and nothing beside it. The form still works. For no label, make its
`input` draw a hidden field alone:

```templ
templ input(f Form, field Field) {
	if field.Type == "hidden" {
		<input type="hidden" name={ f.Param + "[" + field.Name + "]" } value={ f.Values[field.Name] }/>
	} else {
		<p>
			…the label and the field, as before…
		</p>
	}
}
```

Or move `pages.templ` aside: the next `g resource` writes the new one.

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
