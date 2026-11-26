package main

import (
	"maps"
	"os"
	"os/exec"
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

var targetedApp = map[string]string{
	"go.mod":                         "module example.com/shop\n\ngo 1.27\n",
	"README.md":                      "# shop\n",
	"compose.yml":                    "x-houston:\n  commands:\n    test: sh -c \"go vet ./... && go test -race -tags integration ./...\"\n",
	"cmd/shop/main.go":               "package main\n\nimport _ \"example.com/shop/app\"\n\nfunc main() {}\n",
	"app/routes.go":                  "package app\n\nimport (\n\t_ \"example.com/shop/app/home\"\n\t_ \"example.com/shop/app/posts\"\n)\n",
	"app/models/post.go":             "package models\n\ntype Post struct{}\n",
	"app/posts/controller.go":        "package posts\n\nimport _ \"example.com/shop/app/models\"\n",
	"app/posts/helper.go":            "package posts\n\nfunc Helper() int { return 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10 }\n",
	"app/posts/help.md":              "How to post.\n",
	"app/posts/testdata/fixture.txt": "a post\n",
	"app/posts/views/icon.svg":       "<svg/>\n",
	"app/home/controller.go":         "package home\n",
	"app/home/home_test.go":          "package home_test\n\nimport _ \"example.com/shop/app/lib\"\n",
	"app/lib/lib.go":                 "package lib\n",
	"assets/app.css":                 "body {}\n",
}

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "gantry")
	}
	for _, v := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "gantry@example.com")
	}
	root := t.TempDir()
	put(t, root, files)
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-qm", "base")
	runGit(t, root, "checkout", "-qb", "work")
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func put(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func onlyGitOnPath(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestAffectedPackages(t *testing.T) {
	appPkgs := []string{"./app", "./app/posts", "./cmd/shop"}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, root string)
		pkgs   []string
		all    string
	}{
		{"a committed change, and who imports it", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/models/post.go": "package models\n\ntype Post struct{ ID int }\n"})
			runGit(t, root, "commit", "-qam", "id")
		}, []string{"./app", "./app/models", "./app/posts", "./cmd/shop"}, ""},
		{"a change imported only by a test", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/lib/lib.go": "package lib\n\nvar X = 1\n"})
		}, []string{"./app/home", "./app/lib"}, ""},
		{"a test, which nothing imports", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/home/home_test.go": "package home_test\n\nimport _ \"example.com/shop/app/lib\"\n\nvar _ = 1\n"})
		}, []string{"./app/home"}, ""},
		{"test data, staged", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/posts/testdata/fixture.txt": "another post\n"})
			runGit(t, root, "add", "-A")
		}, appPkgs, ""},
		{"a new embedded asset, untracked", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/posts/views/logo.svg": "<svg/>\n"})
		}, appPkgs, ""},
		{"file names git would quote or split", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/posts/views/café.svg": "<svg/>\n", "app/posts/views/two\nlines.svg": "<svg/>\n"})
		}, appPkgs, ""},
		{"a Markdown file in a package's directory", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/posts/help.md": "How to post, again.\n"})
		}, appPkgs, ""},
		{"a rename counts where it left as well as where it went", func(t *testing.T, root string) {
			runGit(t, root, "mv", "app/posts/helper.go", "app/home/helper.go")
			runGit(t, root, "commit", "-qm", "move")
		}, []string{"./app", "./app/home", "./app/posts", "./cmd/shop"}, ""},
		{"only documentation", func(t *testing.T, root string) {
			put(t, root, map[string]string{"README.md": "# shop, again\n", "docs/notes.txt": "notes\n"})
		}, nil, ""},
		{"go.work", func(t *testing.T, root string) {
			put(t, root, map[string]string{"go.work": "go 1.27\n\nuse .\n"})
		}, nil, "go.work changed"},
		{"the schema", func(t *testing.T, root string) {
			put(t, root, map[string]string{"db/schema.sql": "create table posts (id integer);\n"})
		}, nil, "db/schema.sql changed"},
		{"a file in no package", func(t *testing.T, root string) {
			put(t, root, map[string]string{"assets/app.css": "body { margin: 0 }\n"})
		}, nil, "assets/app.css is in no package"},
		{"a package deleted whole", func(t *testing.T, root string) {
			runGit(t, root, "rm", "-q", "app/lib/lib.go")
		}, nil, "app/lib/lib.go is in no package"},
		{"a Go file whose imports can't be read", func(t *testing.T, root string) {
			put(t, root, map[string]string{"app/posts/broken.go": "package posts\n\nimport (\n"})
		}, nil, "reading the imports of app/posts/broken.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t, targetedApp)
			tc.change(t, root)
			onlyGitOnPath(t)
			got := affectedPackages(root, "")
			if !strings.HasPrefix(got.everythingBecause, tc.all) || (tc.all == "") != (got.everythingBecause == "") || !reflect.DeepEqual(got.pkgs, tc.pkgs) {
				t.Errorf("got %+v, want packages %q or everything because %q", got, tc.pkgs, tc.all)
			}
		})
	}
}

