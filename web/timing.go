package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Timing is opt-in development instrumentation. Server-Timing reports time
// until the handler commits its headers, NOT network latency or browser paint.
// Completion is logged separately so streaming responses need not be buffered.
// Enable only in development; paths can contain application identifiers.
func Timing(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		tw := &timingWriter{ResponseWriter: w, started: started}
		defer func() {
			if log != nil {
				log.Debug("request timing", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
			}
		}()
		next.ServeHTTP(tw, r)
		if !tw.wrote {
			tw.WriteHeader(http.StatusOK)
		}
	})
}

type timingWriter struct {
	http.ResponseWriter
	started time.Time
	wrote   bool
}

func (w *timingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *timingWriter) WriteHeader(status int) {
	if !w.wrote && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.wrote = true
		w.Header().Add("Server-Timing", fmt.Sprintf("gantry;dur=%.3f;desc=\"Time to response headers\"", float64(time.Since(w.started))/float64(time.Millisecond)))
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *timingWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *timingWriter) FlushError() error {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
