package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scttymn/gantry/testkit"
	"github.com/scttymn/gantry/web"
)

func listedRoutes(t *testing.T, routes []web.Route, fail error) *[][]string {
	t.Helper()
	steps(t, "")
	var ran [][]string
	was := outputIn
	outputIn = func(dir, name string, args ...string) ([]byte, error) {
		ran = append(ran, append([]string{name}, args...))
		if fail != nil {
			return nil, fail
		}
		return json.Marshal(routes)
	}
	t.Cleanup(func() { outputIn = was })
	return &ran
}

func shopRoot(t *testing.T, features string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cmd", "shop"), 0o755)
	if features != "" {
		os.MkdirAll(filepath.Join(root, "test"), 0o755)
		if err := os.WriteFile(filepath.Join(root, "test", "features.yaml"), []byte(features), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readFeatures(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "test", "features.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var shopRoutes = []web.Route{
	{Method: "GET", Path: "/{$}"},
	{Method: "GET", Path: "/up"},
	{Method: "GET", Path: "/live"},
	{Method: "GET", Path: "/posts"},
	{Method: "POST", Path: "/posts"},
	{Method: "GET", Path: "/posts/{id}"},
	{Method: "GET", Path: "/news", Cached: true},
	{Method: "GET", Path: "/admin/{$}", Scope: "/admin", Filters: []string{"auth.(*Auth).Required"}},
	{Path: "/webhooks"},
	{Method: "GET", Path: "/beta", Constrained: true},
	{Method: "GET", Path: "api.example.com/v1/"},
	{Method: "GET", Path: "/assets/", Mounted: true},
}

func TestGenerateFeaturesCreatesTheMap(t *testing.T) {
	root := shopRoot(t, "")
	ran := listedRoutes(t, shopRoutes, nil)
	code, out, stderr := gantry(t, root, "g", "features", "--local")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := [][]string{{"env", "GANTRY_ENV=development", "go", "run", "./cmd/shop", "routes", "--json"}}; !reflect.DeepEqual(*ran, want) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
	path := filepath.Join(root, "test", "features.yaml")
	got := testkit.Features(t, path)
	visit := func(p string) []testkit.Visit { return []testkit.Visit{{Path: p}} }
	want := []testkit.Feature{
		{Name: "home", Routes: []string{"GET /{$}"}, Role: testkit.Public, Visit: []testkit.Visit{{Path: "/", Selector: "h1"}}},
		{Name: "health check", Routes: []string{"GET /up"}, Role: testkit.Public, Visit: visit("/up")},
		{Name: "live updates", Routes: []string{"GET /live"}, Role: testkit.Public},
		{Name: "GET /posts", Routes: []string{"GET /posts"}, Role: testkit.Public, Visit: visit("/posts")},
		{Name: "POST /posts", Routes: []string{"POST /posts"}, Role: testkit.Public},
		{Name: "GET /posts/{id}", Routes: []string{"GET /posts/{id}"}, Role: testkit.Public},
		{Name: "GET /news", Routes: []string{"GET /news"}, Role: testkit.Public, Cacheable: true, Visit: visit("/news")},
		{Name: "GET /admin/{$}", Routes: []string{"GET /admin/{$}"}, Role: testkit.Public, Visit: visit("/admin/")},
		{Name: "/webhooks", Routes: []string{"/webhooks"}, Role: testkit.Public, Visit: visit("/webhooks")},
		{Name: "GET /beta", Routes: []string{"GET /beta"}, Role: testkit.Public},
		{Name: "GET api.example.com/v1/", Routes: []string{"GET api.example.com/v1/"}, Role: testkit.Public},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("test/features.yaml:\n got %+v\nwant %+v", got, want)
	}
	for _, line := range []string{"wrote test/features.yaml", "GET /live: live updates, as a new app's map has it", "GET /posts: visited at /posts", "POST /posts\n", "GET /admin/{$}: visited at /admin/, filtered by auth.(*Auth).Required", "set each one's role"} {
		if !strings.Contains(out, line) {
			t.Errorf("no %q in\n%s", line, out)
		}
	}
	if strings.Contains(readFeatures(t, root), "/assets/") {
		t.Errorf("a Mounted route is in the map:\n%s", readFeatures(t, root))
	}
}

func TestGenerateFeaturesCachedHome(t *testing.T) {
	root := shopRoot(t, "")
	listedRoutes(t, []web.Route{{Method: "GET", Path: "/{$}", Cached: true}}, nil)
	if code, _, stderr := gantry(t, root, "g", "features", "--local"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := testkit.Features(t, filepath.Join(root, "test", "features.yaml"))
	want := []testkit.Feature{{Name: "GET /{$}", Routes: []string{"GET /{$}"}, Role: testkit.Public, Cacheable: true, Visit: []testkit.Visit{{Path: "/"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a Cached home page:\n got %+v\nwant %+v", got, want)
	}
}

func TestGenerateFeaturesCoversWhatItAdds(t *testing.T) {
	root := shopRoot(t, "")
	listedRoutes(t, []web.Route{
		{Method: "GET", Path: "/docs/"},
		{Method: "GET", Path: "/docs/{slug}"},
		{Path: "/webhooks"},
		{Method: "POST", Path: "/webhooks"},
		{Method: "GET", Path: "/beta", Constrained: true},
		{Method: "GET", Path: "/beta", Constrained: true},
	}, nil)
	if code, _, stderr := gantry(t, root, "g", "features", "--local"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var names []string
	for _, f := range testkit.Features(t, filepath.Join(root, "test", "features.yaml")) {
		names = append(names, f.Name)
	}
	if want := []string{"GET /docs/", "/webhooks", "GET /beta"}; !reflect.DeepEqual(names, want) {
		t.Errorf("features %q, want %q: a route a feature it added covers was added again", names, want)
	}
}

func TestGenerateFeaturesStarters(t *testing.T) {
	root := shopRoot(t, "")
	listedRoutes(t, []web.Route{
		{Method: "GET", Path: "/{$}", Filters: []string{"auth.(*Auth).Required"}},
		{Method: "GET", Path: "/up", Constrained: true},
	}, nil)
	code, out, stderr := gantry(t, root, "g", "features", "--local")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := "GET /{$}: home, as a new app's map has it, filtered by auth.(*Auth).Required\n"; !strings.Contains(out, want) {
		t.Errorf("no %q in\n%s", want, out)
	}
	got := testkit.Features(t, filepath.Join(root, "test", "features.yaml"))
	want := []testkit.Feature{
		{Name: "home", Routes: []string{"GET /{$}"}, Role: testkit.Public, Visit: []testkit.Visit{{Path: "/", Selector: "h1"}}},
		{Name: "GET /up", Routes: []string{"GET /up"}, Role: testkit.Public},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a constrained /up takes a new app's feature, with its visit:\n got %+v\nwant %+v", got, want)
	}
}

func TestGenerateFeaturesWithoutHouston(t *testing.T) {
	root := shopRoot(t, "")
	ran := listedRoutes(t, shopRoutes, nil)
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	code, _, stderr := gantry(t, root, "g", "features")
	if code != 1 || !strings.Contains(stderr, "Houston, which isn't installed") || strings.Contains(stderr, "didn't list its routes") || len(*ran) != 0 {
		t.Errorf("exit %d, ran %q: %s", code, *ran, stderr)
	}
}

func TestGenerateFeaturesKeepsTheMap(t *testing.T) {
	const mapped = `features:
    - name: home
      routes: ["GET /{$}", GET /up]
      role: public
      visit:
        - path: /
          selector: h1

    - name: posts
      routes:
        - /posts/
        - GET /posts
      role: signed-in
    - name: GET /news
      routes: [/elsewhere]
      role: public
`
	root := shopRoot(t, mapped)
	ran := listedRoutes(t, shopRoutes, nil)
	code, out, stderr := gantry(t, root, "g", "features")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := [][]string{{"houston", "exec", "go", "run", "./cmd/shop", "routes", "--json"}}; !reflect.DeepEqual(*ran, want) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
	got := readFeatures(t, root)
	added, kept := strings.CutPrefix(got, mapped)
	if !kept {
		t.Fatalf("the map's features were changed:\n%s", got)
	}
	if !strings.HasPrefix(added, "    - name: live updates\n      routes:\n") {
		t.Errorf("not added at the map's own indent:\n%s", added)
	}
	var names []string
	for _, f := range testkit.Features(t, filepath.Join(root, "test", "features.yaml")) {
		names = append(names, f.Name)
	}
	want := []string{"home", "posts", "GET /news", "live updates", "POST /posts", "GET /news 2", "GET /admin/{$}", "/webhooks", "GET /beta", "GET api.example.com/v1/"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("features %q, want %q", names, want)
	}
	if strings.Contains(out, "GET /posts/{id}") {
		t.Errorf("a route in a feature (/posts/) was added:\n%s", out)
	}

	code, out, stderr = gantry(t, root, "g", "features")
	if code != 0 || readFeatures(t, root) != got || !strings.Contains(out, "every route is in a feature") {
		t.Errorf("again: exit %d, out %q, %s, the map:\n%s", code, out, stderr, readFeatures(t, root))
	}
}

func TestGenerateFeaturesRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, features string
		fail           error
		want           string
	}{
		{"an app that can't list its routes", "", errExit(2), "the app didn't list its routes (exit 2)"},
		{"a map that isn't YAML", "features: [\n", nil, "test/features.yaml: yaml:"},
		{"a map whose features aren't a list it can add to", "features: []\n", nil, "add them by hand"},
		{"a map with something after its features", "features:\n  - name: home\n    routes: [/x]\n    role: public\nother: 1\n", nil, "add them by hand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := shopRoot(t, tc.features)
			listedRoutes(t, shopRoutes, tc.fail)
			code, _, stderr := gantry(t, root, "g", "features", "--local")
			if code != 1 || !strings.Contains(stderr, tc.want) {
				t.Errorf("exit %d, want 1 and %q: %s", code, tc.want, stderr)
			}
			b, err := os.ReadFile(filepath.Join(root, "test", "features.yaml"))
			if tc.features == "" && err == nil || tc.features != "" && string(b) != tc.features {
				t.Errorf("the map was written: %q", b)
			}
		})
	}
	listedRoutes(t, shopRoutes, nil)
	if code, _, stderr := gantry(t, t.TempDir(), "g", "features"); code != 1 || !strings.Contains(stderr, "run gantry at the app's root") || strings.Contains(stderr, "said why") {
		t.Errorf("outside an app: exit %d: %s", code, stderr)
	}
}

func TestGenerateFeaturesPassesVerify(t *testing.T) {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Cache = &web.PageCache{}
	page := func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := w.Write([]byte("<!DOCTYPE html><h1>Shop</h1>"))
		return err
	}
	rt.Handle("GET /{$}", page)
	rt.Handle("POST /posts", page)
	rt.Handle("GET /posts/{id}", page)
	rt.Cached("GET /news", page)
	rt.Scope("/admin", nil, func(s *web.Scope) { s.Handle("GET /{$}", page) })
	rt.Mount("GET /assets/", http.NotFoundHandler())
	root := shopRoot(t, "features:\n  - name: home\n    routes: [\"GET /{$}\"]\n    role: public\n")
	listedRoutes(t, rt.Routes(), nil)
	if code, _, stderr := gantry(t, root, "g", "features", "--local"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	testkit.Verify(t, rt.Handler(), rt.Routes(), filepath.Join(root, "test", "features.yaml"), testkit.Roles{})
}
