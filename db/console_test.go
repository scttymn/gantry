package db

import (
	"context"
	"strings"
	"testing"
)

func TestConsole(t *testing.T) {
	d := openTemp(t)
	in := strings.NewReader(`CREATE TABLE posts (id integer PRIMARY KEY, title text);
INSERT INTO posts (title)
  VALUES ('first'), (NULL);
SELECT id, title FROM posts ORDER BY id;
SELECT nope FROM posts;
SELECT count(*) AS n FROM posts;
`)
	var out strings.Builder
	if err := d.Console(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"sqlite console",
		"sql> ok\n", // CREATE
		"...> ok\n", // the INSERT, over two lines
		"id  title\n1   first\n2   NULL\n(2 rows)\n", // rows under their columns
		"error: ", // a bad statement, and it goes on
		"n\n2\n(1 rows)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out.String())
		}
	}
}
