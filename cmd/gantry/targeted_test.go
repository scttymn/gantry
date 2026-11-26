package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVerifyFeature(t *testing.T) {
	const fmap = `features:
  - name: home
    routes: ["GET /{$}"]
    role: public
  - name: posts
    routes: [/posts, /posts/]
    role: public
`
	root := shopRoot(t, fmap)
	base := filepath.Base(root)
	os.MkdirAll(filepath.Join(root, "app"), 0o755)
	os.WriteFile(filepath.Join(root, "app", "features_test.go"), []byte("package app_test\n\nimport \"testing\"\n\nfunc TestFeatures(t *testing.T) {}\n"), 0o644)

	for _, tc := range []struct{ flag, want string }{
		{"posts", "posts"},
		{" posts, home,posts ", "posts,home"},
	} {
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "verify", "--local", "--feature", tc.flag)
		if code != 0 || stderr != "" {
			t.Fatalf("--feature %q: exit %d stderr=%q", tc.flag, code, stderr)
		}
		if want := [][]string{{base, "env", "GANTRY_ENV=test", "GANTRY_VERIFY_FEATURES=" + tc.want, "go", "test", "-count=1", "-v", "-run", "^TestFeatures$", "./app/"}}; !reflect.DeepEqual(*ran, want) {
			t.Errorf("--feature %q: ran %q, want %q", tc.flag, *ran, want)
		}
	}
	for _, empty := range []string{"", " , "} {
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "verify", "--local", "--feature", empty)
		if code != 2 || !strings.Contains(stderr, "--feature names no feature") || len(*ran) != 0 {
			t.Errorf("--feature %q: exit %d, ran %q, stderr %q; want a usage error that runs nothing, not every feature", empty, code, *ran, stderr)
		}
	}
}

func TestTargetedAppTestCommand(t *testing.T) {
	root := t.TempDir()
	base := filepath.Base(root)

	t.Run("no args delegates to houston test", func(t *testing.T) {
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "test")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr=%q", code, stderr)
		}
		if want := [][]string{{base, "houston", "test"}}; !reflect.DeepEqual(*ran, want) {
			t.Errorf("ran %q, want %q", *ran, want)
		}
	})

	t.Run("--local with package and flags", func(t *testing.T) {
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "test", "--local", "./app/posts", "-run", "TestFeatures")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr=%q", code, stderr)
		}
		if want := [][]string{{base, "env", "GANTRY_ENV=test", "go", "test", "./app/posts", "-run", "TestFeatures"}}; !reflect.DeepEqual(*ran, want) {
			t.Errorf("ran %q, want %q", *ran, want)
		}
	})

	t.Run("--changed computes reverse-importer closure", func(t *testing.T) {
		origGit, origList := gitOutputIn, goListIn
		t.Cleanup(func() { gitOutputIn, goListIn = origGit, origList })
		gitOutputIn = func(dir string, args ...string) ([]byte, error) {
			if len(args) >= 1 && args[0] == "rev-parse" {
				return []byte("true\n"), nil
			}
			if len(args) == 3 && args[0] == "diff" && strings.Contains(args[2], "...") {
				return []byte("app/models/post.go\n"), nil
			}
			return nil, nil
		}
		absRoot, _ := filepath.Abs(root)
		goListIn = func(dir string) ([]byte, error) {
			pkgs := []goListPackage{
				{ImportPath: "example.com/shop/app/models", Dir: filepath.Join(absRoot, "app/models")},
				{ImportPath: "example.com/shop/app/posts", Dir: filepath.Join(absRoot, "app/posts"), Imports: []string{"example.com/shop/app/models"}},
				{ImportPath: "example.com/shop/app/home", Dir: filepath.Join(absRoot, "app/home")},
			}
			var buf strings.Builder
			for _, p := range pkgs {
				b, _ := json.Marshal(p)
				buf.Write(b)
				buf.WriteByte('\n')
			}
			return []byte(buf.String()), nil
		}
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "test", "--local", "--changed")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr=%q", code, stderr)
		}
		want := [][]string{
			{base, "env", "GANTRY_ENV=test", "go", "vet", "./app/models", "./app/posts"},
			{base, "env", "GANTRY_ENV=test", "go", "test", "./app/models", "./app/posts"},
		}
		if !reflect.DeepEqual(*ran, want) {
			t.Errorf("ran %q, want %q", *ran, want)
		}
	})
}
