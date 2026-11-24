package featuremap

import "testing"

func FuzzCovers(f *testing.F) {
	f.Add("GET /posts", "GET", "/posts")
	f.Add("/posts/", "DELETE", "/posts/{id}")
	f.Add("GET /{$}", "GET", "/{$}")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, route, method, path string) {
		got := Covers(route, method, path)
		if got && route == "" && path != "" {
			t.Fatalf("empty route covered non-empty path %q", path)
		}
	})
}
