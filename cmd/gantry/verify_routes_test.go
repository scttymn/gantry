package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/scttymn/gantry/testkit"
	"github.com/scttymn/gantry/web"
)

func TestVerifyJSONLocal(t *testing.T) {
	root := shopRoot(t, "features: []\n")
	os.MkdirAll(filepath.Join(root, "app"), 0o755)
	os.WriteFile(filepath.Join(root, "app", "features_test.go"), []byte("package app_test\n\nimport \"testing\"\n\nfunc TestFeatures(t *testing.T) {}\n"), 0o644)

	t.Run("passing", func(t *testing.T) {
		steps(t, "")
		wantReport := testkit.VerifyReport{
			Passed:         true,
			Routes:         2,
			UnmappedRoutes: []string{},
			Features: []testkit.FeatureResult{{
				Name: "home", Role: testkit.Public, Passed: true,
				Routes: []string{"GET /{$}"}, Visits: []testkit.VisitResult{{Path: "/", Role: testkit.Public, Status: 200, Allowed: true, Passed: true}},
				Cache: []testkit.CacheResult{}, Leaks: []testkit.LeakResult{}, Errors: []string{},
			}},
		}
		payload, _ := json.Marshal(wantReport)
		var ran []string
		runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
			ran = append([]string{name}, args...)
			return []byte("=== RUN TestFeatures\n" + testkit.VerifyJSONPrefix + string(payload) + "\n--- PASS: TestFeatures\n"), nil
		}
		code, out, stderr := gantry(t, root, "verify", "--json", "--local")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d, stderr %q, out %q", code, stderr, out)
		}
		if want := []string{"env", "GANTRY_ENV=test", "GANTRY_VERIFY_JSON=1", "go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}; !reflect.DeepEqual(ran, want) {
			t.Errorf("ran %q, want %q", ran, want)
		}
		var got testkit.VerifyReport
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if !reflect.DeepEqual(got, wantReport) {
			t.Errorf("got %+v, want %+v", got, wantReport)
		}
	})

	t.Run("failing exits nonzero after printing JSON", func(t *testing.T) {
		steps(t, "")
		failing := testkit.VerifyReport{
			Passed:         false,
			Failures:       1,
			Routes:         1,
			UnmappedRoutes: []string{"GET /orphan"},
			Features:       []testkit.FeatureResult{},
		}
		payload, _ := json.Marshal(failing)
		runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
			return []byte(testkit.VerifyJSONPrefix + string(payload) + "\nFAIL\n"), errExit(1)
		}
		code, out, _ := gantry(t, root, "verify", "--json", "--local")
		if code != 1 {
			t.Fatalf("exit %d, want 1", code)
		}
		var got testkit.VerifyReport
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if got.Passed || got.Failures != 1 || !reflect.DeepEqual(got.UnmappedRoutes, []string{"GET /orphan"}) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("no report prints raw output and fails", func(t *testing.T) {
		steps(t, "")
		runQuietlyIn = func(dir, name string, args ...string) ([]byte, error) {
			return []byte("compile error in app/features_test.go\n"), errExit(2)
		}
		code, out, stderr := gantry(t, root, "verify", "--json", "--local")
		if code != 2 || out != "" || !strings.Contains(stderr, "compile error") {
			t.Errorf("exit %d, out %q, stderr %q", code, out, stderr)
		}
	})
}

