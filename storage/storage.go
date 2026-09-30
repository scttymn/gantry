// Package storage keeps the files an app's records have attached: Active
// Storage's blobs and attachments, in Rails' tables (gantry g storage
// writes them, as `rails active_storage:install` does), and on disk as
// Rails' disk service keeps them, so a Rails app's files carry over.
//
// A record has a file under a name, as has_one_attached:
//
//	thumb := storage.Ref{RecordType: "Clip", RecordID: clip.ID, Name: "thumbnail"}
//	blob, err := st.Attach(ctx, thumb, file)   // replaces the one it had
//	blob, ok, err := st.Find(ctx, thumb)
//	@st.Img(blob, images.Img{Alt: clip.Title, Sizes: "240px"})
//
// Pictures come as the images package's standard way: AVIF and WebP copies
// at every width up to the picture's own, made ahead in a child process
// that keeps out of the server's way (Warm), and drawn as a <picture>.
// Storage serves them, and any file's original, at Prefix.
package storage

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/images"
	"github.com/scttymn/gantry/web"
)

// Storage is an app's attached files.
type Storage struct {
	DB *db.DB
	// Root is the disk service's folder, on a volume: a blob's file is
	// Root/<key[0:2]>/<key[2:4]>/<key>, and pictures' copies are under
	// Root/variants.
	Root string
	// Prefix is where files are served: "/storage/" when empty.
	Prefix string
	// Images makes pictures' copies (WebP, by default). A server's makes
	// them in a child (images.Child), which the app's main starts before
	// anything else:
	//
	//	if images.IsChild(os.Args) {
	//		os.Exit(pictures.RunChild(os.Args))
	//	}
	//
	// Nil: WebP, made in this process (tests). Its Dir is Root/variants when
	// empty. Its PlaceholderWidth, when set, has Warm make a placeholder too.
	Images *images.Pipeline
	// AVIF makes the AVIF copies, offered first: images.AVIFFor(Images)
	// when nil, unless NoAVIF or this machine would make them at a crawl
	// (images.AVIFSlow).
	AVIF   *images.Pipeline
	NoAVIF bool
	// Quality is the copies' quality now (an admin's setting); the
	// pipeline's when nil.
	Quality func(ctx context.Context) (int, error)
	Log     *slog.Logger
	// Changed, when set, is called after Warm makes copies: pages that show
	// them (a cached one) are out of date.
	Changed func()

	once       sync.Once
	server     *images.Server
	warm       sync.Mutex
	background sync.WaitGroup
}

// Ref names an attachment: a record's file under a name.
type Ref struct {
	RecordType string // the record's type, as Rails names it: "Clip"
	RecordID   int64
	Name       string // "thumbnail"
}

// File is a file to store: what a form sent.
type File struct {
	Filename string
	Data     []byte
}

// Blob is a stored file.
type Blob struct {
	ID          int64
	Key         string
	Filename    string
	ContentType string
	ByteSize    int64
	Checksum    string // base64 MD5, as Rails'
	Width       int    // a picture's, upright; 0 when unknown
	Height      int
}

// FileFrom is the file a multipart form sent in field, if one was chosen.
func FileFrom(r *http.Request, field string) (File, bool) {
	name, data, ok := web.Upload(r, field)
	return File{Filename: filepath.Base(name), Data: data}, ok
}

func (s *Storage) setup() {
	s.once.Do(func() {
		if s.Log == nil {
			s.Log = slog.Default()
		}
		if s.Prefix == "" {
			s.Prefix = "/storage/"
		}
		if s.Images == nil {
			s.Images = &images.Pipeline{}
		}
		if s.Images.Dir == "" {
			s.Images.Dir = filepath.Join(s.Root, "variants")
		}
		if s.AVIF == nil && !s.NoAVIF && images.AVIFSlow() == "" {
			s.AVIF = images.AVIFFor(s.Images)
		}
		s.server = &images.Server{Pipeline: s.Images, AVIF: s.AVIF, Prefix: s.Prefix, Quality: s.Quality, Log: s.Log, Find: s.find}
	})
}

