# gantry

An opinionated Go web framework: Rails' conventions, as idiomatic Go, built from maintained libraries.

- **A place for everything:** `app/models`, a folder per resource (its controller and views), `app/services`, `app/shared`, `db/migrations`, `config/`.
- **Postgres or SQLite:** each app picks one; everything gantry ships works on both.
- **Fast by default:** pages the same for every visitor are cached in memory until the data changes; photos come in a few fixed widths (never enlarged), in WebP by default; one static binary on a `scratch` image.
- **Sign-in solved once** (coming): email and password, emailed codes, TOTP, passkeys, and OAuth.

[Valley Built CrossFit](https://github.com/scttymn/valleybuiltcrossfit) runs on it, deployed with [Houston](https://github.com/scttymn/houston).

## Packages
| Package | What it does |
|---|---|
| `db` | Opens SQLite or Postgres from a URL; SQLite gets one writer and a read pool. goose migrations, and a data version that changes on every write. |
| `web` | Handlers that return errors, one place that turns them into pages, the middleware every app wants, and the page cache. |
| `assets` | Fingerprinted, minified, gzipped-once assets, and stylesheet bundles drawn into the page or linked by size. |
| `images` | Resized copies at fixed widths, 320 to 2400 about 1.5× apart, never enlarged: WebP by default, formats as adapters (`images/heic` reads iPhones' photos), made once in a child process. |
| `sign`, `mail`, `compress`, `testkit` | Signed tokens, email, gzip, and a database with fixtures for each test. |

## Working on gantry
Everything runs in Docker: `bin/go go test ./...`. The plan, with what was measured and found: [`docs/plans/gantry.md`](docs/plans/gantry.md).
