// Package pins is the versions gantry and its apps build with. gantry new
// writes them into an app; the copies kept by hand elsewhere (the
// Dockerfile, the workflows, bin/bench, bin/ci, CONTRIBUTING.md) are held to these
// by its test.
package pins

const (
	// Go is go.mod's go line, which a new app's go.mod and go.work have too.
	Go = "1.27.4"
	// Templ is go.mod's templ, so an app generates with the one gantry builds with.
	Templ = "v0.3.1118"
	// GolangciLint is the golangci-lint release CI and generated apps' CI run.
	GolangciLint = "v2.15.0"
	// GolangImage is the toolchain stage's base: gantry's (bin/go) and an app's.
	// The images are tag@digest, the multi-arch index's: the digest is what's
	// pulled. They're bumped by hand, with their copies (CONTRIBUTING.md).
	GolangImage = "golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea"
	// SqlcImage is where the toolchain stage copies sqlc from.
	SqlcImage = "sqlc/sqlc:1.31.1@sha256:70f53171d27b2424e9358869975455a6e955a5aa8e58a998a270a6e34e525537"
	// PostgresImage is a Postgres app's database, and the workflows' for
	// gantry's tests; psql and pg_dump are its major's.
	PostgresImage = "postgres:17@sha256:223efeb196329b55b3b9a13db144956411dbc18e727e59f681c3e0aede6475cf"
	// SQLiteCFlags is CGO_CFLAGS for SQLite compiled from C: Go's own, and the options SQLiteTags don't give.
	SQLiteCFlags = "-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY"
	// SQLiteTags is an app's build tags: the sqlite_ ones select db's C build (db/sqlite_c.go), the rest a static binary's.
	SQLiteTags = "nodynamic,timetzdata,netgo,osusergo,sqlite_omit_load_extension,sqlite_fts5,sqlite_math_functions,sqlite_dbstat,sqlite_stat4"
)
