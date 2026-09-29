package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
)

// constraintSchema makes a parent and a child table on d's engine.
func constraintSchema(t *testing.T, d *DB) {
	t.Helper()
	id := "integer PRIMARY KEY"
	if d.Engine == Postgres {
		id = "bigint PRIMARY KEY"
		d.Write.Exec(`DROP TABLE IF EXISTS c_children, c_parents`)
	}
	for _, stmt := range []string{
		`CREATE TABLE c_parents (id ` + id + `, name text NOT NULL UNIQUE)`,
		`CREATE TABLE c_children (id ` + id + `, parent_id bigint NOT NULL REFERENCES c_parents (id))`,
		`INSERT INTO c_parents (id, name) VALUES (1, 'a')`,
	} {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func checkConstraintErrors(t *testing.T, d *DB) {
	constraintSchema(t, d)
	_, dup := d.Write.Exec(`INSERT INTO c_parents (id, name) VALUES (2, 'a')`)
	_, dupKey := d.Write.Exec(`INSERT INTO c_parents (id, name) VALUES (1, 'b')`)
	_, orphan := d.Write.Exec(`INSERT INTO c_children (id, parent_id) VALUES (1, 99)`)
	wrapped := fmt.Errorf("creating the parent: %w", dup)
	for _, tc := range []struct {
		name         string
		err          error
		unique, fkey bool
	}{
		{"a duplicate name", dup, true, false},
		{"a duplicate key", dupKey, true, false},
		{"wrapped", wrapped, true, false},
		{"no such parent", orphan, false, true},
		{"another error", errors.New("disk full"), false, false},
		{"none", nil, false, false},
	} {
		if IsUnique(tc.err) != tc.unique || IsForeignKey(tc.err) != tc.fkey {
			t.Errorf("%s (%v): IsUnique %v, IsForeignKey %v", tc.name, tc.err, IsUnique(tc.err), IsForeignKey(tc.err))
		}
	}
}

func TestConstraintErrors(t *testing.T) { checkConstraintErrors(t, openTemp(t)) }

func TestConstraintErrorsPostgres(t *testing.T) {
	url := os.Getenv("GANTRY_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
	}
	d, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	checkConstraintErrors(t, d)
}

// ErrStale is answered 409 Conflict (Rails' StaleObjectError).
func TestErrStale(t *testing.T) {
	var s interface{ HTTPStatus() int }
	if !errors.As(fmt.Errorf("saving: %w", ErrStale), &s) || s.HTTPStatus() != 409 {
		t.Error("ErrStale doesn't say 409")
	}
}