// Path is where a blob's file is.
func (s *Storage) Path(key string) string {
	return filepath.Join(s.Root, key[0:2], key[2:4], key)
}

// Attach stores f and attaches it at ref in place of what ref had, whose
// blob is purged (to keep that blob, Detach it first). The file is written
// first, then the rows go in together, so a crash leaves at most a file
// nothing names. A picture's copies are made in the background.
func (s *Storage) Attach(ctx context.Context, ref Ref, f File) (Blob, error) {
	return s.store(ctx, &ref, f)
}

// Store stores f as a blob attached to nothing (Rails' create_and_upload!),
// for AttachBlob to attach later. Unattached lists it until then.
func (s *Storage) Store(ctx context.Context, f File) (Blob, error) {
	return s.store(ctx, nil, f)
}

func (s *Storage) store(ctx context.Context, ref *Ref, f File) (Blob, error) {
	s.setup()
	b := Blob{Key: newKey(), Filename: f.Filename, ByteSize: int64(len(f.Data))}
	metadata := map[string]any{"identified": true}
	if width, height, contentType, err := s.Images.Dimensions(f.Data); err == nil {
		b.ContentType, b.Width, b.Height = contentType, width, height
		metadata["width"], metadata["height"], metadata["analyzed"] = width, height, true
	} else {
		b.ContentType = http.DetectContentType(f.Data)
	}
	sum := md5.Sum(f.Data)
	b.Checksum = base64.StdEncoding.EncodeToString(sum[:])
	if err := writeAtomically(s.Path(b.Key), f.Data); err != nil {
		return Blob{}, err
	}
	encoded, _ := json.Marshal(metadata)
	err := s.DB.Tx(ctx, func(tx *db.Tx) error {
		err := tx.QueryRowContext(ctx, `INSERT INTO active_storage_blobs (key, filename, content_type, metadata, service_name, byte_size, checksum, created_at)
			VALUES ($1, $2, $3, $4, 'local', $5, $6, $7) RETURNING id`,
			b.Key, b.Filename, b.ContentType, string(encoded), b.ByteSize, b.Checksum, time.Now().UTC()).Scan(&b.ID)
		if err != nil || ref == nil {
			return err
		}
		return s.replace(ctx, tx, *ref, b.ID)
	})
	if err != nil {
		os.Remove(s.Path(b.Key))
		return Blob{}, err
	}
	if ref != nil {
		s.WarmLater()
	}
	return b, nil
}

// AttachBlob attaches a blob already stored (one Detach kept) at ref, in
// place of what ref had, whose blob is purged.
func (s *Storage) AttachBlob(ctx context.Context, ref Ref, key string) (Blob, error) {
	s.setup()
	b, err := s.blob(ctx, s.DB.Read, `b.key = $1`, key)
	if err != nil {
		return Blob{}, err
	}
	if err := s.DB.Tx(ctx, func(tx *db.Tx) error { return s.replace(ctx, tx, ref, b.ID) }); err != nil {
		return Blob{}, err
	}
	s.WarmLater()
	return b, nil
}

// replace attaches blobID at ref, and purges the blobs ref had, once the
// transaction commits.
func (s *Storage) replace(ctx context.Context, tx *db.Tx, ref Ref, blobID int64) error {
	old, err := s.detach(ctx, tx, ref)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_storage_attachments (name, record_type, record_id, blob_id, created_at) VALUES ($1, $2, $3, $4, $5)`,
		ref.Name, ref.RecordType, ref.RecordID, blobID, time.Now().UTC()); err != nil {
		return err
	}
	for _, b := range old {
		if b.ID == blobID {
			continue
		}
		if err := s.deleteBlob(ctx, tx, b); err != nil {
			return err
		}
	}
	return nil
}

// Detach removes ref's attachment and keeps its blob, which Unattached then
// lists. ok is false when ref had none.
func (s *Storage) Detach(ctx context.Context, ref Ref) (b Blob, ok bool, err error) {
	s.setup()
	var old []Blob
	err = s.DB.Tx(ctx, func(tx *db.Tx) error {
		var err error
		old, err = s.detach(ctx, tx, ref)
		return err
	})
	if err != nil || len(old) == 0 {
		return Blob{}, false, err
	}
	return old[0], true, nil
}

func (s *Storage) detach(ctx context.Context, tx *db.Tx, ref Ref) ([]Blob, error) {
	old, err := s.blobs(ctx, tx, `a.record_type = $1 AND a.record_id = $2 AND a.name = $3`, ref.RecordType, ref.RecordID, ref.Name)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM active_storage_attachments WHERE record_type = $1 AND record_id = $2 AND name = $3`,
		ref.RecordType, ref.RecordID, ref.Name)
	return old, err
}

