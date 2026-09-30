package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/images"
	"github.com/scttymn/gantry/testkit"
)

// Rails' tables, as gantry g storage writes them for SQLite, with Rails'
// variant records beside them, as in a database Rails made.
var schema = fstest.MapFS{"00001_storage.sql": {Data: []byte(`-- +goose Up
CREATE TABLE active_storage_blobs (id integer PRIMARY KEY AUTOINCREMENT NOT NULL, key varchar NOT NULL, filename varchar NOT NULL,
  content_type varchar, metadata text, service_name varchar NOT NULL, byte_size bigint NOT NULL, checksum varchar, created_at datetime(6) NOT NULL);
CREATE UNIQUE INDEX index_active_storage_blobs_on_key ON active_storage_blobs (key);
CREATE TABLE active_storage_attachments (id integer PRIMARY KEY AUTOINCREMENT NOT NULL, name varchar NOT NULL, record_type varchar NOT NULL,
  record_id bigint NOT NULL, blob_id bigint NOT NULL REFERENCES active_storage_blobs (id), created_at datetime(6) NOT NULL);
CREATE UNIQUE INDEX index_active_storage_attachments_uniqueness ON active_storage_attachments (record_type, record_id, name, blob_id);
CREATE TABLE active_storage_variant_records (id integer PRIMARY KEY AUTOINCREMENT NOT NULL, blob_id bigint NOT NULL REFERENCES active_storage_blobs (id),
  variation_digest varchar NOT NULL);
`)}}

func newStorage(t *testing.T) *Storage {
	t.Helper()
	d := testkit.DB(t, func(ctx context.Context, d *db.DB) error { return d.Migrate(ctx, schema, "app_migrations") })
	s := &Storage{DB: d, Root: t.TempDir(), NoAVIF: true, Log: slog.New(slog.DiscardHandler)}
	t.Cleanup(s.Wait)
	return s
}

// picture is a JPEG this size.
func picture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var ctx = context.Background()

