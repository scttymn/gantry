package web

import (
	"io"
	"net/http"
	"time"
)

// timeouts are the ReadTimeout and WriteTimeout of the server r came to.
// They bound a whole request from its start, and a 25 MB photo from a
// phone, or a large file to one, can outlast them while still moving: a
// transfer's reads and writes push them out to that long from then
// instead (nginx's client_body_timeout and send_timeout), so it's cut off
// only once it stops. A server with no timeouts gets none.
func timeouts(r *http.Request) (read, write time.Duration) {
	if srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server); ok {
		return srv.ReadTimeout, srv.WriteTimeout
	}
	return 0, 0
}

// movingBody is a request's body, with the read timeout from each read. The
// answer gets the write timeout from the last, so a slow upload can still
// be answered.
type movingBody struct {
	io.ReadCloser
	rc          *http.ResponseController
	read, write time.Duration
}

func (b movingBody) Read(p []byte) (int, error) {
	now := time.Now()
	if b.read > 0 {
		b.rc.SetReadDeadline(now.Add(b.read))
	}
	if b.write > 0 {
		b.rc.SetWriteDeadline(now.Add(b.write))
	}
	return b.ReadCloser.Read(p)
}

// movingWriter is a response, with the write timeout from each write.
type movingWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	write time.Duration
}

func moving(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	_, write := timeouts(r)
	if write == 0 {
		return w
	}
	return movingWriter{w, http.NewResponseController(w), write}
}

func (w movingWriter) Write(p []byte) (int, error) {
	w.rc.SetWriteDeadline(time.Now().Add(w.write))
	return w.ResponseWriter.Write(p)
}

func (w movingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
