#!/usr/bin/env bash
# Serves in the foreground, on HOST:PORT, the database at SQLITE_PATH (config/config.go reads them).
exec "$(dirname "$0")/out/server"
