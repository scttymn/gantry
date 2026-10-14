package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// token is a bearer token for user 1, signed as the app expects.
func token(secret string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	signed := enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(fmt.Sprintf(`{"sub":"1","username":"ada","exp":%d}`, exp.Unix())))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return signed + "." + enc(mac.Sum(nil))
}

func TestPosts(t *testing.T) {
	a := newApp(t)
	h := a.Handler()
	if _, err := a.DB.Write.ExecContext(t.Context(), `INSERT INTO users (id, username) VALUES (1, 'ada')`); err != nil {
		t.Fatal(err)
	}
	do := func(method, path, tok, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		h.ServeHTTP(w, r)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	good := token("test-secret", time.Now().Add(time.Hour))
	for _, c := range []struct {
		method, path, tok, body string
		status                  int
		want                    string
	}{
		{"GET", "/feed", "", "", 200, `{"posts":[]}`},
		{"GET", "/posts/abc", "", "", 400, `{"error":"invalid post id"}`},
		{"GET", "/posts/7", "", "", 404, `{"error":"post not found"}`},
		{"POST", "/posts", "", `{"body":"x"}`, 401, `{"error":"missing bearer token"}`},
		{"POST", "/posts", token("another", time.Now().Add(time.Hour)), `{"body":"x"}`, 401, `{"error":"invalid or expired token"}`},
		{"POST", "/posts", token("test-secret", time.Now().Add(-time.Hour)), `{"body":"x"}`, 401, `{"error":"invalid or expired token"}`},
		{"POST", "/posts/abc/like", "", "", 401, `{"error":"missing bearer token"}`},
		{"POST", "/posts/abc/like", good, "", 400, `{"error":"invalid post id"}`},
		{"POST", "/posts", good, `{bad`, 400, `{"error":"malformed JSON body"}`},
		{"POST", "/posts", good, `{"body":"  "}`, 400, `{"error":"body is required"}`},
		{"POST", "/posts", good, `{"body":"` + strings.Repeat("a", 501) + `"}`, 400, `{"error":"body must be at most 500 characters"}`},
		{"POST", "/posts/7/like", good, "", 404, `{"error":"post not found"}`},
	} {
		if status, got := do(c.method, c.path, c.tok, c.body); status != c.status || got != c.want {
			t.Errorf("%s %s = %d %s; want %d %s", c.method, c.path, status, got, c.status, c.want)
		}
	}
	if status, got := do("POST", "/posts", good, `{"body":"  hello  "}`); status != 201 || !strings.Contains(got, `"body":"hello"`) || !strings.Contains(got, `"author":"ada","like_count":0}`) {
		t.Fatalf("create = %d %s", status, got)
	}
	if status, got := do("POST", "/posts/1/like", good, ""); status != 201 || got != `{"liked":true,"already_liked":false,"post_id":1}` {
		t.Errorf("first like = %d %s", status, got)
	}
	if status, got := do("POST", "/posts/1/like", good, ""); status != 200 || got != `{"liked":true,"already_liked":true,"post_id":1}` {
		t.Errorf("second like = %d %s", status, got)
	}
	if status, got := do("GET", "/posts/1", "", ""); status != http.StatusOK || !strings.HasSuffix(got, `"author":"ada","like_count":1}}`) {
		t.Errorf("read back = %d %s", status, got)
	}
}
