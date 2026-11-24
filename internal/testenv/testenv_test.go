package testenv

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

type recorder struct {
	testing.TB
	skipped, failed string
}

func (r *recorder) Helper() {}

func (r *recorder) Skip(args ...any) { r.skipped = fmt.Sprint(args...); runtime.Goexit() }

func (r *recorder) Fatal(args ...any) { r.failed = fmt.Sprint(args...); runtime.Goexit() }

func postgres(t *testing.T, ci, url string) (string, *recorder) {
	t.Setenv("CI", ci)
	t.Setenv("GANTRY_TEST_POSTGRES_URL", url)
	r := &recorder{}
	var got string
	done := make(chan struct{})
	go func() {
		defer close(done)
		got = Postgres(r)
	}()
	<-done
	return got, r
}

func TestPostgres(t *testing.T) {
	const server = "postgres://postgres@localhost:5432/postgres"
	for _, ci := range []string{"", "true"} {
		if got, r := postgres(t, ci, server); got != server || r.skipped != "" || r.failed != "" {
			t.Errorf("CI=%q, set: %q, skipped %q, failed %q", ci, got, r.skipped, r.failed)
		}
	}
	for _, ci := range []string{"", "false"} {
		if _, r := postgres(t, ci, ""); r.skipped == "" || r.failed != "" {
			t.Errorf("CI=%q, unset: skipped %q, failed %q; want skipped", ci, r.skipped, r.failed)
		}
	}
	for _, ci := range []string{"true", "1"} {
		if _, r := postgres(t, ci, ""); r.failed == "" || !strings.Contains(r.failed, "GANTRY_TEST_POSTGRES_URL") || r.skipped != "" {
			t.Errorf("CI=%q, unset: skipped %q, failed %q; want failed, naming the variable", ci, r.skipped, r.failed)
		}
	}
}