func TestVerifyJSONOnTheTestStack(t *testing.T) {
	dir := shopApp(t, "sqlite")
	steps(t, "")
	var copied map[string]any
	payload, _ := json.Marshal(testkit.VerifyReport{Passed: true, UnmappedRoutes: []string{}, Features: []testkit.FeatureResult{}})
	runQuietlyIn = func(root, name string, args ...string) ([]byte, error) {
		if len(args) == 3 && args[0] == "-f" {
			b, err := os.ReadFile(filepath.Join(root, args[1]))
			if err != nil {
				return nil, err
			}
			yaml.Unmarshal(b, &copied)
		}
		return []byte("houston banner\n" + testkit.VerifyJSONPrefix + string(payload) + "\n"), nil
	}
	code, out, stderr := gantry(t, dir, "verify", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	x, _ := copied["x-houston"].(map[string]any)
	commands, _ := x["commands"].(map[string]any)
	if got, want := commands["test"], featuresJSONTestCommand; got != want {
		t.Errorf("commands.test = %q, want %q", got, want)
	}
	var got testkit.VerifyReport
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.Passed {
		t.Errorf("out %q, err %v", out, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".gantry-verify-*")); len(left) > 0 {
		t.Errorf("left copy: %q", left)
	}
}

func TestRoutesDiagram(t *testing.T) {
	const fmap = `features:
  - name: news
    routes: [GET /news]
    role: public
    cacheable: true
  - name: home
    routes: ["GET /{$}", GET /up]
    role: public
  - name: account
    routes: [/account/]
    role: signed-in
  - name: admin
    routes: [/admin/]
    role: admin
`
	routeList := []web.Route{
		{Method: "GET", Path: "/{$}"},
		{Method: "GET", Path: "/up"},
		{Method: "GET", Path: "/news", Cached: true},
		{Method: "GET", Path: "/account/{$}", Scope: "/account", Filters: []string{"auth.(*Auth).Required"}},
		{Method: "GET", Path: "/admin/posts", Scope: "/admin", Filters: []string{"auth.(*Auth).Required", "app.requireAdmin"}},
		{Method: "POST", Path: "/orphan"},
		{Method: "GET", Path: "/beta", Constrained: true},
		{Method: "GET", Path: "/assets/", Mounted: true},
	}

	root := shopRoot(t, fmap)
	ran := listedRoutes(t, routeList, nil)
	code, out, stderr := gantry(t, root, "routes", "--diagram", "--local")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := [][]string{{"env", "GANTRY_ENV=development", "go", "run", "./cmd/shop", "routes", "--json"}}; !reflect.DeepEqual(*ran, want) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
	const want = `flowchart LR
  role_public["public"]
  role_signed_in["signed-in"]
  role_admin["admin"]
  role_public --> role_signed_in
  role_signed_in --> role_admin
  subgraph sg_public["public"]
    f2["home\nGET /up\nGET /{$}"]
    f3["news [cached]\nGET /news [cached]"]
  end
  subgraph sg_signed_in["signed-in"]
    f0["account\nGET /account/{$} (auth.(*Auth).Required)"]
  end
  subgraph sg_admin["admin"]
    f1["admin\nGET /admin/posts (auth.(*Auth).Required, app.requireAdmin)"]
  end
  role_public --> f2
  role_public --> f3
  role_signed_in --> f0
  role_admin --> f1
  subgraph sg_mounted["mounted"]
    m0["GET /assets/"]
  end
  subgraph sg_unmapped["unmapped"]
    u0["GET /beta [constrained]"]
    u1["POST /orphan"]
  end
  classDef unmapped fill:#fee,stroke:#c00,color:#900
  class u0,u1 unmapped
`
	if out != want {
		t.Errorf("diagram mismatch:\ngot:\n%s\nwant:\n%s", out, want)
	}

	t.Run("without features.yaml highlights all routes as unmapped", func(t *testing.T) {
		noMap := shopRoot(t, "")
		listedRoutes(t, []web.Route{{Method: "GET", Path: "/{$}"}, {Method: "GET", Path: "/assets/", Mounted: true}}, nil)
		code, out, stderr := gantry(t, noMap, "routes", "--diagram")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if !strings.Contains(out, "subgraph sg_unmapped") || !strings.Contains(out, "GET /{$}") || !strings.Contains(out, "subgraph sg_mounted") {
			t.Errorf("unexpected diagram:\n%s", out)
		}
	})

	t.Run("diagram and json together is a usage error", func(t *testing.T) {
		code, _, stderr := gantry(t, root, "routes", "--diagram", "--json")
		if code != 2 || !strings.Contains(stderr, "usage: gantry routes [--diagram] [--json] [--local]") {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})
}

func TestVerifyBrowserCLI(t *testing.T) {
	dir := shopApp(t, "sqlite")

	t.Run("local passes browser env vars", func(t *testing.T) {
		got := steps(t, "")
		code, _, stderr := gantry(t, dir, "verify", "--browser", "--artifacts", "proof", "--local")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		wantDir, _ := filepath.Abs(filepath.Join(dir, "proof"))
		want := []string{"env", "GANTRY_ENV=test", "GANTRY_VERIFY_BROWSER=1", "GANTRY_VERIFY_ARTIFACTS=" + wantDir, "go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}
		if len(*got) != 1 || !reflect.DeepEqual((*got)[0][1:], want) {
			t.Errorf("ran %q, want %q", *got, want)
		}
	})

	t.Run("test stack writes browser env and CDP URL into compose copy", func(t *testing.T) {
		t.Setenv("GANTRY_CDP_URL", "http://host.docker.internal:9222")
		steps(t, "")
		var copied map[string]any
		payload, _ := json.Marshal(testkit.VerifyReport{Passed: true, UnmappedRoutes: []string{}, Features: []testkit.FeatureResult{}})
		runQuietlyIn = func(root, name string, args ...string) ([]byte, error) {
			if len(args) == 3 && args[0] == "-f" {
				b, err := os.ReadFile(filepath.Join(root, args[1]))
				if err != nil {
					return nil, err
				}
				yaml.Unmarshal(b, &copied)
			}
			return []byte(testkit.VerifyJSONPrefix + string(payload) + "\n"), nil
		}
		code, _, stderr := gantry(t, dir, "verify", "--browser", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		x, _ := copied["x-houston"].(map[string]any)
		commands, _ := x["commands"].(map[string]any)
		cmd, _ := commands["test"].(string)
		for _, want := range []string{"GANTRY_VERIFY_JSON=1", "GANTRY_VERIFY_BROWSER=1", "GANTRY_VERIFY_ARTIFACTS='.gantry/verify-artifacts'", "GANTRY_CDP_URL='http://host.docker.internal:9222'", featuresTestCommand} {
			if !strings.Contains(cmd, want) {
				t.Errorf("commands.test = %q, missing %q", cmd, want)
			}
		}
	})

	t.Run("artifacts without browser is a usage error", func(t *testing.T) {
		code, _, stderr := gantry(t, dir, "verify", "--artifacts", "proof")
		if code != 2 || !strings.Contains(stderr, "usage: gantry verify [--artifacts DIR] [--browser] [--feature NAME[,NAME...]] [--json] [--local]") {
			t.Errorf("exit %d: %s", code, stderr)
		}
	})
}
