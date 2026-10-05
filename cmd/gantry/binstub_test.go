package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"
)

// releaseServer serves the GitHub release layout bin/gantry downloads:
// <base>/<version>/<asset> and <base>/<version>/SHA256SUMS.
type releaseServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	hits  map[string]int
	files map[string][]byte
	pause time.Duration
}

func newReleaseServer(t *testing.T) *releaseServer {
	t.Helper()
	rs := &releaseServer{hits: map[string]int{}, files: map[string][]byte{}}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rs.pause > 0 && !strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			time.Sleep(rs.pause)
		}
		rs.mu.Lock()
		rs.hits[r.URL.Path]++
		body, ok := rs.files[r.URL.Path]
		rs.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *releaseServer) hitCount() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, c := range rs.hits {
		n += c
	}
	return n
}

// put stores a release asset and a SHA256SUMS line for it. sums, when set,
// replaces the real digest (a bad checksum, or a lookalike filename).
func (rs *releaseServer) put(repo, version, asset string, body []byte, sums string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	path := "/" + repo + "/" + version + "/" + asset
	rs.files[path] = body
	if sums == "" {
		sum := sha256.Sum256(body)
		sums = hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	}
	sumsPath := "/" + repo + "/" + version + "/SHA256SUMS"
	rs.files[sumsPath] = append(rs.files[sumsPath], []byte(sums)...)
}

func (rs *releaseServer) bases() (gantryBase, houstonBase string) {
	return rs.srv.URL + "/gantry", rs.srv.URL + "/houston"
}

func platform(t *testing.T) (osName, arch string) {
	t.Helper()
	out, err := exec.Command("uname", "-sm").Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.ToLower(string(out)))
	if len(fields) != 2 {
		t.Fatalf("uname -sm: %q", out)
	}
	osName = fields[0]
	switch fields[1] {
	case "x86_64", "amd64":
		arch = "amd64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		t.Fatalf("unsupported uname arch %q", fields[1])
	}
	return osName, arch
}

func cliScript(name, line string) []byte {
	return []byte("#!/bin/sh\necho " + name + "\necho args:\"$*\"\necho houston:$(command -v houston)\nhouston\n" + line)
}

