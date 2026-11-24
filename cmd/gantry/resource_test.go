package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourcePaths(t *testing.T) {
	for _, tc := range []struct {
		name, controller, base, route string
	}{
		{"posts", "app/posts/controller.go", `path     = "/posts"`, `rt.Resources("/posts", posts.Controller{`},
		{"admin/posts", "app/admin/posts/controller.go", `path     = "/admin/posts"`, `s.Resources("/posts", posts.Controller{`},
		{"editor/blog_posts", "app/editor/blogposts/controller.go", `path     = "/editor/blog_posts"`, `rt.Scope("/editor", web.Pipeline{}, func(s *web.Scope) {`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := app(t)
			out := generate(t, root, tc.name, "title:string")
			code, err := os.ReadFile(filepath.Join(root, tc.controller))
			if err != nil || !strings.Contains(string(code), tc.base) {
				t.Errorf("%s: %v\n%s", tc.controller, err, code)
			}
			for _, want := range []string{tc.route, `"example.com/gym/` + filepath.Dir(tc.controller) + `"`, `"example.com/gym/app/shared/resource"`} {
				if !strings.Contains(out, want) {
					t.Errorf("no %q in\n%s", want, out)
				}
			}
		})
	}
	t.Run("the app's own folders aren't a resource's, and nothing is written", func(t *testing.T) {
		for _, name := range []string{"models", "shared", "mod_els", "models/posts", "shared/posts", "Admin/posts", "/posts", "admin/blog/posts"} {
			root := app(t)
			code, _, stderr := gantry(t, root, "g", "resource", name, "title:string")
			if code == 0 {
				t.Errorf("%s: made", name)
			}
			if strings.HasPrefix(name, "models") || strings.HasPrefix(name, "shared") || name == "mod_els" {
				if !strings.Contains(stderr, "the app's own") {
					t.Errorf("%s: %s", name, stderr)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "app")); err == nil {
				t.Errorf("%s: wrote files", name)
			}
		}
	})
}

func TestResourceInNamespaceWithItsOwnCode(t *testing.T) {
	const note = "app/admin has code of its own"
	root := app(t)
	out := generate(t, root, "admin/events", "name:string")
	if strings.Contains(out, note) {
		t.Errorf("a namespace of generated resources only:\n%s", out)
	}
	out = generate(t, root, "admin/posts", "title:string")
	if strings.Contains(out, note) {
		t.Errorf("a namespace of generated resources only:\n%s", out)
	}
	os.WriteFile(filepath.Join(root, "app", "admin", "layout.go"), []byte("package admin\n"), 0o644)
	out = generate(t, root, "admin/staff", "name:string")
	for _, want := range []string{note, "app/shared/resource", "app/admin/staff/index.templ and form.templ"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if out := generate(t, root, "pages", "title:string"); strings.Contains(out, "code of its own") {
		t.Errorf("a top-level resource:\n%s", out)
	}
}

func TestResourceHelpersKept(t *testing.T) {
	root := app(t)
	out := generate(t, root, "posts", "title:string")
	helpers := []string{"app/shared/resource/resource.go", "app/shared/resource/pages.templ"}
	for _, h := range helpers {
		if !strings.Contains(out, "wrote "+h) {
			t.Errorf("no wrote %s in\n%s", h, out)
		}
	}
	changed := filepath.Join(root, helpers[0])
	if err := os.WriteFile(changed, []byte("package resource // the app's now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = generate(t, root, "admin/events", "name:string")
	for _, h := range helpers {
		if !strings.Contains(out, "kept "+h) {
			t.Errorf("no kept %s in\n%s", h, out)
		}
	}
	if b, _ := os.ReadFile(changed); string(b) != "package resource // the app's now\n" {
		t.Errorf("written over:\n%s", b)
	}
}
