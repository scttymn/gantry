package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

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

func TestTxPanicRollsBack(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	d.Write.Exec(`CREATE TABLE notes (id integer PRIMARY KEY, body text)`)
	ran := false
	func() {
		defer func() {
			if p := recover(); p != "boom" {
				t.Errorf("recovered %v, want the panic fn raised", p)
			}
		}()
		d.Tx(ctx, func(tx *Tx) error {
			tx.AfterCommit(func() { ran = true })
			tx.ExecContext(ctx, `INSERT INTO notes (body) VALUES ('lost')`)
			panic("boom")
		})
	}()
	if ran {
		t.Error("an after-commit hook ran on a panic")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := d.Tx(ctx, func(tx *Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO notes (body) VALUES ('kept')`)
		return err
	})
	if err != nil {
		t.Fatalf("the next write: %v", err)
	}
	var bodies []string
	rows, err := d.Read.QueryContext(ctx, `SELECT body FROM notes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		rows.Scan(&b)
		bodies = append(bodies, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0] != "kept" {
		t.Errorf("notes are %q, want only the second write's", bodies)
	}
}
