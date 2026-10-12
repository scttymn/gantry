package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTiming(t *testing.T) {
	for _, mode := range []string{"write", "empty", "flush", "status"} {
		t.Run(mode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h := Timing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Add("Server-Timing", "db;dur=1")
				switch mode {
				case "write":
					w.Write([]byte("hello"))
				case "flush":
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Fatal(err)
					}
					w.Write([]byte("stream"))
				case "status":
					w.WriteHeader(422)
				}
			}), nil)
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			values := rec.Result().Header.Values("Server-Timing")
			if len(values) != 2 || values[0] != "db;dur=1" || !strings.HasPrefix(values[1], "gantry;dur=") {
				t.Fatal(values)
			}
			if mode == "status" && rec.Code != 422 {
				t.Fatal(rec.Code)
			}
			if mode == "flush" && !rec.Flushed {
				t.Fatal("stream was not flushed")
			}
		})
	}
}
