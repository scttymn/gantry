# Plan: storage, Active Storage in gantry

## Direction
From converting Esther Pictures (Phoenix) to gantry (2026-09-30). Its admin uploads clip thumbnails and cast headshots. The choice was app code over gantry's images, or "Active Storage in gantry": your call was Active Storage, with Esther as its first user and the gym site able to move onto it later.

## Evidence
- The gym site's `app/services/photos` (478 lines) is Active Storage by hand: Rails' `active_storage_blobs` and `active_storage_attachments`, the disk service's `<key[0:2]>/<key[2:4]>/<key>` files, `Attach`/`Replace`/`Purge`, analysis into the blob's metadata, and `Warm`, which makes every copy through the polite child. Only its slots (`Site.hero_photo`, …) and its admin-set quality are its own.
- Esther keeps `thumbnail_path` and `headshot_path` columns ("/uploads/clips/<random>.webp") and files under `DATA_DIR/uploads`, and serves the upload itself. Its version history snapshots those paths, and a sweep deletes files that neither a record nor a snapshot names.
- `gantry/auth` sets the pattern: `gantry g auth` writes the app's migration for tables in Rails' shape, and the package works on them with plain SQL, so a Rails app's rows carry over.

## Design
**`gantry g storage`** writes the migration for Rails' two tables, as `rails active_storage:install` does (not `active_storage_variant_records`: copies are files named by width, not rows). SQLite and Postgres.

**`storage.Storage`**, an app's attached files:
- `Root`: the disk service's folder (a volume); a blob's file is `Root/<key[0:2]>/<key[2:4]>/<key>`, and its copies are under `Root/variants`.
- `Attach(ctx, ref, file)`: `has_one_attached`. The file is written first (whole, then renamed in), analysed (its type from its bytes, and its size as shown), and the blob and attachment rows go in together, replacing the ref's attachment, whose blob is purged. `AttachBlob` attaches a blob that's already stored (a restore). `Detach` removes the attachment and keeps the blob; `Purge` removes both and the files. `Unattached` lists blobs attached to nothing, for an app's sweep.
- `Find(ctx, ref)`, and `All(ctx, recordType, name)` for a page's list, keyed by record id.
- Pictures: the images package's standard way. `Img(blob, images.Img)` draws a `<picture>` of AVIF and WebP copies at every width up to the picture's own; `Warm` makes the missing copies through the polite child (stopping at `images.ErrNoRoom`, carried on at the next start or upload), and `Attach` asks for a warming. `Placeholders`, when set, make the 32 px blur too.
- Serving: `Storage` is an `http.Handler` at `Prefix` ("/storage/"): a picture's copies, and any blob's original at `/storage/<key>/original`, only for blobs attached to something. Keys are Rails': 28 random lowercase letters and digits.
- `FileFrom(r, field)`: the uploaded file of a multipart form. What an app accepts (types, size) is its own validation.

Not now: `has_many_attached`, other services (S3), direct uploads. Moving the gym site onto `storage` comes after Esther; then `gantry g resource`'s `photo` type, which writes code against the gym's own photos service today, writes it against `storage`.

## Acceptance criteria → tests
| Criterion | Test |
| --- | --- |
| `gantry g storage` writes Rails' two tables, SQLite and Postgres, and they migrate | `TestGenerateStorage` |
| Attach stores the file at Rails' path with Rails' key, checksum and metadata, and attaches it | `storage.TestAttach` |
| Attaching again replaces the attachment and purges the old blob and its files | `storage.TestAttachReplaces` |
| Detach keeps the blob; AttachBlob attaches it again; Unattached lists it | `storage.TestDetachAndAttachBlob` |
| Purge removes rows, file and copies | `storage.TestPurge` |
| Find and All return attached blobs with their sizes | `storage.TestFind` |
| Only attached blobs are served: copies and originals; others 404 | `storage.TestServe` |
| Img draws AVIF and WebP sources sized to the picture | `storage.TestImg` |
| Warm makes the missing copies, stops at ErrNoRoom, analyses blobs without a size | `storage.TestWarm` |
| A Rails-made database's rows are read as they are | `storage.TestRailsRows` |
| Postgres as SQLite | the tests above on both |

## Evidence (2026-09-30)
- `storage` tests: all pass (`bin/go go test ./storage/`, three runs); `TestStorageMigrates` runs every query `storage` makes on SQLite and on Postgres 17 (a throwaway container), including a purge without Rails' variant table, where the savepoint keeps Postgres' transaction alive.
- Mutation check: all 17 caught (a replaced blob not purged, an unattached blob served, an attached one purged by `PurgeBlob`, no AVIF copies, no analysis saved, Rails' variant records left, no warming after an upload, every copy remade, a wrong checksum, `Detach` keeping the row, a path not Rails', a copy that found no room carrying on, copies not forgotten, `All` ignoring the name, no placeholder, `AttachBlob` keeping what it replaced). Found by it: the no-room test stopped at the placeholder and never reached a copy; it now checks both.
- Found along the way: `v0.8.12` went out with `TestNewGolden` failing (its go.mod still said `v0.8.11`: `latestRelease` was bumped after the suite ran). Fixed here; the suite now runs after the bump.
