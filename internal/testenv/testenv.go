// Package testenv is what gantry's own tests take from their environment.
package testenv

import (
	"os"
	"strconv"
	"testing"
)

// Postgres is the Postgres server gantry's tests make their databases on,
// GANTRY_TEST_POSTGRES_URL (bin/go passes it). Without it the test is
// skipped, or fails when CI is set: there a skip would pass unseen.
// testkit.Postgres takes the server from TEST_DATABASE_URL, an app's
// variable: t.Setenv("TEST_DATABASE_URL", testenv.Postgres(t)) bridges them.
func Postgres(t testing.TB) string {
	t.Helper()
	url := os.Getenv("GANTRY_TEST_POSTGRES_URL")
	if url == "" {
		if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci {
			t.Fatal("GANTRY_TEST_POSTGRES_URL isn't set, and CI is: a Postgres server for the tests (postgres://postgres:...@host:5432/postgres)")
		}
		t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
	}
	return url
}
