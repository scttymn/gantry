package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/internal/testenv"
	"github.com/scttymn/gantry/storage"
)

func shopApp(t *testing.T, engine string) string {
	t.Helper()
	root := t.TempDir()
	if code, _, stderr := gantry(t, root, "new", "shop", "--db", engine, "--module", "example.com/shop", "--skip-houston"); code != 0 {
		t.Fatalf("gantry new: %s", stderr)
	}
	return filepath.Join(root, "shop")
}

func TestAPITokensRules(t *testing.T) {
	dir := shopApp(t, "sqlite")
	for _, prefix := range []string{"Shop_", "shop", "_shop_", "shop-_", "averyveryverylongone_"} {
		if code, _, stderr := gantry(t, dir, "g", "api-tokens", "--prefix", prefix); code != 1 || !strings.Contains(stderr, "--prefix") {
			t.Errorf("prefix %q: exit %d %s", prefix, code, stderr)
		}
	}
	gantry(t, dir, "g", "api-tokens")
	if code, _, stderr := gantry(t, dir, "g", "api-tokens"); code != 1 || !strings.Contains(stderr, "the recipe was applied") {
		t.Errorf("twice: exit %d %s", code, stderr)
	}
	if code, _, stderr := gantry(t, t.TempDir(), "g", "api-tokens"); code != 1 || !strings.Contains(stderr, "run gantry at the app's root") {
		t.Errorf("outside an app: exit %d %s", code, stderr)
	}
}

func TestAPITokensInModule(t *testing.T) {
	root := big(t, "")
	gantry(t, filepath.Join(root, "apps"), "new", "control", "--in-module", "--skip-houston")
	dir := filepath.Join(root, "apps", "control")
	if code, _, stderr := gantry(t, dir, "g", "api-tokens"); code != 0 {
		t.Fatal(stderr)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "app", "apitokens", "apitokens.go"))
	if !strings.Contains(string(b), `"example.com/big/apps/control/app/models"`) {
		t.Errorf("imports:\n%s", b)
	}
}

func TestAuthRules(t *testing.T) {
	dir := shopApp(t, "sqlite")
	if code, _, stderr := gantry(t, dir, "g", "auth", "extra"); code != 1 || !strings.Contains(stderr, "no arguments") {
		t.Errorf("an argument: %d %s", code, stderr)
	}
	gantry(t, dir, "g", "auth")
	if code, _, stderr := gantry(t, dir, "g", "auth"); code != 1 || !strings.Contains(stderr, "the recipe was applied") {
		t.Errorf("twice: %d %s", code, stderr)
	}
}

func TestStorageMigrates(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			url := "sqlite://" + filepath.Join(t.TempDir(), "app.sqlite3")
			if engine == "postgres" {
				url = testenv.Postgres(t)
			}
			dir := shopApp(t, engine)
			if code, _, stderr := gantry(t, dir, "g", "storage"); code != 0 {
				t.Fatal(stderr)
			}
			ctx := context.Background()
			d, err := db.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if engine == "postgres" {
				d.Write.Exec(`DROP TABLE IF EXISTS active_storage_attachments, active_storage_blobs, gantry_storage_test_migrations`)
			}
			migrations, _ := filepath.Glob(filepath.Join(dir, "db", "migrations", "*_create_active_storage_tables.sql"))
			if len(migrations) != 1 {
				t.Fatalf("migrations: %v", migrations)
			}
			if err := d.Migrate(ctx, os.DirFS(filepath.Dir(migrations[0])), "gantry_storage_test_migrations"); err != nil {
				t.Fatal(err)
			}
			st := &storage.Storage{DB: d, Root: t.TempDir(), NoAVIF: true, Log: slog.New(slog.DiscardHandler)}
			defer st.Wait()
			ref := storage.Ref{RecordType: "Clip", RecordID: 1, Name: "thumbnail"}
			b, err := st.Attach(ctx, ref, storage.File{Filename: "a.txt", Data: []byte("hello")})
			if err != nil {
				t.Fatal(err)
			}
			if found, ok, err := st.Find(ctx, ref); err != nil || !ok || found.Key != b.Key {
				t.Fatalf("Find: %+v %v %v", found, ok, err)
			}
			if all, err := st.All(ctx, "Clip", "thumbnail"); err != nil || all[1].Key != b.Key {
				t.Fatalf("All: %+v %v", all, err)
			}
			if _, ok, err := st.Detach(ctx, ref); err != nil || !ok {
				t.Fatalf("Detach: %v", err)
			}
			if loose, err := st.Unattached(ctx); err != nil || len(loose) != 1 {
				t.Fatalf("Unattached: %+v %v", loose, err)
			}
			if _, err := st.AttachBlob(ctx, ref, b.Key); err != nil {
				t.Fatal(err)
			}
			st.Detach(ctx, ref)
			if err := st.PurgeBlob(ctx, b.Key); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Attach(ctx, ref, storage.File{Filename: "b.txt", Data: []byte("again")}); err != nil {
				t.Fatal(err)
			}
			if err := st.Purge(ctx, ref); err != nil {
				t.Fatal(err)
			}
			if loose, _ := st.Unattached(ctx); len(loose) != 0 {
				t.Errorf("left: %+v", loose)
			}
		})
	}
}

