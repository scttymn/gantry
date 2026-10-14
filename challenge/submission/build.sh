#!/usr/bin/env bash
# Builds out/server: one static binary, with SQLite compiled from C.
set -euo pipefail
cd "$(dirname "$0")"
CGO_ENABLED=1 CGO_CFLAGS="-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY" /usr/local/go/bin/go build -trimpath -ldflags "-linkmode external -extldflags -static" \
	-tags nodynamic,timetzdata,netgo,osusergo,sqlite_omit_load_extension,sqlite_fts5,sqlite_math_functions,sqlite_dbstat,sqlite_stat4 -o out/server ./cmd/twelve-dollar
