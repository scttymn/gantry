package web

import (
	"io"
	"net/http"
	"time"
)

// minRate is the pace a transfer must keep to outlast the server's
// ReadTimeout or WriteTimeout: 16 KiB a second, slower than a phone on a
// poor connection.
const minRate = 16 << 10

// pace is when a transfer (a request's body, or an answer) is cut off. The
// server's timeouts bound a whole request from its start, and a 25 MB
// photo from a phone, or a large file to one, can outlast them while still
// moving. So it gets the timeout from its start, and a second more for each
// minRate bytes it has moved, but never more than the timeout from now: one
// that keeps up minRate goes on, and one that stops, or trickles in a byte
// at a time to hold the connection, is cut off as the timeout alone would.
type pace struct {
	start   time.Time
	timeout time.Duration // the server's; none when it's 0 or less
	moved   int64
}

func (p *pace) deadline(now time.Time) time.Time {
	// Whole seconds first: moved*time.Second overflows past 8.6 GiB.
	earned := p.start.Add(p.timeout + time.Duration(p.moved/minRate)*time.Second + time.Duration(p.moved%minRate)*time.Second/minRate)
	if idle := now.Add(p.timeout); idle.Before(earned) {
		return idle
	}
	return earned
}

// timeouts are the ReadTimeout and WriteTimeout of the server r came to.
func timeouts(r *http.Request) (read, write time.Duration) {
	if srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server); ok {
		return srv.ReadTimeout, srv.WriteTimeout
	}
	return 0, 0
}

// movingBody is a request's body, paced by the server's ReadTimeout. The
// answer has the WriteTimeout from when the body is cut off, so a slow
// upload can still be answered, and one cut off told why.
type movingBody struct {
	io.ReadCloser
	rc    *http.ResponseController
	read  pace
	write time.Duration
}

func (b *movingBody) Read(p []byte) (int, error) {
	by := time.Now()
	if b.read.timeout > 0 {
		by = b.read.deadline(by)
		b.rc.SetReadDeadline(by)
	}
	if b.write > 0 {
		b.rc.SetWriteDeadline(by.Add(b.write))
	}
	n, err := b.ReadCloser.Read(p)
	b.read.moved += int64(n)
	return n, err
}

// Moving is w for an answer that may take longer to send than the server's
// WriteTimeout allows: a large file to a slow client. Each write has the
// timeout from the answer's start and a second more for each 16 KiB sent,
// but never more than the timeout from then, so a client reading at least
// that fast isn't cut off, and one that stops or reads a trickle is.
// web.Download uses it. It's w itself when the server has no WriteTimeout.
func Moving(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	_, write := timeouts(r)
	if write <= 0 {
		return w
	}
	return &movingWriter{ResponseWriter: w, rc: http.NewResponseController(w), pace: pace{start: time.Now(), timeout: write}}
}

type movingWriter struct {
	http.ResponseWriter
	rc   *http.ResponseController
	pace pace
}

// Write paces p a piece at a time: io.Copy hands it a bytes.Reader's whole
// body in one write.
func (w *movingWriter) Write(p []byte) (n int, err error) {
	for len(p) > 0 && err == nil {
		w.rc.SetWriteDeadline(w.pace.deadline(time.Now()))
		var m int
		m, err = w.ResponseWriter.Write(p[:min(len(p), 32<<10)])
		n += m
		w.pace.moved += int64(m)
		p = p[m:]
	}
	return n, err
}

func (w *movingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
