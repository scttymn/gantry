package db

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Console reads SQL statements from in, each ending with a semicolon, runs
// them on the write pool, and prints what they return: the rows under their
// columns' names, else the rows changed. A failing statement prints its
// error, and the console goes on. It's the database console for where
// sqlite3 and psql aren't installed (a production image has only the app),
// as Rails' dbconsole, but in the app's own binary.
func (d *DB) Console(ctx context.Context, in io.Reader, out io.Writer) error {
	fmt.Fprintf(out, "%s console: SQL ending with ;, and Ctrl-D to leave.\n", d.Engine)
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 64*1024), 1<<20)
	var stmt strings.Builder
	prompt := func() {
		if stmt.Len() == 0 {
			fmt.Fprint(out, "sql> ")
		} else {
			fmt.Fprint(out, "...> ")
		}
	}
	prompt()
	for lines.Scan() {
		stmt.WriteString(lines.Text() + "\n")
		if !strings.HasSuffix(strings.TrimSpace(stmt.String()), ";") {
			prompt()
			continue
		}
		if err := d.run(ctx, stmt.String(), out); err != nil {
			fmt.Fprintln(out, "error:", err)
		}
		stmt.Reset()
		prompt()
	}
	fmt.Fprintln(out)
	return lines.Err()
}

// run runs one statement and prints its result.
func (d *DB) run(ctx context.Context, sql string, out io.Writer) error {
	rows, err := d.Write.QueryContext(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		// A statement that returns no columns (INSERT, CREATE, ...).
		fmt.Fprintln(out, "ok")
		return rows.Err()
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		cells := make([]string, len(values))
		for i, v := range values {
			switch v := v.(type) {
			case nil:
				cells[i] = "NULL"
			case []byte:
				cells[i] = string(v)
			default:
				cells[i] = fmt.Sprint(v)
			}
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
		n++
	}
	w.Flush()
	fmt.Fprintf(out, "(%d rows)\n", n)
	return rows.Err()
}