func TestAffectedPackagesWithARootPackage(t *testing.T) {
	files := maps.Clone(targetedApp)
	maps.Copy(files, map[string]string{
		"doc.go":                        "package shop\n",
		"tools/go.mod":                  "module example.com/tools\n",
		"tools/tools.go":                "package tools\n",
		"_scripts/seed.go":              "package scripts\n",
		"vendor/example.com/dep/dep.go": "package dep\n",
		".github/workflows/ci.yml":      "on: push\n",
	})
	for _, tc := range []struct {
		name, file string
		pkgs       []string
		all        string
	}{
		{"vendored code", "vendor/example.com/dep/dep.go", nil, "vendor/example.com/dep/dep.go is under vendor"},
		{"a nested module", "tools/tools.go", nil, "tools/tools.go is under tools"},
		{"an underscore directory", "_scripts/seed.go", nil, "_scripts/seed.go is under _scripts"},
		{"the in-module Dockerfile's ignore file", "Dockerfile.dockerignore", nil, "Dockerfile.dockerignore changed"},
		{"a test in test data counts for its package, not only its tests", "app/posts/testdata/helper_test.go", []string{"./app", "./app/posts", "./cmd/shop"}, ""},
		{"the workflows", ".github/workflows/ci.yml", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t, files)
			put(t, root, map[string]string{tc.file: "package changed\n\n// " + tc.name + "\n"})
			got := affectedPackages(root, "")
			if !strings.HasPrefix(got.everythingBecause, tc.all) || (tc.all == "") != (got.everythingBecause == "") || !reflect.DeepEqual(got.pkgs, tc.pkgs) {
				t.Errorf("got %+v, want packages %q or everything because %q", got, tc.pkgs, tc.all)
			}
		})
	}
}

func TestAffectedPackagesRunsEverythingWhenGitCantSay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
		base  string
		all   string
	}{
		{"no main or origin/main", func(t *testing.T) string {
			root := gitRepo(t, targetedApp)
			runGit(t, root, "branch", "-qm", "main", "master")
			put(t, root, map[string]string{"app/models/post.go": "package models\n\ntype Post struct{ ID int }\n"})
			runGit(t, root, "commit", "-qam", "id")
			return root
		}, "", "no origin/main or main to compare with"},
		{"a --base that isn't there", func(t *testing.T) string {
			return gitRepo(t, targetedApp)
		}, "origin/trunk", "--base origin/trunk isn't a commit here"},
		{"a base with no history in common", func(t *testing.T) string {
			root := gitRepo(t, targetedApp)
			runGit(t, root, "checkout", "-q", "--orphan", "unrelated")
			runGit(t, root, "commit", "-qm", "unrelated")
			runGit(t, root, "checkout", "-q", "work")
			return root
		}, "unrelated", "HEAD shares no history with unrelated"},
		{"not a git checkout", func(t *testing.T) string {
			root := t.TempDir()
			put(t, root, targetedApp)
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
			return root
		}, "", "git rev-parse --show-prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := affectedPackages(tc.setup(t), tc.base)
			if !strings.HasPrefix(got.everythingBecause, tc.all) {
				t.Errorf("got %+v, want everything because %q", got, tc.all)
			}
		})
	}
}

