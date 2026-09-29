package db

import (
	"context"
	"os"
	"testing"
)

type settings struct {
	Theme string   `json:"theme"`
	Tags  []string `json:"tags"`
}

func checkJSON(t *testing.T, d *DB) {
	t.Helper()
	col := "text"
	if d.Engine == Postgres {
		col = "jsonb"
		d.Write.Exec(`DROP TABLE IF EXISTS j_prefs`)
	}
	if _, err := d.Write.Exec(`CREATE TABLE j_prefs (id bigint PRIMARY KEY, data ` + col + `)`); err != nil {
		t.Fatal(err)
	}
	in := JSON[settings]{V: settings{Theme: "dark", Tags: []string{"a", "b"}}}
	if _, err := d.Write.Exec(`INSERT INTO j_prefs (id, data) VALUES (1, $1), (2, NULL)`, in); err != nil {
		t.Fatal(err)
	}
	var out JSON[settings]
	if err := d.Read.QueryRow(`SELECT data FROM j_prefs WHERE id = 1`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	if out.V.Theme != "dark" || len(out.V.Tags) != 2 || out.V.Tags[1] != "b" {
		t.Errorf("read back %+v", out.V)
	}
	var none JSON[settings]
	none.V.Theme = "stale"
	if err := d.Read.QueryRow(`SELECT data FROM j_prefs WHERE id = 2`).Scan(&none); err != nil || none.V.Theme != "" {
		t.Errorf("NULL: %+v %v; want the zero value", none.V, err)
	}
	// The same SQL reads inside it, per engine.
	q := `SELECT json_extract(data, '$.theme') FROM j_prefs WHERE id = 1`
	if d.Engine == Postgres {
		q = `SELECT data->>'theme' FROM j_prefs WHERE id = 1`
	}
	var theme string
	if err := d.Read.QueryRow(q).Scan(&theme); err != nil || theme != "dark" {
		t.Errorf("inside the JSON: %q %v", theme, err)
	}
}

func TestJSON(t *testing.T) { checkJSON(t, openTemp(t)) }

func TestJSONPostgres(t *testing.T) {
	url := os.Getenv("GANTRY_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("GANTRY_TEST_POSTGRES_URL isn't set")
	}
	d, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	checkJSON(t, d)
}
