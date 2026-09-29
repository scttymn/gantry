package db

import (
	"context"
	"errors"
	"testing"
)

// Code that must only run once the change is saved (sending the email,
// enqueuing the job) runs after commit, in order, and never on rollback
// (Rails' after_commit).
func TestAfterCommit(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	d.Write.Exec(`CREATE TABLE notes (id integer PRIMARY KEY, body text)`)
	var ran []string
	err := d.Tx(ctx, func(tx *Tx) error {
		tx.AfterCommit(func() { ran = append(ran, "first") })
		if _, err := tx.ExecContext(ctx, `INSERT INTO notes (body) VALUES ('x')`); err != nil {
			return err
		}
		tx.AfterCommit(func() {
			// The change is there to see: it's committed.
			var n int
			d.Read.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n)
			ran = append(ran, "second", string(rune('0'+n)))
		})
		if len(ran) != 0 {
			t.Error("a hook ran before the commit")
		}
		return nil
	})
	if err != nil || len(ran) != 3 || ran[0] != "first" || ran[1] != "second" || ran[2] != "1" {
		t.Fatalf("ran %q, %v", ran, err)
	}

	ran = nil
	err = d.Tx(ctx, func(tx *Tx) error {
		tx.AfterCommit(func() { ran = append(ran, "rolled back") })
		return errors.New("no")
	})
	if err == nil || len(ran) != 0 {
		t.Errorf("on rollback: ran %q, %v", ran, err)
	}
}