func clip(id int64) Ref { return Ref{RecordType: "Clip", RecordID: id, Name: "thumbnail"} }

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func count(t *testing.T, s *Storage, table string) int {
	t.Helper()
	var n int
	if err := s.DB.Read.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAttach(t *testing.T) {
	s := newStorage(t)
	data := picture(t, 400, 300)
	b, err := s.Attach(ctx, clip(1), File{Filename: "still.jpg", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\A[0-9a-z]{28}\z`).MatchString(b.Key) {
		t.Errorf("key %q isn't Rails'", b.Key)
	}
	sum := md5.Sum(data)
	if b.Checksum != base64.StdEncoding.EncodeToString(sum[:]) || b.ByteSize != int64(len(data)) || b.ContentType != "image/jpeg" ||
		b.Width != 400 || b.Height != 300 || b.Filename != "still.jpg" {
		t.Errorf("blob %+v", b)
	}
	got, err := os.ReadFile(filepath.Join(s.Root, b.Key[0:2], b.Key[2:4], b.Key))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file at Rails' path: %v", err)
	}
	var metadata, service string
	s.DB.Read.QueryRow(`SELECT metadata, service_name FROM active_storage_blobs WHERE id = $1`, b.ID).Scan(&metadata, &service)
	var m map[string]any
	json.Unmarshal([]byte(metadata), &m)
	if m["identified"] != true || m["analyzed"] != true || m["width"] != 400.0 || m["height"] != 300.0 || service != "local" {
		t.Errorf("metadata %s, service %q", metadata, service)
	}
	found, ok, err := s.Find(ctx, clip(1))
	if err != nil || !ok || found != b {
		t.Fatalf("Find: %+v %v %v", found, ok, err)
	}
	if _, ok, _ := s.Find(ctx, clip(2)); ok {
		t.Error("another record has nothing attached")
	}
	// A file that isn't a picture is kept as it is.
	txt, err := s.Attach(ctx, Ref{"Clip", 1, "script"}, File{Filename: "notes.txt", Data: []byte("hello there")})
	if err != nil || !strings.HasPrefix(txt.ContentType, "text/plain") || txt.Width != 0 || s.Picture(txt) {
		t.Errorf("text: %+v %v", txt, err)
	}
}

func TestAttachReplaces(t *testing.T) {
	s := newStorage(t)
	old, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 200, 100)})
	s.Wait()
	if !s.Images.Has(old.Key, 160, 0) {
		t.Fatal("the upload's copies are made in the background")
	}
	b, err := s.Attach(ctx, clip(1), File{"b.jpg", picture(t, 300, 100)})
	if err != nil {
		t.Fatal(err)
	}
	if found, _, _ := s.Find(ctx, clip(1)); found.Key != b.Key {
		t.Errorf("found %s, want the new one", found.Key)
	}
	if n := count(t, s, "active_storage_blobs"); n != 1 {
		t.Errorf("%d blobs, want the old one purged", n)
	}
	if exists(s.Path(old.Key)) || exists(filepath.Join(s.Images.Dir, old.Key)) {
		t.Error("the old file and its copies stay")
	}
}

func TestDetachAndAttachBlob(t *testing.T) {
	s := newStorage(t)
	a, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 200, 100)})
	got, ok, err := s.Detach(ctx, clip(1))
	if err != nil || !ok || got.Key != a.Key {
		t.Fatalf("Detach: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.Find(ctx, clip(1)); ok {
		t.Error("detached, but still found")
	}
	if !exists(s.Path(a.Key)) {
		t.Error("Detach keeps the file")
	}
	loose, _ := s.Unattached(ctx)
	if len(loose) != 1 || loose[0].Key != a.Key {
		t.Fatalf("Unattached: %+v", loose)
	}
	// A new thumbnail, then the old one back (a restore): the new one, not
	// detached, is purged.
	b, _ := s.Attach(ctx, clip(1), File{"b.jpg", picture(t, 200, 100)})
	if _, err := s.AttachBlob(ctx, clip(1), a.Key); err != nil {
		t.Fatal(err)
	}
	if found, _, _ := s.Find(ctx, clip(1)); found.Key != a.Key || found.Width != 200 {
		t.Errorf("found %+v, want the restored one", found)
	}
	if exists(s.Path(b.Key)) {
		t.Error("the replaced one stays")
	}
	if loose, _ := s.Unattached(ctx); len(loose) != 0 {
		t.Errorf("Unattached: %+v", loose)
	}
	// PurgeBlob leaves an attached blob alone, and removes a loose one.
	if err := s.PurgeBlob(ctx, a.Key); err != nil || !exists(s.Path(a.Key)) {
		t.Fatalf("an attached blob was purged: %v", err)
	}
	s.Detach(ctx, clip(1))
	if err := s.PurgeBlob(ctx, a.Key); err != nil || exists(s.Path(a.Key)) || count(t, s, "active_storage_blobs") != 0 {
		t.Fatalf("a loose blob stays: %v", err)
	}
	if _, err := s.AttachBlob(ctx, clip(1), "nosuchkey"); err == nil {
		t.Error("attached a blob that isn't there")
	}
}

func TestStore(t *testing.T) {
	s := newStorage(t)
	b, err := s.Store(ctx, File{"a.jpg", picture(t, 200, 100)})
	if err != nil || b.Width != 200 || !exists(s.Path(b.Key)) {
		t.Fatalf("Store: %+v %v", b, err)
	}
	if loose, _ := s.Unattached(ctx); len(loose) != 1 || loose[0].Key != b.Key {
		t.Errorf("a stored blob is attached to nothing: %+v", loose)
	}
	if got, ok, err := s.Blob(ctx, b.Key); err != nil || !ok || got != b {
		t.Errorf("Blob: %+v %v %v", got, ok, err)
	}
	if _, ok, err := s.Blob(ctx, "nosuchkey"); ok || err != nil {
		t.Errorf("Blob of no key: %v %v", ok, err)
	}
	if _, err := s.AttachBlob(ctx, clip(1), b.Key); err != nil {
		t.Fatal(err)
	}
	if found, ok, _ := s.Find(ctx, clip(1)); !ok || found.Key != b.Key {
		t.Errorf("attached later: %+v", found)
	}
}

func TestPurge(t *testing.T) {
	s := newStorage(t)
	a, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 200, 100)})
	b, _ := s.Attach(ctx, clip(2), File{"b.jpg", picture(t, 200, 100)})
	s.Wait()
	if err := s.Purge(ctx, clip(1)); err != nil {
		t.Fatal(err)
	}
	if exists(s.Path(a.Key)) || exists(filepath.Join(s.Images.Dir, a.Key)) || count(t, s, "active_storage_attachments") != 1 || count(t, s, "active_storage_blobs") != 1 {
		t.Error("the purged one's rows or files stay")
	}
	if !exists(s.Path(b.Key)) || !s.Images.Has(b.Key, 160, 0) {
		t.Error("another record's went too")
	}
	if err := s.Purge(ctx, clip(3)); err != nil {
		t.Errorf("nothing to purge: %v", err)
	}
}

func TestAll(t *testing.T) {
	s := newStorage(t)
	a, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 200, 100)})
	b, _ := s.Attach(ctx, clip(2), File{"b.jpg", picture(t, 120, 90)})
	s.Attach(ctx, Ref{"Member", 1, "thumbnail"}, File{"c.jpg", picture(t, 50, 50)})
	s.Attach(ctx, Ref{"Clip", 3, "poster"}, File{"d.jpg", picture(t, 50, 50)})
	all, err := s.All(ctx, "Clip", "thumbnail")
	if err != nil || len(all) != 2 || all[1] != a || all[2] != b {
		t.Fatalf("All: %+v %v", all, err)
	}
}

func TestServe(t *testing.T) {
	s := newStorage(t)
	a, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 200, 100)})
	txt, _ := s.Attach(ctx, Ref{"Clip", 1, "script"}, File{"notes.txt", []byte("hello there")})
	loose, _ := s.Attach(ctx, clip(2), File{"b.jpg", picture(t, 200, 100)})
	s.Detach(ctx, clip(2))
	s.Wait()
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	if w := get("/storage/" + a.Key + "/160w-q80.webp"); w.Code != 200 || w.Header().Get("Content-Type") != "image/webp" {
		t.Errorf("a copy: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(s.URL(txt)); w.Code != 200 || w.Body.String() != "hello there" {
		t.Errorf("an original: %d %q", w.Code, w.Body.String())
	}
	for _, path := range []string{"/storage/" + loose.Key + "/160w-q80.webp", "/storage/" + loose.Key + "/original", "/storage/nosuchkey/original"} {
		if w := get(path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404 (not attached)", path, w.Code)
		}
	}
}

func render(t *testing.T, s *Storage, b Blob, o images.Img) string {
	t.Helper()
	var buf strings.Builder
	if err := s.Img(b, o).Render(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestImg(t *testing.T) {
	if why := images.AVIFSlow(); why != "" {
		t.Skip(why)
	}
	s := newStorage(t)
	s.NoAVIF = false
	a, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 400, 300)})
	s.Wait()
	html := render(t, s, a, images.Img{Alt: "Still", Sizes: "240px"})
	for _, want := range []string{`<picture>`, `type="image/avif"`, `/storage/` + a.Key + `/320w-q50.avif 320w`, `/storage/` + a.Key + `/320w-q80.webp 320w`,
		`width="400"`, `height="300"`, `alt="Still"`, `sizes="240px"`} {
		if !strings.Contains(html, want) {
			t.Errorf("no %s in %s", want, html)
		}
	}
	txt, _ := s.Attach(ctx, Ref{"Clip", 1, "script"}, File{"notes.txt", []byte("hello")})
	if html := render(t, s, txt, images.Img{}); !strings.Contains(html, `src="/storage/`+txt.Key+`/original"`) {
		t.Errorf("a file that isn't a picture: %s", html)
	}
}

// noRoom is a machine without the memory for a copy.
type noRoom struct{}

func (noRoom) Make(context.Context, string, string, int, int) error { return images.ErrNoRoom }

func TestWarm(t *testing.T) {
	s := newStorage(t)
	s.Images = &images.Pipeline{PlaceholderWidth: 32}
	a, _ := s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 500, 300)})
	s.Attach(ctx, Ref{"Clip", 1, "script"}, File{"notes.txt", []byte("hello")})
	s.Wait()
	for _, w := range s.Images.WidthsFor(500) {
		if !s.Images.Has(a.Key, w, 0) {
			t.Errorf("no %dw copy", w)
		}
	}
	if !s.Images.HasPlaceholder(a.Key) {
		t.Error("no placeholder")
	}
	if made, err := s.Warm(ctx); made != 0 || err != nil {
		t.Errorf("a second warming made %d (%v): only what's missing", made, err)
	}
	// A Rails blob not analysed yet is analysed, then warmed.
	s.DB.Write.Exec(`UPDATE active_storage_blobs SET metadata = '{"identified":true}' WHERE id = $1`, a.ID)
	s.Images.Forget(a.Key)
	if made, err := s.Warm(ctx); made != len(s.Images.WidthsFor(500))+1 || err != nil {
		t.Errorf("made %d (%v) after forgetting", made, err)
	}
	if found, _, _ := s.Find(ctx, clip(1)); found.Width != 500 || found.Height != 300 {
		t.Errorf("not analysed: %+v", found)
	}
	// Without the room for a copy, warming stops, and says why.
	s.Images.Forget(a.Key)
	s.Images.Maker = noRoom{}
	if made, err := s.Warm(ctx); made != 0 || !errors.Is(err, images.ErrNoRoom) {
		t.Errorf("made %d, err %v: want a stop at ErrNoRoom", made, err)
	}
	s.Images.PlaceholderWidth = 0 // a copy's turn to find no room
	if made, err := s.Warm(ctx); made != 0 || !errors.Is(err, images.ErrNoRoom) {
		t.Errorf("made %d, err %v: want a stop at ErrNoRoom", made, err)
	}
}

// Rows Rails wrote read as they are: its timestamps, its metadata, and its
// variant records, which purging clears.
func TestRailsRows(t *testing.T) {
	s := newStorage(t)
	key := "abcdefghijklmnopqrstuvwxyz01"
	data := picture(t, 240, 160)
	os.MkdirAll(filepath.Dir(s.Path(key)), 0o755)
	os.WriteFile(s.Path(key), data, 0o644)
	w := s.DB.Write
	w.Exec(`INSERT INTO active_storage_blobs (id, key, filename, content_type, metadata, service_name, byte_size, checksum, created_at)
		VALUES (7, $1, 'hero.jpg', 'image/jpeg', '{"identified":true,"width":240,"height":160,"analyzed":true}', 'local', $2, 'x', '2024-03-01 12:00:00.123456')`, key, len(data))
	w.Exec(`INSERT INTO active_storage_attachments (name, record_type, record_id, blob_id, created_at) VALUES ('hero_photo', 'Site', 1, 7, '2024-03-01 12:00:00.123456')`)
	w.Exec(`INSERT INTO active_storage_variant_records (blob_id, variation_digest) VALUES (7, 'digest')`)
	b, ok, err := s.Find(ctx, Ref{"Site", 1, "hero_photo"})
	if err != nil || !ok || b.Width != 240 || b.Filename != "hero.jpg" {
		t.Fatalf("Find: %+v %v %v", b, ok, err)
	}
	w2 := httptest.NewRecorder()
	s.ServeHTTP(w2, httptest.NewRequest("GET", "/storage/"+key+"/240w-q80.webp", nil))
	if body, _ := io.ReadAll(w2.Body); w2.Code != 200 || len(body) == 0 {
		t.Errorf("serve: %d", w2.Code)
	}
	if err := s.Purge(ctx, Ref{"Site", 1, "hero_photo"}); err != nil {
		t.Fatal(err)
	}
	if count(t, s, "active_storage_variant_records") != 0 || exists(s.Path(key)) {
		t.Error("Rails' variant records or the file stay")
	}
}

// Without Rails' variant records table, purging still works.
func TestPurgeWithoutVariantRecords(t *testing.T) {
	s := newStorage(t)
	s.DB.Write.Exec(`DROP TABLE active_storage_variant_records`)
	s.Attach(ctx, clip(1), File{"a.jpg", picture(t, 100, 100)})
	if err := s.Purge(ctx, clip(1)); err != nil || count(t, s, "active_storage_blobs") != 0 {
		t.Fatalf("purge: %v", err)
	}
}
