# Security

What a Gantry app believes about a request, how long a client may take, and
the headers that keep a browser to https and to the app's own scripts. New
apps set all of it up in `app/routes.go`, `cmd/<app>/main.go` and
`config/config.go`; an existing app adopts it by hand (see
[Upgrading](#upgrading)).

## Trusted proxies

A proxy in front of the app (Houston's, a load balancer, Cloudflare's tunnel)
knows what the app can't see: the visitor's address, whether they used https,
and the host they asked for. It says so in headers anyone can also send, so
the router believes them only from a peer in `rt.Proxies`, and removes them
from any other before the app sees them.

- `X-Forwarded-For` is read from the right: the nearest address that isn't a
  proxy's.
- `X-Forwarded-Proto` and `X-Forwarded-Host` take their last value, across
  all their lines: a proxy that appends puts its own after whatever the
  visitor sent. `X-Forwarded-Host` is believed only with `ForwardedHost`.
- `ClientIP` names a provider's one-address header instead
  (`"CF-Connecting-IP"`), and `Names` trusts a container by its DNS name.

`web.ClientIP(r)`, `web.Scheme(r)` and `web.RequestHost(r)` are the results.
Secure cookies, generated URLs, HSTS and per-address rate limits (sign-in,
emailed codes) all rest on them.

With nothing set, the trusted proxies are `web.PrivateNetworks`: anything
that reaches the app from a loopback, private or link-local address, as Rails
does. Houston puts an app behind its proxy on a private network, so that's
the proxy the default believes, and it's right while the only way in is
through it. Where anything else can reach the app from a private address (another
container on its network, a Docker gateway, a machine on the same LAN), it
can name any visitor's address, and get past per-address rate limits. List
the proxies then:

| `TRUSTED_PROXIES` | Trusted |
| --- | --- |
| blank | `web.PrivateNetworks` |
| `10.0.1.5, 172.18.0.0/16` | those addresses and ranges, separated by commas or spaces |
| `none` | nothing: no proxy is in front of the app |

New apps read it in `config.Config.TrustedProxies`, parse it with
`web.ParseProxies` as they start (a value that isn't an address or a range
stops the app), and set `rt.Proxies` from it.

## Server timeouts

New apps' `http.Server` gives a request 10 s to send its headers and 30 s to
arrive whole (`ReadTimeout`), 60 s to be answered (`WriteTimeout`), and
closes a connection idle for 2 minutes (`IdleTimeout`), so a client that's
slow to send or to read can't hold one open.

What legitimately takes longer goes on past them:

- Live streams (`live.Hub.Serve`) clear the write timeout and give each write
  its own instead, `live.Options.WriteTimeout` (10 s): a client that stops
  reading ends its stream rather than holding it.
- A request body read through the router, and an answer sent through
  `web.Moving` (`web.Download` and the image server's files: storage's
  originals and the copies it makes on request), keep pace with the server's
  timeouts rather than ending at them. Each has the timeout from its start
  and a second more for every 16 KiB it has moved, but never more than the
  timeout from now. A large upload or download at a phone's pace goes on.
  One that stops, or trickles in a byte at a time to hold the connection
  open, is cut off as the timeout alone would cut it off. A form that
  doesn't arrive in time is answered 408, with the `WriteTimeout` from then.
- `assets.Images`' precompiled copies are sent from memory in one write,
  under the `WriteTimeout` as it is.

A handler of the app's own that sends a large answer writes it through
`web.Moving(w, r)`. One that is slow to start answering by design (a long
report) extends its deadline with
`http.NewResponseController(w).SetWriteDeadline`.

## HSTS

`rt.Security.HSTS` sends `Strict-Transport-Security: max-age=…` on every
request made over https (by `web.Scheme`: TLS, or a trusted proxy's
`X-Forwarded-Proto`), never over http, which browsers ignore. A browser that
has seen it goes to the site over https alone until it lapses, so a network
along the way can't serve it a plain http page first.

New apps send a year's in production (`App.Production`), and nothing in
development or tests. It's a promise to serve https for that long: set
`HSTS` to 0 for a site that may need plain http, and lower it before
retiring https. `includeSubDomains` and `preload` aren't sent; add them only
when every subdomain is on https.

## Content-Security-Policy

`rt.Security.CSP` is sent as `Content-Security-Policy` on every response,
and as `Content-Security-Policy-Report-Only` with `CSPReportOnly`: the
browser then reports in its console what the policy would block, and blocks
nothing.

`web.ContentSecurityPolicy(scripts, styles)` is a policy for an app's pages:

```
default-src 'self'; script-src 'self' <scripts>; style-src 'self' <styles> <Turbo's>;
img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'self'
```

Scripts and styles come from the app itself, and inline only the ones named by
hash. New apps pass the inline ones Gantry draws:

```go
rt.Security = web.Security{
	CSP:           web.ContentSecurityPolicy(assets.ImportMap.Hashes(), assets.Styles.Hash()),
	CSPReportOnly: true,
}
```

- `assets.ImportMap.Hashes()`: the import map and the `<script type="module">`
  that imports the entry. Pass the same entry `ImportMap.Tag` draws.
- `assets.Styles.Hash()`: the stylesheets when `Styles.Tag` draws them inline.
- Turbo's progress bar: a `<style>` Turbo adds to every page. Its hash is in
  the policy already, for the Turbo new apps come with. An app that pins
  another Turbo sees a report naming the hash to add to `styles`.
- Images may be `data:` URIs, as pictures' placeholders are.

The rest of Gantry's own needs nothing more. The development panel and
reload script are files from the app itself, the development inbox sets a
policy of its own, and the app's static error pages (`assets/public/404.html`
…), plain files with their style inline, are sent with a policy of their own
that lets that style in and no inline script, in place of the app's.

New apps ship the policy report-only. A page's own `style="…"` attribute,
`<script>` without a `src`, `onclick=` handler or picture from another site
shows up as a report in the browser's console: move styles to the app's
stylesheet and scripts to a Stimulus controller. Add a directive for what
the app loads from elsewhere (`+ "; connect-src 'self' https://api.example.com"`).
A browser ignores a directive given twice, so to change one the policy
already has, like `img-src`, write the policy out in full. Once pages report
nothing, drop `CSPReportOnly` to enforce it.

## Upgrading

For existing apps, gantry's own changes:

- **`X-Forwarded-Proto` and `X-Forwarded-Host` take the last value.** Before,
  `X-Forwarded-Proto` took the first, which behind a proxy that appends is
  the visitor's own. Nothing changes behind a proxy that replaces the header
  rather than appending to it, as nginx's
  `proxy_set_header X-Forwarded-Proto $scheme` and Go's
  `httputil.ProxyRequest.SetXForwarded` do.
- **`rt.Security` is new**, zero by default: no HSTS and no policy until the
  app sets them. `web.Headers(h)` is unchanged; an app with its own
  middleware stack uses `web.Security{…}.Headers(h)` for them.
- **Uploads and downloads keep pace with the server's timeouts**, so adding
  `ReadTimeout` and `WriteTimeout` doesn't cut off one still moving at a
  phone's pace. With no timeouts on the server (or negative ones), nothing
  changes.
- **A form that fails to parse is 400**, or 408 when it didn't arrive in
  time; 413 is only for one over its size limit. Before, all three were 413.
- **Live streams end when a write waits 10 s** on a client that has stopped
  reading (`live.Options.WriteTimeout`). The browser reconnects, as it does
  after any dropped stream.
- **Static error pages are sent with a policy of their own** when the app
  has one, and the development panel's style is a stylesheet at
  `/_gantry/development.css`.

To adopt what new apps have, copy from a new app (`gantry new`):

- **Timeouts:** `ReadTimeout`, `WriteTimeout` and `IdleTimeout` in
  `cmd/<app>/main.go`'s `http.Server`.
- **Trusted proxies:** `TrustedProxies` in `config/config.go`,
  `web.ParseProxies` in `build`, `App.Proxies` and `rt.Proxies` in
  `app/routes.go`, and `TRUSTED_PROXIES` in `compose.yml`.
- **HSTS and the policy:** `App.Production` (set in `build`) and the
  `rt.Security` block in `app/routes.go`. Deploy the policy report-only,
  check the console on the app's pages, then enforce it.
