package web

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Download streams r to the client as a file to save, named filename
// (Rails' send_data): nothing is held in memory, and the name is made safe,
// its folders dropped and anything but plain ASCII kept for browsers that
// read the RFC 6266 form. A failure partway cuts the connection (the
// router's rule), so a download is never mistaken for a whole one. Files on
// disk go through http.ServeContent instead, which also resumes them.
func Download(w http.ResponseWriter, req *http.Request, filename, contentType string, r io.Reader) error {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition(filename))
	_, err := io.Copy(w, r)
	return err
}

// disposition is the Content-Disposition for filename: its last path
// element, with an ASCII name for every browser and the UTF-8 one when
// they differ.
func disposition(filename string) string {
	if i := strings.LastIndexAny(filename, `/\`); i >= 0 {
		filename = filename[i+1:]
	}
	if filename == "" || filename == "." || filename == ".." {
		filename = "download"
	}
	var ascii strings.Builder
	for _, c := range filename {
		switch {
		case c < 0x20 || c > 0x7e || c == '"' || c == '\\':
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(c)
		}
	}
	d := `attachment; filename="` + ascii.String() + `"`
	if ascii.String() != filename {
		d += "; filename*=UTF-8''" + attrValue(filename)
	}
	return d
}

// attrValue is s percent-encoded as RFC 5987 asks: all but letters, digits
// and !#$&+-.^_`|~.
func attrValue(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
