# Go + gantry

| | |
|---|---|
| Language | Go 1.27.1 |
| Framework | [gantry](https://github.com/scttymn/gantry), at the commit `go.mod` pins: its `web` (over `net/http`) and `db` (over `database/sql`) |
| SQLite driver | `modernc.org/sqlite` 1.59.0 (SQLite 3.53.4, pure Go) |
| **Nginx or direct** | **Direct**: serves `HOST:PORT` itself |

This is an app as `gantry new` makes it, with the five endpoints added: routes in `app/routes.go`,
the controller in `app/posts`, the bearer-token filter in `app/shared/bearer`, and the queries in
`app/models/posts.sql` (sqlc). Nothing of gantry's request path is turned off: its gzip, security
headers, cross-origin check and panic recovery all run.

## What it does for speed, and what it doesn't

- **gantry's own, with nothing set:** one writer connection and a pool of readers in WAL mode with
  `synchronous=NORMAL`, and each connection keeping the statements it compiles (prepared statements,
  rule 5). Writes are transactions on the one writer, committed before the answer.
- **Nothing is cached across requests.** Every request reads SQLite, and every token is verified.
- **The schema is the challenge's.** With `SQLITE_PATH` set the app runs no migration and adds no
  table. The jobs, mail and live a new gantry app starts with keep tables of their own, so they're
  taken out.
- A like is one `INSERT ... ON CONFLICT DO NOTHING`: gantry turns foreign keys on, so a post that
  isn't there is the foreign key's error, and no second query asks.

## Running it

```bash
sudo bash install.sh   # Go, from go.dev
bash build.sh          # out/server
SQLITE_PATH=... JWT_SECRET=... HOST=0.0.0.0 PORT=80 bash start.sh
```