// Purge removes ref's attachment, its blob and the blob's files.
func (s *Storage) Purge(ctx context.Context, ref Ref) error {
	s.setup()
	return s.DB.Tx(ctx, func(tx *db.Tx) error {
		old, err := s.detach(ctx, tx, ref)
		if err != nil {
			return err
		}
		for _, b := range old {
			if err := s.deleteBlob(ctx, tx, b); err != nil {
				return err
			}
		}
		return nil
	})
}

// PurgeBlob removes a blob attached to nothing, and its files: an app's
// sweep of what Unattached lists. A blob still attached is left alone.
func (s *Storage) PurgeBlob(ctx context.Context, key string) error {
	s.setup()
	return s.DB.Tx(ctx, func(tx *db.Tx) error {
		b, err := s.blob(ctx, tx, `b.key = $1 AND NOT EXISTS (SELECT 1 FROM active_storage_attachments a WHERE a.blob_id = b.id)`, key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.deleteBlob(ctx, tx, b)
	})
}

// deleteBlob removes a blob's row (and any attachment still naming it:
// another record's), and its files once the transaction commits.
func (s *Storage) deleteBlob(ctx context.Context, tx *db.Tx, b Blob) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_storage_attachments WHERE blob_id = $1`, b.ID); err != nil {
		return err
	}
	// Rails' variant records name the blob too, in a database Rails made.
	// Inside a savepoint, so where the table is missing only this statement
	// is undone (on Postgres a failed statement would abort the transaction).
	if _, err := tx.ExecContext(ctx, `SAVEPOINT variant_records`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_storage_variant_records WHERE blob_id = $1`, b.ID); err != nil {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT variant_records`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT variant_records`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_storage_blobs WHERE id = $1`, b.ID); err != nil {
		return err
	}
	tx.AfterCommit(func() {
		os.Remove(s.Path(b.Key))
		s.Images.Forget(b.Key)
		if s.AVIF != nil {
			s.AVIF.Forget(b.Key)
		}
	})
	return nil
}

// Blob is the blob with this key, attached or not.
func (s *Storage) Blob(ctx context.Context, key string) (Blob, bool, error) {
	s.setup()
	b, err := s.blob(ctx, s.DB.Read, `b.key = $1`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return Blob{}, false, nil
	}
	return b, err == nil, err
}

// Find is the blob attached at ref.
func (s *Storage) Find(ctx context.Context, ref Ref) (Blob, bool, error) {
	s.setup()
	found, err := s.blobs(ctx, s.DB.Read, `a.record_type = $1 AND a.record_id = $2 AND a.name = $3`, ref.RecordType, ref.RecordID, ref.Name)
	if err != nil || len(found) == 0 {
		return Blob{}, false, err
	}
	return found[0], true, nil
}

// All is every blob attached under name to a record of recordType, by the
// record's id: a page's list in one query.
func (s *Storage) All(ctx context.Context, recordType, name string) (map[int64]Blob, error) {
	s.setup()
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT a.record_id, `+blobColumns+` FROM active_storage_attachments a
		JOIN active_storage_blobs b ON b.id = a.blob_id WHERE a.record_type = $1 AND a.name = $2 ORDER BY a.id`, recordType, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Blob{}
	for rows.Next() {
		var id int64
		b, err := scanBlob(rows, &id)
		if err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// Unattached is every blob attached to nothing: kept by Detach, or left by
// a crash between a file and its rows.
func (s *Storage) Unattached(ctx context.Context) ([]Blob, error) {
	s.setup()
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+blobColumns+` FROM active_storage_blobs b
		WHERE NOT EXISTS (SELECT 1 FROM active_storage_attachments a WHERE a.blob_id = b.id) ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Blob
	for rows.Next() {
		b, err := scanBlob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const blobColumns = `b.id, b.key, b.filename, COALESCE(b.content_type, ''), COALESCE(b.metadata, ''), b.byte_size, COALESCE(b.checksum, '')`

type scanner interface{ Scan(dest ...any) error }

// scanBlob reads blobColumns, after any leading columns into before.
func scanBlob(row scanner, before ...any) (Blob, error) {
	var b Blob
	var metadata string
	if err := row.Scan(append(before, &b.ID, &b.Key, &b.Filename, &b.ContentType, &metadata, &b.ByteSize, &b.Checksum)...); err != nil {
		return Blob{}, err
	}
	var m map[string]any
	if json.Unmarshal([]byte(metadata), &m) == nil {
		w, wok := m["width"].(float64)
		h, hok := m["height"].(float64)
		if wok && hok && w > 0 && h > 0 {
			b.Width, b.Height = int(w), int(h)
		}
	}
	return b, nil
}

func (s *Storage) blob(ctx context.Context, q db.Querier, where string, args ...any) (Blob, error) {
	return scanBlob(q.QueryRowContext(ctx, `SELECT `+blobColumns+` FROM active_storage_blobs b WHERE `+where, args...))
}

func (s *Storage) blobs(ctx context.Context, q db.Querier, where string, args ...any) ([]Blob, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+blobColumns+` FROM active_storage_attachments a
		JOIN active_storage_blobs b ON b.id = a.blob_id WHERE `+where+` ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Blob
	for rows.Next() {
		b, err := scanBlob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// find is the images server's lookup: only a blob attached to something is
// served.
func (s *Storage) find(ctx context.Context, key string) (images.Original, bool, error) {
	b, err := s.blob(ctx, s.DB.Read, `b.key = $1 AND EXISTS (SELECT 1 FROM active_storage_attachments a WHERE a.blob_id = b.id)`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return images.Original{}, false, nil
	}
	if err != nil {
		return images.Original{}, false, err
	}
	return images.Original{Path: s.Path(b.Key), ContentType: b.ContentType, Width: b.Width}, true, nil
}

// ServeHTTP serves at Prefix a picture's copies (/storage/<key>/720w-q80.webp)
// and any file's original (/storage/<key>/original), of blobs attached to
// something. Mount it: rt.Mount("GET /storage/", st).
func (s *Storage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.setup()
	s.server.ServeHTTP(w, r)
}

// URL is the address of a blob's original.
func (s *Storage) URL(b Blob) string {
	s.setup()
	return s.server.OriginalURL(b.Key)
}

// Picture reports whether the blob is a picture Storage makes copies of.
func (s *Storage) Picture(b Blob) bool {
	s.setup()
	return s.Images.Resizable(b.ContentType)
}

// Img draws a blob: a picture as a <picture> of its AVIF and WebP copies at
// every width it comes in (once its AVIF copies are made), sized so the page
// doesn't shift as it loads; any other file as an <img> of its original.
func (s *Storage) Img(b Blob, o images.Img) templ.Component {
	s.setup()
	p := images.Photo{Key: b.Key, ContentType: b.ContentType, Width: b.Width, Height: b.Height}
	if s.Images.PlaceholderWidth > 0 {
		p.Placeholder = s.Images.Placeholder(b.Key)
	}
	if s.Quality != nil {
		if q, err := s.Quality(context.Background()); err == nil {
			p.Quality = q
		}
	}
	return s.server.Img(p, o)
}

// Warm makes every attached picture's missing copies, ahead of anyone
// asking: its placeholder (when the pipeline has one), its WebP copies, then
// its AVIF ones. A picture without a recorded size is analysed first. One
// unreadable file doesn't stop the rest; a machine without the memory free
// for a copy (images.ErrNoRoom) does, and the next warming carries on. One
// warming runs at a time. made is how many copies it made.
func (s *Storage) Warm(ctx context.Context) (made int, err error) {
	s.setup()
	s.warm.Lock()
	defer s.warm.Unlock()
	defer func() {
		s.Log.Info(fmt.Sprintf("[storage] made %d cop%s", made, map[bool]string{true: "y", false: "ies"}[made == 1]))
		if errors.Is(err, images.ErrNoRoom) {
			s.Log.Warn("[storage] stopped: the rest wait for room", "err", err)
		}
		if made > 0 && s.Changed != nil {
			s.Changed()
		}
	}()
	quality := 0
	if s.Quality != nil {
		if quality, err = s.Quality(ctx); err != nil {
			return 0, err
		}
	}
	attached, err := s.blobs(ctx, s.DB.Read, `1 = 1`)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, b := range attached {
		if seen[b.Key] {
			continue
		}
		seen[b.Key] = true
		if b.Width == 0 {
			if err := s.analyze(ctx, &b); err != nil {
				s.Log.Warn("[storage] could not read", "file", b.Filename, "key", b.Key, "err", err)
				continue
			}
		}
		if !s.Images.Resizable(b.ContentType) {
			continue
		}
		type want struct {
			p              *images.Pipeline
			width, quality int
		}
		if s.Images.PlaceholderWidth > 0 && !s.Images.HasPlaceholder(b.Key) {
			if err := s.Images.MakePlaceholder(ctx, b.Key, s.Path(b.Key)); err != nil {
				if errors.Is(err, images.ErrNoRoom) {
					return made, err
				}
				s.Log.Warn("[storage] could not make a placeholder", "file", b.Filename, "key", b.Key, "err", err)
			} else {
				made++
			}
		}
		var copies []want
		for _, w := range s.Images.WidthsFor(b.Width) {
			copies = append(copies, want{s.Images, w, quality})
		}
		if s.AVIF != nil {
			for _, w := range s.AVIF.WidthsFor(b.Width) {
				copies = append(copies, want{s.AVIF, w, 0})
			}
		}
		for _, c := range copies {
			if err := ctx.Err(); err != nil {
				return made, err
			}
			if c.p.Has(b.Key, c.width, c.quality) {
				continue
			}
			if _, err := c.p.Copy(ctx, b.Key, s.Path(b.Key), c.width, c.quality); err != nil {
				if errors.Is(err, images.ErrNoRoom) {
					return made, err
				}
				s.Log.Warn("[storage] could not make a copy", "file", b.Filename, "key", b.Key, "width", c.width, "err", err)
				continue
			}
			made++
		}
	}
	return made, nil
}

// WarmLater warms in the background: after an upload, and at start.
func (s *Storage) WarmLater() {
	s.background.Go(func() { s.Warm(context.Background()) })
}

// Wait waits for the background warmings to finish.
func (s *Storage) Wait() { s.background.Wait() }

// analyze records a picture's type and size in its blob's metadata, as
// Active Storage's identify and analyze do: for a Rails blob not analysed
// yet.
func (s *Storage) analyze(ctx context.Context, b *Blob) error {
	data, err := os.ReadFile(s.Path(b.Key))
	if err != nil {
		return err
	}
	width, height, contentType, err := s.Images.Dimensions(data)
	if err != nil {
		if !strings.HasPrefix(b.ContentType, "image/") {
			return nil // not a picture: nothing to record
		}
		return err
	}
	var raw string
	var metadata map[string]any
	s.DB.Write.QueryRowContext(ctx, `SELECT COALESCE(metadata, '') FROM active_storage_blobs WHERE id = $1`, b.ID).Scan(&raw)
	if json.Unmarshal([]byte(raw), &metadata) != nil || metadata == nil {
		metadata = map[string]any{}
	}
	metadata["identified"], metadata["analyzed"], metadata["width"], metadata["height"] = true, true, width, height
	encoded, _ := json.Marshal(metadata)
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE active_storage_blobs SET content_type = $1, metadata = $2 WHERE id = $3`,
		contentType, string(encoded), b.ID); err != nil {
		return err
	}
	b.ContentType, b.Width, b.Height = contentType, width, height
	return nil
}

// newKey is an Active Storage key: 28 random lowercase letters and digits.
func newKey() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 28)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

// writeAtomically writes data beside path and renames it into place, so a
// reader never sees half a file.
func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
