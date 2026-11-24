//go:build linux

package db

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

const noTempDirEnv = "GANTRY_TEST_NO_TEMP_DIR"

func TestSQLiteWithoutTempDir(t *testing.T) {
	if os.Getenv(noTempDirEnv) != "" {
		sqliteWithoutTempDir(t)
		return
	}
	if os.Getuid() != 0 {
		t.Skip("chroot needs root (bin/go runs the tests as root)")
	}
	root := t.TempDir()
	const uid = 65532
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(data, uid, uid); err != nil {
		t.Fatal(err)
	}
	copyTestBinary(t, filepath.Join(root, "db.test"))

	cmd := exec.Command("/db.test", "-test.run=^TestSQLiteWithoutTempDir$", "-test.v")
	cmd.Dir = "/"
	cmd.Env = []string{noTempDirEnv + "=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Chroot: root, Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("as %d in a root with only /data: %v\n%s", uid, err, out)
	}
}

func copyTestBinary(t *testing.T, to string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

func sqliteWithoutTempDir(t *testing.T) {
	for _, dir := range []string{"/tmp", "/var/tmp", "/usr/tmp"} {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("%s exists: this isn't a root without a temporary directory", dir)
		}
	}
	if f, err := os.Create("/writable"); err == nil {
		f.Close()
		t.Fatal("/ is writable: this isn't a root without a temporary directory")
	}
	ctx := context.Background()
	d, err := Open(ctx, "sqlite:///data/app.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`CREATE TABLE users (id integer PRIMARY KEY)`,
		`CREATE TABLE posts (id integer PRIMARY KEY, user_id integer NOT NULL REFERENCES users (id), body text NOT NULL, created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE pages (id integer PRIMARY KEY, n integer NOT NULL DEFAULT 0, pad blob NOT NULL)`,
		`CREATE TABLE keys (id integer PRIMARY KEY, k blob NOT NULL)`,
		`INSERT INTO users (id) VALUES (1)`,
		`WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 64)
			INSERT INTO pages (pad) SELECT randomblob(3000) FROM n`,
		`WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 40000)
			INSERT INTO keys (k) SELECT randomblob(200) FROM n`,
	} {
		if _, err := d.Write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("INSERT RETURNING in a Tx", func(t *testing.T) {
		var id int64
		var createdAt string
		err := d.Tx(ctx, func(tx *Tx) error {
			return tx.QueryRowContext(ctx, `INSERT INTO posts (user_id, body) VALUES (?, ?) RETURNING id, created_at`, 1, "hello").Scan(&id, &createdAt)
		})
		if err != nil || id == 0 {
			t.Errorf("id %d, %v", id, err)
		}
	})
	t.Run("a statement in a Tx that changes more than 64 KB of pages", func(t *testing.T) {
		err := d.Tx(ctx, func(tx *Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE pages SET n = n + 1`)
			return err
		})
		if err != nil {
			t.Error(err)
		}
		var n int
		err = d.Tx(ctx, func(tx *Tx) error {
			rows, err := tx.QueryContext(ctx, `UPDATE pages SET n = n + 1 RETURNING id`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				n++
			}
			return rows.Err()
		})
		if err != nil || n != 64 {
			t.Errorf("RETURNING: %d rows, %v", n, err)
		}
	})
	t.Run("a sort larger than the page cache", func(t *testing.T) {
		var id int64
		if err := d.Read.QueryRowContext(ctx, `SELECT id FROM keys ORDER BY k LIMIT 1 OFFSET 39999`).Scan(&id); err != nil {
			t.Error(err)
		}
	})
	t.Run("CREATE INDEX on a table larger than the page cache", func(t *testing.T) {
		if _, err := d.Write.ExecContext(ctx, `CREATE INDEX index_keys_on_k ON keys (k)`); err != nil {
			t.Error(err)
		}
	})
	t.Run("VACUUM", func(t *testing.T) {
		if _, err := d.Write.ExecContext(ctx, `VACUUM`); err != nil {
			t.Error(err)
		}
	})
}
