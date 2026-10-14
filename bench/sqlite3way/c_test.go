//go:build !viawasm

package sqlite3way

import _ "github.com/mattn/go-sqlite3"

var other = engine{"compiled-C", "sqlite3", "file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=1&_synchronous=NORMAL"}