func TestStorageRules(t *testing.T) {
	dir := shopApp(t, "sqlite")
	if code, _, stderr := gantry(t, dir, "g", "storage", "extra"); code != 1 || !strings.Contains(stderr, "no arguments") {
		t.Errorf("an argument: %d %s", code, stderr)
	}
	gantry(t, dir, "g", "storage")
	if code, _, stderr := gantry(t, dir, "g", "storage"); code != 1 || !strings.Contains(stderr, "the recipe was applied") {
		t.Errorf("twice: %d %s", code, stderr)
	}
}

func TestEmailCodeRecipe(t *testing.T) {
	for _, args := range [][]string{{"g", "auth"}, {"g", "auth", "--email-code"}, {"g", "auth", "--email-code", "--signup"}} {
		dir := shopApp(t, "sqlite")
		if code, _, stderr := gantry(t, dir, args...); code != 0 {
			t.Fatal(stderr)
		}
		if code, _, stderr := gantry(t, dir, args...); code != 1 || !strings.Contains(stderr, "recipe was applied") {
			t.Errorf("%q reapplied: %s", args, stderr)
		}
	}
	dir := shopApp(t, "sqlite")
	if code, _, stderr := gantry(t, dir, "g", "auth", "--signup"); code != 1 || !strings.Contains(stderr, "requires --email-code") {
		t.Fatalf("signup without email codes: %s", stderr)
	}
}

func TestSocialRecipe(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, mode := range []string{"password", "email-code"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				dir := shopApp(t, engine)
				at(t, "2026-10-12T12:00:00Z")
				args := []string{"g", "auth"}
				if mode == "email-code" {
					args = append(args, "--email-code", "--signup")
				}
				if code, _, stderr := gantry(t, dir, args...); code != 0 {
					t.Fatal(stderr)
				}
				original, _ := os.ReadFile(filepath.Join(dir, "app", "auth", "auth.go"))
				if code, out, stderr := gantry(t, dir, "g", "auth", "--social"); code != 0 || !strings.Contains(out, "ConfigureSocial") {
					t.Fatal(out, stderr)
				}
				after, _ := os.ReadFile(filepath.Join(dir, "app", "auth", "auth.go"))
				if string(original) != string(after) {
					t.Fatal("social recipe overwrote existing auth")
				}
				if code, _, _ := gantry(t, dir, "g", "auth", "--social"); code != 1 {
					t.Fatal("reapplied social recipe")
				}
				files, _ := filepath.Glob(filepath.Join(dir, "db", "migrations", "*_create_auth_identities.sql"))
				if len(files) != 1 {
					t.Fatal("no migration")
				}
				stamp := "20261012120001"
				if mode == "email-code" {
					stamp = "20261012120002"
				}
				if !strings.HasPrefix(filepath.Base(files[0]), stamp) {
					t.Fatal("migration does not follow auth", files)
				}
			})
		}
	}
	dir := shopApp(t, "sqlite")
	if code, _, _ := gantry(t, dir, "g", "auth", "--social"); code != 1 {
		t.Fatal("social recipe needs existing auth")
	}
}

func TestRecipeViewsKeepPageStatus(t *testing.T) {
	old := `package auth
var v = func(w http.ResponseWriter, r *http.Request, p gantryauth.Page) error {
	return web.Render(w, r, http.StatusOK, page(p))
}`
	if got := hardCodedStatuses(t, "old.go", []byte(old)); len(got) != 1 {
		t.Fatalf("missed a view's hard-coded status: %v", got)
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "golden", "auth-*", "app", "auth", "*.go.golden"))
	if len(files) == 0 {
		t.Fatal("no auth recipe goldens")
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range hardCodedStatuses(t, file, src) {
			t.Errorf("%s: a view renders a gantryauth.Page with a status of its own; use pageStatus(p)", at)
		}
	}
}

func hardCodedStatuses(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	sel := func(e ast.Expr, pkg string) string {
		if s, ok := e.(*ast.SelectorExpr); ok {
			if x, ok := s.X.(*ast.Ident); ok && x.Name == pkg {
				return s.Sel.Name
			}
		}
		return ""
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		var typ *ast.FuncType
		var body *ast.BlockStmt
		switch fn := n.(type) {
		case *ast.FuncLit:
			typ, body = fn.Type, fn.Body
		case *ast.FuncDecl:
			typ, body = fn.Type, fn.Body
		default:
			return true
		}
		takesPage := false
		for _, p := range typ.Params.List {
			takesPage = takesPage || sel(p.Type, "gantryauth") == "Page"
		}
		if !takesPage || body == nil {
			return true
		}
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || sel(call.Fun, "web") != "Render" || len(call.Args) < 3 {
				return true
			}
			if _, literal := call.Args[2].(*ast.BasicLit); literal || strings.HasPrefix(sel(call.Args[2], "http"), "Status") {
				found = append(found, fset.Position(call.Pos()).String())
			}
			return true
		})
		return true
	})
	return found
}
