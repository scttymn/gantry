package images

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/image/webp"

	"github.com/scttymn/gantry/images/heic"
)

func decodeWebP(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func picture(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

// withOrientation puts an EXIF orientation into a JPEG, as a phone does.
func withOrientation(jpg []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	entry := make([]byte, 2+12+4)
	binary.BigEndian.PutUint16(entry[0:2], 1)
	binary.BigEndian.PutUint16(entry[2:4], 0x0112)
	binary.BigEndian.PutUint16(entry[4:6], 3)
	binary.BigEndian.PutUint32(entry[6:10], 1)
	binary.BigEndian.PutUint16(entry[10:12], o)
	segment := append([]byte("Exif\x00\x00"), append(tiff, entry...)...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:4], uint16(len(segment)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)
	out = append(out, segment...)
	return append(out, jpg[2:]...)
}

func TestResize(t *testing.T) {
	p := &Pipeline{}
	t.Run("resize fits within the width and never enlarges", func(t *testing.T) {
		out, err := p.Resize(picture(400, 200), 100, 80)
		if err != nil {
			t.Fatal(err)
		}
		if b := decodeWebP(t, out).Bounds(); b.Dx() != 100 || b.Dy() != 50 {
			t.Errorf("%v", b)
		}
		// A tall photo is limited by twice the width.
		out, _ = p.Resize(picture(100, 1000), 100, 80)
		if b := decodeWebP(t, out).Bounds(); b.Dx() != 20 || b.Dy() != 200 {
			t.Errorf("tall: %v", b)
		}
		out, _ = p.Resize(picture(80, 40), 2000, 80)
		if b := decodeWebP(t, out).Bounds(); b.Dx() != 80 || b.Dy() != 40 {
			t.Errorf("enlarged: %v", b)
		}
		if _, err := p.Resize([]byte("not an image"), 100, 80); err == nil {
			t.Error("resized garbage")
		}
	})

	t.Run("a rotated phone photo comes out upright", func(t *testing.T) {
		// Stored sideways: red on the left, blue on the right. Orientation 6
		// means turn it 90° clockwise, so red ends up on top.
		data := withOrientation(picture(40, 20), 6)
		w, h, contentType, err := p.Dimensions(data)
		if err != nil || w != 20 || h != 40 || contentType != "image/jpeg" {
			t.Fatalf("%d×%d %s %v", w, h, contentType, err)
		}
		out, err := p.Resize(data, 2000, 90)
		if err != nil {
			t.Fatal(err)
		}
		img := decodeWebP(t, out)
		if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
			t.Fatalf("%v", b)
		}
		r, _, bl, _ := img.At(10, 5).RGBA()
		if r>>8 < 200 || bl>>8 > 60 {
			t.Errorf("the top isn't red: %d %d", r>>8, bl>>8)
		}
		r, _, bl, _ = img.At(10, 35).RGBA()
		if bl>>8 < 200 || r>>8 > 60 {
			t.Errorf("the bottom isn't blue: %d %d", r>>8, bl>>8)
		}
		for o := uint16(1); o <= 8; o++ {
			w, h, _, _ := p.Dimensions(withOrientation(picture(40, 20), o))
			if (o >= 5 && (w != 20 || h != 40)) || (o < 5 && (w != 40 || h != 20)) {
				t.Errorf("orientation %d: %d×%d", o, w, h)
			}
		}
	})
}

func TestEncoders(t *testing.T) {
	src := picture(400, 200)
	t.Run("copies are WebP at 80 unless the app says otherwise", func(t *testing.T) {
		p := &Pipeline{}
		out, err := p.Resize(src, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, typ, _ := p.Dimensions(out); typ != "image/webp" || p.ContentType() != "image/webp" || p.Name(100, p.QualityOr(0)) != "100w-q80.webp" {
			t.Errorf("%s %s %s", typ, p.ContentType(), p.Name(100, 80))
		}
	})
	t.Run("another encoder writes its own format", func(t *testing.T) {
		p := &Pipeline{Encoder: JPEG{}, Quality: 60}
		out, err := p.Resize(src, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, typ, _ := p.Dimensions(out); typ != "image/jpeg" || p.Name(100, 60) != "100w-q60.jpg" {
			t.Errorf("%s %s", typ, p.Name(100, 60))
		}
	})
	t.Run("a lower quality is a smaller file", func(t *testing.T) {
		p := &Pipeline{}
		hi, _ := p.Resize(src, 400, 95)
		lo, _ := p.Resize(src, 400, 30)
		if len(lo) >= len(hi) {
			t.Errorf("q30 %d bytes, q95 %d", len(lo), len(hi))
		}
	})
}

func TestNames(t *testing.T) {
	p := &Pipeline{}
	for name, want := range map[string][2]int{"1400w-q80.webp": {1400, 80}, "32w-q50.webp": {32, 50}} {
		if w, q, ok := p.ParseName(name); !ok || w != want[0] || q != want[1] {
			t.Errorf("%s: %d %d %v", name, w, q, ok)
		}
	}
	for _, name := range []string{"1400w-q80.jpg", "1400w-q080.webp", "01400w-q80.webp", "1400w-q80.webp.png", "x", "../1400w-q80.webp", "1400w-q.webp"} {
		if _, _, ok := p.ParseName(name); ok {
			t.Errorf("%s parsed", name)
		}
	}
	if got := Srcset([]int{800, 1400}, func(w int) string { return "/i/" + p.Name(w, 80) }); got != "/i/800w-q80.webp 800w, /i/1400w-q80.webp 1400w" {
		t.Error(got)
	}
}

func TestWidths(t *testing.T) {
	p := &Pipeline{}
	t.Run("a photo comes in each width up to its own, never enlarged", func(t *testing.T) {
		for original, want := range map[int][]int{
			800:  {320, 480, 720},
			720:  {320, 480, 720},
			2400: {320, 480, 720, 1080, 1600, 2400},
			6000: {320, 480, 720, 1080, 1600, 2400},
			200:  {320}, // the smallest, at its own size
			0:    {320, 480, 720, 1080, 1600, 2400},
		} {
			if got := p.WidthsFor(original); !slices.Equal(got, want) {
				t.Errorf("%dpx: %v", original, got)
			}
		}
	})

	t.Run("a request above the photo's largest gets its largest; one off the list gets nothing", func(t *testing.T) {
		for _, c := range []struct{ width, original, want int }{
			{1080, 800, 720}, {2400, 800, 720}, {480, 800, 480}, {720, 800, 720}, {2400, 0, 2400}, {1600, 200, 320},
		} {
			if got, ok := p.Fit(c.width, c.original); !ok || got != c.want {
				t.Errorf("%d of %dpx: %d %v", c.width, c.original, got, ok)
			}
		}
		for _, w := range []int{440, 800, 32, 0, 5000} {
			if _, ok := p.Fit(w, 3000); ok {
				t.Errorf("%d fit", w)
			}
		}
	})

	t.Run("an app can set its own", func(t *testing.T) {
		p := &Pipeline{Widths: []int{400, 800}}
		if got := p.WidthsFor(1000); !slices.Equal(got, []int{400, 800}) {
			t.Error(got)
		}
		if _, ok := p.Fit(720, 1000); ok {
			t.Error("the default widths still fit")
		}
	})

	t.Run("WidthsFor's result can't be appended into the list", func(t *testing.T) {
		got := append(p.WidthsFor(800), 999)
		if Widths[3] != 1080 || len(got) != 4 {
			t.Error(Widths)
		}
	})
}

func TestHEIC(t *testing.T) {
	data, err := os.ReadFile("testdata/photo.heic")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("without the adapter a HEIC isn't read", func(t *testing.T) {
		p := &Pipeline{}
		if p.Resizable("image/heic") {
			t.Error("resizable without a decoder")
		}
		if _, err := p.Resize(data, 100, 80); err == nil {
			t.Error("resized without a decoder")
		}
	})
	t.Run("with it, a HEIC is sized and resized like any photo", func(t *testing.T) {
		p := &Pipeline{Decoders: []Decoder{heic.Decoder{}}}
		w, h, typ, err := p.Dimensions(data)
		if err != nil || w != 400 || h != 500 || typ != "image/heic" || !p.Resizable(typ) {
			t.Fatalf("%d×%d %s %v", w, h, typ, err)
		}
		out, err := p.Resize(data, 200, 80)
		if err != nil {
			t.Fatal(err)
		}
		if b := decodeWebP(t, out).Bounds(); b.Dx() != 200 || b.Dy() != 250 {
			t.Errorf("%v", b)
		}
	})
	t.Run("the adapter only claims HEIC", func(t *testing.T) {
		if (heic.Decoder{}).Match(picture(10, 10)) || !(heic.Decoder{}).Match(data) {
			t.Error("matched the wrong bytes")
		}
	})
}

// counting makes copies in process and counts them; fail makes it fail.
type counting struct {
	n    atomic.Int32
	fail bool
	gate chan struct{}
}

func (c *counting) Make(ctx context.Context, src, dst string, width, quality int) error {
	c.n.Add(1)
	if c.gate != nil {
		<-c.gate
	}
	if c.fail {
		os.WriteFile(dst, []byte("half"), 0o644) // a partial file, then the failure
		return errors.New("broken")
	}
	return InProcess{}.Make(ctx, src, dst, width, quality)
}

func source(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.jpg")
	if err := os.WriteFile(path, picture(1600, 800), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCopies(t *testing.T) {
	ctx := context.Background()
	t.Run("two requests for a new copy make it once", func(t *testing.T) {
		maker := &counting{gate: make(chan struct{})}
		p := &Pipeline{Dir: t.TempDir(), Maker: maker}
		src := source(t)
		var wg sync.WaitGroup
		paths, errs := make([]string, 5), make([]error, 5)
		for i := range 5 {
			wg.Go(func() { paths[i], errs[i] = p.Copy(ctx, "k", src, 800, 80) })
		}
		for maker.n.Load() == 0 {
		}
		close(maker.gate)
		wg.Wait()
		for i := range 5 {
			if errs[i] != nil || paths[i] != p.Path("k", 800, 80) {
				t.Errorf("%d: %s %v", i, paths[i], errs[i])
			}
		}
		if n := maker.n.Load(); n != 1 {
			t.Errorf("made %d times", n)
		}
		if b, _ := os.ReadFile(paths[0]); decodeWebP(t, b).Bounds().Dx() != 800 {
			t.Error("not 800 wide")
		}
		p.Copy(ctx, "k", src, 800, 80)
		if n := maker.n.Load(); n != 1 || !p.Has("k", 800, 80) {
			t.Errorf("made again: %d", n)
		}
	})
	t.Run("a failed make leaves no file behind", func(t *testing.T) {
		p := &Pipeline{Dir: t.TempDir(), Maker: &counting{fail: true}}
		if _, err := p.Copy(ctx, "k", source(t), 800, 80); err == nil {
			t.Fatal("a failed make succeeded")
		}
		entries, _ := os.ReadDir(filepath.Join(p.Dir, "k"))
		if len(entries) != 0 {
			t.Errorf("left %d files", len(entries))
		}
	})
	t.Run("a placeholder is a small data URI, at its own size and quality", func(t *testing.T) {
		p := &Pipeline{Dir: t.TempDir()}
		if p.Placeholder("k") != "" || p.HasPlaceholder("k") {
			t.Fatal("a placeholder before it was made")
		}
		if err := p.MakePlaceholder(ctx, "k", source(t)); err != nil {
			t.Fatal(err)
		}
		uri := p.Placeholder("k")
		if !strings.HasPrefix(uri, "data:image/webp;base64,") || len(uri) > 1000 || !p.Has("k", 32, 50) {
			t.Errorf("%d bytes: %.40s", len(uri), uri)
		}
	})
	t.Run("forgetting a key removes its copies and placeholder", func(t *testing.T) {
		p := &Pipeline{Dir: t.TempDir()}
		src := source(t)
		p.Copy(ctx, "k", src, 800, 80)
		p.MakePlaceholder(ctx, "k", src)
		p.Placeholder("k")
		if err := p.Forget("k"); err != nil {
			t.Fatal(err)
		}
		if p.Has("k", 800, 80) || p.Placeholder("k") != "" {
			t.Error("still there")
		}
	})
}

// The test binary stands in for an app's: `resize` runs the child.
func TestMain(m *testing.M) {
	if IsChild(os.Args) {
		if os.Getenv("GOMEMLIMIT") != ChildMemory {
			os.Exit(3)
		}
		os.Exit((&Pipeline{Encoder: JPEG{}}).RunChild(os.Args))
	}
	os.Exit(m.Run())
}

func TestChild(t *testing.T) {
	t.Run("a copy is made by a child process, with the app's settings", func(t *testing.T) {
		p := &Pipeline{Dir: t.TempDir(), Encoder: JPEG{}, Maker: Child{Exe: os.Args[0]}}
		path, err := p.Copy(context.Background(), "k", source(t), 200, 80)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := os.ReadFile(path)
		if w, _, typ, err := p.Dimensions(out); err != nil || w != 200 || typ != "image/jpeg" {
			t.Errorf("%d %s %v", w, typ, err)
		}
		if _, err := p.Copy(context.Background(), "k", "/missing", 300, 80); err == nil {
			t.Error("a missing source succeeded")
		}
	})
}