func TestAffectedPackagesOfAnAppInAModule(t *testing.T) {
	files := map[string]string{"go.mod": "module example.com/big\n\ngo 1.27\n", "README.md": "# big\n", "apps/lib/lib.go": "package lib\n"}
	for name, body := range targetedApp {
		if name != "go.mod" {
			files["apps/control/"+name] = strings.ReplaceAll(body, "example.com/shop", "example.com/big/apps/control")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, root string)
		pkgs   []string
		all    string
	}{
		{"committed and untracked changes in the app", func(t *testing.T, root string) {
			put(t, root, map[string]string{"apps/control/app/models/post.go": "package models\n\ntype Post struct{ ID int }\n"})
			runGit(t, root, "commit", "-qam", "id")
			put(t, root, map[string]string{"apps/control/app/lib/new.go": "package lib\n"})
		}, []string{"./app", "./app/home", "./app/lib", "./app/models", "./app/posts", "./cmd/shop"}, ""},
		{"documentation outside the app", func(t *testing.T, root string) {
			put(t, root, map[string]string{"README.md": "# big, again\n"})
		}, nil, ""},
		{"Markdown outside the app, which a package it imports could embed", func(t *testing.T, root string) {
			put(t, root, map[string]string{"apps/lib/help.md": "Help.\n"})
		}, nil, "apps/lib/help.md changed, outside the app"},
		{"code outside the app, in its module", func(t *testing.T, root string) {
			put(t, root, map[string]string{"apps/lib/lib.go": "package lib\n\nvar X = 1\n"})
		}, nil, "apps/lib/lib.go changed, outside the app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitRepo(t, files)
			tc.change(t, repo)
			got := affectedPackages(filepath.Join(repo, "apps", "control"), "")
			if !strings.HasPrefix(got.everythingBecause, tc.all) || (tc.all == "") != (got.everythingBecause == "") || !reflect.DeepEqual(got.pkgs, tc.pkgs) {
				t.Errorf("got %+v, want packages %q or everything because %q", got, tc.pkgs, tc.all)
			}
		})
	}
}