func writeMod(t *testing.T, dir, version, replace string) {
	t.Helper()
	body := "module example.com/shop\n\ngo 1.27.1\n\nrequire github.com/scttymn/gantry " + version + "\n"
	if replace != "" {
		body += "\nreplace github.com/scttymn/gantry => " + replace + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeWork(t *testing.T, dir, use string) {
	t.Helper()
	body := "go 1.27.1\n\nuse (\n\t.\n\t" + use + "\n)\n"
	if err := os.WriteFile(filepath.Join(dir, "go.work"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installBinstub(t *testing.T, dir string) string {
	t.Helper()
	raw, err := templates.ReadFile("templates/new/bin/gantry.tmpl")
	if err != nil {
		t.Fatalf("bin/gantry template: %v", err)
	}
	tpl, err := template.New("gantry").Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, NewApp{Houston: houstonRelease}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bin", "gantry")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func stubEnv(gantryBase, houstonBase string, extra ...string) []string {
	skip := map[string]bool{
		"GANTRY_BIN": true, "HOUSTON_BIN": true, "GANTRY_OS": true, "GANTRY_ARCH": true,
		"GANTRY_RELEASE_BASE": true, "HOUSTON_RELEASE_BASE": true,
	}
	var env []string
	for _, e := range os.Environ() {
		if key, _, ok := strings.Cut(e, "="); ok && skip[key] {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "GANTRY_RELEASE_BASE="+gantryBase, "HOUSTON_RELEASE_BASE="+houstonBase)
	return append(env, extra...)
}

func runStub(t *testing.T, dir string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(dir, "bin", "gantry"), args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return code, string(out)
}

func appWithCLIs(t *testing.T, version string) (dir string, rs *releaseServer, env []string) {
	t.Helper()
	osName, arch := platform(t)
	dir = t.TempDir()
	writeMod(t, dir, version, "")
	installBinstub(t, dir)
	rs = newReleaseServer(t)
	rs.put("gantry", version, "gantry-"+osName+"-"+arch, cliScript("gantry-ok", ""), "")
	rs.put("houston", houstonRelease, "houston-"+osName+"-"+arch, []byte("#!/bin/sh\necho houston-ok\n"), "")
	gBase, hBase := rs.bases()
	return dir, rs, stubEnv(gBase, hBase)
}

func TestBinstubRunsPinnedCLIs(t *testing.T) {
	dir, _, env := appWithCLIs(t, "v0.11.3")
	code, out := runStub(t, dir, env, "dev", "--as", "login")
	if code != 0 || !strings.Contains(out, "gantry-ok") || !strings.Contains(out, "args:dev --as login") || !strings.Contains(out, "houston-ok") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, filepath.Join(dir, ".gantry", "bin", "houston")) {
		t.Errorf("houston resolved outside .gantry/bin:\n%s", out)
	}
}

func TestBinstubSkipsDownloadWhenCached(t *testing.T) {
	dir, rs, env := appWithCLIs(t, "v0.11.3")
	if code, out := runStub(t, dir, env, "dev"); code != 0 {
		t.Fatal(out)
	}
	first := rs.hitCount()
	if first == 0 {
		t.Fatal("first run downloaded nothing")
	}
	if code, out := runStub(t, dir, env, "dev"); code != 0 {
		t.Fatal(out)
	}
	if rs.hitCount() != first {
		t.Errorf("second run did %d more requests", rs.hitCount()-first)
	}
}

func TestBinstubRejectsBadChecksum(t *testing.T) {
	osName, arch := platform(t)
	asset := "gantry-" + osName + "-" + arch
	for _, tc := range []struct {
		name string
		sums string
		want string
	}{
		{"wrong digest", "0000000000000000000000000000000000000000000000000000000000000000  " + asset + "\n", "failed its checksum"},
		{"lookalike name", "0000000000000000000000000000000000000000000000000000000000000000  " + asset + ".bak\n", "has no " + asset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMod(t, dir, "v0.11.3", "")
			installBinstub(t, dir)
			rs := newReleaseServer(t)
			rs.put("gantry", "v0.11.3", asset, cliScript("gantry-ok", ""), tc.sums)
			rs.put("houston", houstonRelease, "houston-"+osName+"-"+arch, []byte("#!/bin/sh\necho houston-ok\n"), "")
			gBase, hBase := rs.bases()
			code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
			if code == 0 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if _, err := os.Stat(filepath.Join(dir, ".gantry", "bin", "gantry-v0.11.3-"+osName+"-"+arch)); err == nil {
				t.Error("bad download was installed")
			}
		})
	}
}

func TestBinstubKeepsGoodBinaryWhenRefreshFails(t *testing.T) {
	osName, arch := platform(t)
	dir, rs, env := appWithCLIs(t, "v0.11.3")
	if code, out := runStub(t, dir, env, "dev"); code != 0 {
		t.Fatal(out)
	}
	writeMod(t, dir, "v0.12.0", "")
	asset := "gantry-" + osName + "-" + arch
	rs.put("gantry", "v0.12.0", asset, cliScript("gantry-new", ""), "0000000000000000000000000000000000000000000000000000000000000000  "+asset+"\n")
	code, out := runStub(t, dir, env, "dev")
	if code == 0 || !strings.Contains(out, "failed its checksum") || strings.Contains(out, "gantry-ok") || strings.Contains(out, "gantry-new") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gantry", "bin", "gantry-v0.12.0-"+osName+"-"+arch)); err == nil {
		t.Error("failed upgrade left its binary")
	}
	link, err := os.Readlink(filepath.Join(dir, ".gantry", "bin", "gantry"))
	if err != nil || link != "gantry-v0.11.3-"+osName+"-"+arch {
		t.Errorf("symlink is %q (%v)", link, err)
	}
	oldCmd := exec.Command(filepath.Join(dir, ".gantry", "bin", "gantry-v0.11.3-"+osName+"-"+arch))
	oldCmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, ".gantry", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	old, err := oldCmd.CombinedOutput()
	if err != nil || !strings.Contains(string(old), "gantry-ok") {
		t.Errorf("old binary: %v\n%s", err, old)
	}
}

func TestBinstubRejectsMissingAsset(t *testing.T) {
	osName, arch := platform(t)
	dir := t.TempDir()
	writeMod(t, dir, "v0.11.3", "")
	installBinstub(t, dir)
	rs := newReleaseServer(t)
	gBase, hBase := rs.bases()
	code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
	if code == 0 || !strings.Contains(out, "v0.11.3") || !strings.Contains(out, osName+"/"+arch) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gantry", "bin", "gantry-v0.11.3-"+osName+"-"+arch)); err == nil {
		t.Error("404 left a binary")
	}
}

func TestBinstubRejectsBadPins(t *testing.T) {
	osName, _ := platform(t)
	rs := newReleaseServer(t)
	gBase, hBase := rs.bases()
	t.Run("pseudo-version", func(t *testing.T) {
		dir := t.TempDir()
		writeMod(t, dir, "v0.0.0-20260101120000-abcdef123456", "")
		installBinstub(t, dir)
		code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
		if code == 0 || !strings.Contains(out, "isn't a release tag") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("no require", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/shop\n\ngo 1.27.1\n"), 0o644)
		installBinstub(t, dir)
		code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
		if code == 0 || !strings.Contains(out, "doesn't require github.com/scttymn/gantry") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("no go.mod", func(t *testing.T) {
		dir := t.TempDir()
		installBinstub(t, dir)
		code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
		if code == 0 || !strings.Contains(out, "no go.mod") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("bad arch", func(t *testing.T) {
		dir := t.TempDir()
		writeMod(t, dir, "v0.11.3", "")
		installBinstub(t, dir)
		code, out := runStub(t, dir, stubEnv(gBase, hBase, "GANTRY_OS="+osName, "GANTRY_ARCH=sparc"), "dev")
		if code == 0 || !strings.Contains(out, "isn't a supported architecture") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	if rs.hitCount() != 0 {
		t.Errorf("rejected pins still downloaded: %d requests", rs.hitCount())
	}
}

func TestBinstubUsesParentModule(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root, "v0.11.3", "")
	dir := filepath.Join(root, "apps", "shop")
	os.MkdirAll(dir, 0o755)
	installBinstub(t, dir)
	osName, arch := platform(t)
	rs := newReleaseServer(t)
	rs.put("gantry", "v0.11.3", "gantry-"+osName+"-"+arch, cliScript("gantry-ok", ""), "")
	rs.put("houston", houstonRelease, "houston-"+osName+"-"+arch, []byte("#!/bin/sh\necho houston-ok\n"), "")
	gBase, hBase := rs.bases()
	code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
	if code != 0 || !strings.Contains(out, "gantry-ok") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		t.Error("the app dir grew its own go.mod")
	}
}

func TestBinstubHonorsBinOverrides(t *testing.T) {
	dir := t.TempDir()
	writeMod(t, dir, "v0.11.3", "")
	installBinstub(t, dir)
	bin := filepath.Join(dir, "override")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "gantry"), cliScript("override-gantry", ""), 0o755)
	os.WriteFile(filepath.Join(bin, "houston"), []byte("#!/bin/sh\necho override-houston\n"), 0o755)
	rs := newReleaseServer(t)
	gBase, hBase := rs.bases()
	env := stubEnv(gBase, hBase,
		"GANTRY_BIN="+filepath.Join(bin, "gantry"),
		"HOUSTON_BIN="+filepath.Join(bin, "houston"))
	code, out := runStub(t, dir, env, "dev")
	if code != 0 || !strings.Contains(out, "override-gantry") || !strings.Contains(out, "override-houston") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if rs.hitCount() != 0 {
		t.Errorf("overrides still downloaded: %d requests", rs.hitCount())
	}
}

func checkout(t *testing.T, app, rel string) string {
	t.Helper()
	dir := filepath.Join(app, rel)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/scttymn/gantry\n\ngo 1.27.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBinstubRefusesLocalCheckout(t *testing.T) {
	rs := newReleaseServer(t)
	gBase, hBase := rs.bases()
	for _, tc := range []struct {
		name string
		wire func(t *testing.T, app string)
	}{
		{"replace", func(t *testing.T, app string) {
			checkout(t, app, "src")
			writeMod(t, app, "v0.11.3", "./src")
		}},
		{"go.work", func(t *testing.T, app string) {
			checkout(t, app, "src")
			writeMod(t, app, "v0.11.3", "")
			writeWork(t, app, "./src")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.wire(t, dir)
			installBinstub(t, dir)
			code, out := runStub(t, dir, stubEnv(gBase, hBase), "dev")
			if code == 0 || !strings.Contains(out, "checkout") || !strings.Contains(out, "bin/gantry") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if _, err := os.Stat(filepath.Join(dir, ".gantry")); err == nil {
				t.Error("refused checkout wrote .gantry")
			}
		})
	}
	if rs.hitCount() != 0 {
		t.Errorf("checkout refusal downloaded: %d requests", rs.hitCount())
	}
}

func TestBinstubUsesCheckoutCLI(t *testing.T) {
	osName, arch := platform(t)
	for _, tc := range []struct {
		name string
		wire func(t *testing.T, app, src string)
	}{
		{"replace", func(t *testing.T, app, src string) { writeMod(t, app, "v0.11.3", src) }},
		{"go.work", func(t *testing.T, app, src string) {
			writeMod(t, app, "v0.11.3", "")
			writeWork(t, app, src)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := checkout(t, dir, "src")
			os.MkdirAll(filepath.Join(src, "bin"), 0o755)
			os.WriteFile(filepath.Join(src, "bin", "gantry"), cliScript("checkout-gantry", ""), 0o755)
			tc.wire(t, dir, "./src")
			installBinstub(t, dir)
			houston := filepath.Join(dir, "houston")
			os.WriteFile(houston, []byte("#!/bin/sh\necho houston-ok\n"), 0o755)
			rs := newReleaseServer(t)
			gBase, hBase := rs.bases()
			code, out := runStub(t, dir, stubEnv(gBase, hBase, "HOUSTON_BIN="+houston), "g", "migration", "create_posts")
			if code != 0 || !strings.Contains(out, "checkout-gantry") || !strings.Contains(out, "args:g migration create_posts") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if rs.hitCount() != 0 {
				t.Errorf("checkout CLI still downloaded (%s/%s): %d", osName, arch, rs.hitCount())
			}
		})
	}
}

func TestBinstubConcurrentInstall(t *testing.T) {
	dir, rs, env := appWithCLIs(t, "v0.11.3")
	rs.pause = 200 * time.Millisecond
	var wg sync.WaitGroup
	codes := make([]int, 2)
	outs := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], outs[i] = runStub(t, dir, env, "dev")
		}(i)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if codes[i] != 0 || !strings.Contains(outs[i], "gantry-ok") {
			t.Errorf("process %d: exit %d\n%s", i, codes[i], outs[i])
		}
	}
}

func TestNewWritesExecutableBinstub(t *testing.T) {
	root := t.TempDir()
	code, out, stderr := gantry(t, root, "new", "shop", "--skip-houston")
	if code != 0 {
		t.Fatal(stderr)
	}
	path := filepath.Join(root, "shop", "bin", "gantry")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("mode %s, want executable", info.Mode())
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "houston_version=\""+houstonRelease+"\"") {
		t.Errorf("Houston pin missing from\n%s", body)
	}
	if !strings.Contains(out, "bin/gantry dev") {
		t.Errorf("output: %s", out)
	}
	ignored, _ := os.ReadFile(filepath.Join(root, "shop", ".gitignore"))
	if !strings.Contains(string(ignored), ".gantry/\n") {
		t.Errorf(".gitignore:\n%s", ignored)
	}
	raw, err := os.ReadFile(filepath.Join(root, "shop", ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), ".gantry") {
		t.Errorf(".dockerignore doesn't exclude .gantry:\n%s", raw)
	}
}

func TestReleasePublishesBinstubAssets(t *testing.T) {
	docker, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(templates, "templates/new/bin/gantry.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range [][]byte{docker, script} {
		for _, needle := range []string{"darwin", "linux", "amd64", "arm64", "gantry-$os-$arch"} {
			if !strings.Contains(string(text), needle) {
				t.Errorf("missing %q in release contract", needle)
			}
		}
	}
	for _, needle := range []string{"SHA256SUMS", "gantry-*", "gh release create"} {
		if !strings.Contains(string(workflow), needle) {
			t.Errorf("workflow missing %q", needle)
		}
	}
	if strings.Contains(string(script), "{{") && !strings.Contains(string(script), "{{.Houston}}") {
		t.Error("script template has a go action other than the Houston pin")
	}
}
