package web

import (
	"net/http/httptest"
	"testing"
)

func TestBearerToken(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer hou_abc123":   "hou_abc123",
		"bearer hou_abc123":   "hou_abc123", // the scheme's case doesn't matter
		"BEARER  hou_abc123 ": "hou_abc123",
		"Bearer ":             "",
		"Bearer":              "",
		"Basic dXNlcjpwYXNz":  "",
		"hou_abc123":          "",
		"":                    "",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		got, ok := BearerToken(r)
		if got != want || ok != (want != "") {
			t.Errorf("%q = %q, %v; want %q", header, got, ok, want)
		}
	}
}
