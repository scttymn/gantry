package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrossOriginCallback(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	called := 0
	h := func(w http.ResponseWriter, r *http.Request) error { called++; w.WriteHeader(204); return nil }
	rt.CrossOriginCallback("POST /apple/callback", h)
	rt.Handle("POST /other", h)
	handler := rt.Handler()
	for _, tc := range []struct {
		path   string
		status int
	}{{"/apple/callback", 204}, {"/other", 422}, {"/apple/callback/extra", 422}} {
		r := httptest.NewRequest("POST", "https://app.example"+tc.path, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://appleid.apple.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
	if called != 1 {
		t.Fatal(called)
	}
}