func TestTestCommand(t *testing.T) {
	changedIn := func(t *testing.T, files map[string]string) string {
		root := gitRepo(t, files)
		put(t, root, map[string]string{"app/models/post.go": "package models\n\ntype Post struct{ ID int }\n"})
		return root
	}
	changed := func(t *testing.T) string { return changedIn(t, targetedApp) }
	t.Run("without gantry's flags it is houston test, arguments and all", func(t *testing.T) {
		root := t.TempDir()
		for _, args := range [][]string{nil, {"-count=1"}, {"-count=1", "--help"}} {
			ran := steps(t, "")
			code, _, stderr := gantry(t, root, append([]string{"test"}, args...)...)
			if want := [][]string{append([]string{filepath.Base(root), "houston", "test"}, args...)}; code != 0 || !reflect.DeepEqual(*ran, want) {
				t.Errorf("test %q: exit %d %s, ran %q, want %q", args, code, stderr, *ran, want)
			}
		}
	})
	t.Run("--changed narrows the app's own test command", func(t *testing.T) {
		root := changed(t)
		steps(t, "")
		var compose string
		runIn = func(dir, name string, args ...string) error {
			if name == "houston" && len(args) == 3 && args[0] == "-f" {
				b, _ := os.ReadFile(filepath.Join(dir, args[1]))
				compose = string(b)
			}
			return nil
		}
		if code, _, stderr := gantry(t, root, "test", "--changed"); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if want := `go vet ./app ./app/models ./app/posts ./cmd/shop && go test -race -tags integration ./app ./app/models ./app/posts ./cmd/shop`; !strings.Contains(compose, want) {
			t.Errorf("the test stack runs\n%s\nwant %q, with the app's own flags", compose, want)
		}
	})
	t.Run("--changed runs a test command it can't narrow whole", func(t *testing.T) {
		files := maps.Clone(targetedApp)
		files["compose.yml"] = "x-houston:\n  commands:\n    test: sh -c \"go generate ./... && go test ./...\"\n"
		root := changedIn(t, files)
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "test", "--changed")
		if want := [][]string{{filepath.Base(root), "houston", "test"}}; code != 0 || !reflect.DeepEqual(*ran, want) || !strings.Contains(stderr, "runs whole") {
			t.Errorf("exit %d, ran %q, stderr %q; want %q", code, *ran, stderr, want)
		}
	})
	t.Run("--changed runs everything when it can't tell", func(t *testing.T) {
		root := changed(t)
		ran := steps(t, "")
		code, _, stderr := gantry(t, root, "test", "--changed", "--base", "origin/trunk")
		if want := [][]string{{filepath.Base(root), "houston", "test"}}; code != 0 || !reflect.DeepEqual(*ran, want) || !strings.Contains(stderr, "so every package runs") {
			t.Errorf("exit %d, ran %q, stderr %q; want %q", code, *ran, stderr, want)
		}
	})
	t.Run("--local --changed", func(t *testing.T) {
		root := changed(t)
		ran := steps(t, "")
		if code, _, stderr := gantry(t, root, "test", "--local", "--changed"); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		pkgs := []string{"./app", "./app/models", "./app/posts", "./cmd/shop"}
		want := [][]string{
			append([]string{filepath.Base(root), "env", "GANTRY_ENV=test", "go", "vet"}, pkgs...),
			append([]string{filepath.Base(root), "env", "GANTRY_ENV=test", "go", "test"}, pkgs...),
		}
		if !reflect.DeepEqual(*ran, want) {
			t.Errorf("ran %q, want %q", *ran, want)
		}
	})
	t.Run("--changed with only documentation runs nothing", func(t *testing.T) {
		root := gitRepo(t, targetedApp)
		put(t, root, map[string]string{"README.md": "# shop, again\n"})
		ran := steps(t, "")
		code, out, _ := gantry(t, root, "test", "--changed")
		if code != 0 || len(*ran) != 0 || !strings.Contains(out, "no Go packages affected") {
			t.Errorf("exit %d, ran %q, out %q", code, *ran, out)
		}
	})
}

func TestNarrowed(t *testing.T) {
	pkgs := []string{"./app", "./app/posts"}
	for command, want := range map[string]string{
		`sh -c "go vet ./... && go test ./..."`:      `sh -c "go vet ./app ./app/posts && go test ./app ./app/posts"`,
		`go test -race -count=1 ./...`:               `go test -race -count=1 ./app ./app/posts`,
		`sh -c 'go test ./... -run X'`:               `sh -c 'go test ./app ./app/posts -run X'`,
		`sh -c "go generate ./... && go test ./..."`: "",
		`sh -c "GOFLAGS=-race go test ./..."`:        "",
		`go test ../...`:                             "",
		`go test ./...x`:                             "",
		`make test`:                                  "",
		``:                                           "",
	} {
		got, ok := narrowed(command, pkgs)
		if got != want || ok != (want != "") {
			t.Errorf("narrowed(%q) = %q, %v; want %q", command, got, ok, want)
		}
	}
	if got, ok := narrowed("go test ./...", []string{"./app/$(rm -rf ~)"}); ok {
		t.Errorf("a package with shell in its name narrowed to %q", got)
	}
}
