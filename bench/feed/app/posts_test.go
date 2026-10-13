package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

// The feed is the 20 newest posts, the newest first (and of two made in one
// millisecond, the later id), each with its author's name and its likes
// counted; and /health asks the database.
func TestFeed(t *testing.T) {
	a := newApp(t)
	h := a.Handler()
	ctx := t.Context()
	for id := 1; id <= 3; id++ {
		if _, err := a.DB.Write.ExecContext(ctx, `INSERT INTO users (id, username) VALUES (?, ?)`, id, fmt.Sprint("user", id)); err != nil {
			t.Fatal(err)
		}
	}
	for id := 1; id <= 25; id++ {
		at := fmt.Sprintf("2025-06-01T12:00:%02d.000Z", id/2) // two posts to a second
		if _, err := a.DB.Write.ExecContext(ctx, `INSERT INTO posts (id, user_id, body, created_at) VALUES (?, ?, ?, ?)`, id, 1+id%3, fmt.Sprint("post ", id), at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Write.ExecContext(ctx, `INSERT INTO likes (user_id, post_id) VALUES (1, 25), (2, 25), (3, 24)`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/feed", nil))
	var feed struct {
		Posts []struct {
			ID        int    `json:"id"`
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
			Author    string `json:"author"`
			LikeCount int    `json:"like_count"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &feed); err != nil || w.Code != 200 || len(feed.Posts) != 20 {
		t.Fatalf("GET /feed = %d, %d posts, %v\n%s", w.Code, len(feed.Posts), err, w.Body)
	}
	for i, p := range feed.Posts {
		if want := 25 - i; p.ID != want || p.Body != fmt.Sprint("post ", want) || p.Author != fmt.Sprint("user", 1+want%3) {
			t.Errorf("post %d of the feed is %+v; want post %d", i, p, want)
		}
	}
	if feed.Posts[0].LikeCount != 2 || feed.Posts[1].LikeCount != 1 || feed.Posts[2].LikeCount != 0 || feed.Posts[0].CreatedAt != "2025-06-01T12:00:12.000Z" {
		t.Errorf("the newest three: %+v", feed.Posts[:3])
	}
	if !strings.HasPrefix(w.Body.String(), `{"posts":[{"id":25,"body":"post 25","created_at":"2025-06-01T12:00:12.000Z","author":"user2","like_count":2},`) {
		t.Errorf("the feed's JSON isn't the challenge's shape:\n%.200s", w.Body)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if got := strings.TrimSpace(w.Body.String()); w.Code != 200 || !strings.HasPrefix(got, `{"status":"ok","db":"ok","uptime_s":`) {
		t.Errorf("GET /health = %d %s", w.Code, got)
	}
	a.DB.Close()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if got := strings.TrimSpace(w.Body.String()); w.Code != 503 || !strings.HasPrefix(got, `{"status":"degraded","db":"unreachable","error":"`) {
		t.Errorf("GET /health with the database closed = %d %s", w.Code, got)
	}
}

// A token that isn't good yet (nbf) is refused as an expired one is.
func TestTokenNotYetGood(t *testing.T) {
	a := newApp(t)
	h := a.Handler()
	enc := base64.RawURLEncoding.EncodeToString
	sign := func(claims string) string {
		signed := enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(claims))
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write([]byte(signed))
		return signed + "." + enc(mac.Sum(nil))
	}
	now := time.Now().Unix()
	for claims, want := range map[string]int{
		fmt.Sprintf(`{"sub":"1","username":"ada","nbf":%d,"exp":%d}`, now+3600, now+7200): 401,
		fmt.Sprintf(`{"sub":"1","username":"ada","nbf":%d,"exp":%d}`, now-60, now+7200):   400, // good: on to the body
		fmt.Sprintf(`{"sub":"1","username":"ada","nbf":"soon","exp":%d}`, now+7200):       401,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/posts", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+sign(claims))
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d %s; want %d", claims, w.Code, strings.TrimSpace(w.Body.String()), want)
		}
	}
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
