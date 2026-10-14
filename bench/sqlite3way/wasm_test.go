//go:build viawasm

package sqlite3way

import (
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

var other = engine{"WebAssembly", "sqlite3", pragmas}
