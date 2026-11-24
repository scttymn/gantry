package test

import (
	"testing"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/testkit"

	"feed/db/migrations"
)

func DB(t testing.TB) *db.DB { return testkit.DB(t, migrations.Up) }
