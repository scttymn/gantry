// Package pins is the versions gantry and its apps build with. gantry new
// writes them into an app; the copies kept by hand elsewhere (the
// Dockerfile, the workflows, bin/bench, CONTRIBUTING.md) are held to these
// by its test.
package pins

const (
	// Go is go.mod's go line, which a new app's go.mod and go.work have too.
	Go = "1.27.4"
	// Templ is go.mod's templ, so an app generates with the one gantry builds with.
	Templ = "v0.3.1020"
	// GolangImage is the toolchain stage's base: gantry's (bin/go) and an app's.
	GolangImage = "golang:1.27-bookworm"
	// SqlcImage is where the toolchain stage copies sqlc from.
	SqlcImage = "sqlc/sqlc:1.31.1"
	// PostgresImage is a Postgres app's database; psql and pg_dump are its major's.
	PostgresImage = "postgres:17"
	// SQLiteCFlags is CGO_CFLAGS for SQLite compiled from C: Go's own, and the options SQLiteTags don't give.
	SQLiteCFlags = "-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY"
	// SQLiteTags is an app's build tags: the sqlite_ ones select db's C build (db/sqlite_c.go), the rest a static binary's.
	SQLiteTags = "nodynamic,timetzdata,netgo,osusergo,sqlite_omit_load_extension,sqlite_fts5,sqlite_math_functions,sqlite_dbstat,sqlite_stat4"
)
