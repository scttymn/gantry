package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"
	"time"
)

// releaseServer serves the GitHub release layout bin/gantry downloads, over
// https: <base>/<version>/<asset>, <base>/<version>/SHA256SUMS and, for
// gantry, <base>/<version>/provenance.sigstore.json. Its env runs bin/gantry
// against it, trusting its certificate, with a fake gh (fakeGH) on PATH.
type releaseServer struct {
	srv   *httptest.Server
	dir   string
	mu    sync.Mutex
	hits  map[string]int
	files map[string][]byte
	moved map[string]string
	pause atomic.Int64 // a time.Duration, set with the server already serving
}

func newReleaseServer(t *testing.T) *releaseServer {
	t.Helper()
	rs := &releaseServer{hits: map[string]int{}, files: map[string][]byte{}, moved: map[string]string{}, dir: t.TempDir()}
	rs.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pause := time.Duration(rs.pause.Load()); pause > 0 && !strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			time.Sleep(pause)
		}
		rs.mu.Lock()
		rs.hits[r.URL.Path]++
		body, ok := rs.files[r.URL.Path]
		to, moved := rs.moved[r.URL.Path]
		rs.mu.Unlock()
		if moved {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(rs.srv.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rs.srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(rs.dir, "ca.pem"), cert, 0o644); err != nil {
		t.Fatal(err)
	}
	rs.gh(t, true)
	return rs
}

// gh installs the fake gh, one with --source-ref or one older.
func (rs *releaseServer) gh(t *testing.T, sourceRef bool) {
	t.Helper()
	os.MkdirAll(filepath.Join(rs.dir, "bin"), 0o755)
	if err := os.WriteFile(filepath.Join(rs.dir, "bin", "gh"), fakeGH(filepath.Join(rs.dir, "gh-calls"), sourceRef), 0o755); err != nil {
		t.Fatal(err)
	}
}

// ghCalls is the arguments of each gh attestation verify bin/gantry ran.
func (rs *releaseServer) ghCalls() string {
	calls, _ := os.ReadFile(filepath.Join(rs.dir, "gh-calls"))
	return string(calls)
}

// fakeGH is gh attestation verify as far as bin/gantry uses it. A bundle line
// "<sha256> <ref>" attests a file with that digest built at that ref by
// gantry's release workflow, and gh fails on anything else, as the real one
// does on a digest, signer or ref the attestation doesn't carry.
func fakeGH(calls string, sourceRef bool) []byte {
	help := "echo '  -b, --bundle string'\n"
	if sourceRef {
		help += "echo '      --source-ref string'\n"
	}
	return []byte(`#!/bin/sh
[ "$1 $2" = "attestation verify" ] || exit 1
shift 2
if [ "$1" = --help ]; then
` + help + `	exit 0
fi
echo "$*" >>'` + calls + `'
file=$1
shift
while [ $# -gt 1 ]; do
	case $1 in
	--bundle) bundle=$2 ;;
	--repo) repo=$2 ;;
	--signer-workflow) workflow=$2 ;;
	--source-ref) ref=$2 ;;
	*) echo "unknown flag: $1" >&2; exit 1 ;;
	esac
	shift 2
done
if [ "$repo" != scttymn/gantry ] || [ "$workflow" != scttymn/gantry/.github/workflows/release.yml ]; then
	echo "Error: not signed by gantry's release workflow" >&2
	exit 1
fi
sum=$( (sha256sum "$file" 2>/dev/null || shasum -a 256 "$file") | awk '{print $1}')
grep -qx "$sum $ref" "$bundle" || { echo "Error: no attestation for sha256:$sum at $ref" >&2; exit 1; }
`)
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
// replaces the real digest (a bad checksum, or a lookalike filename). A
// gantry asset is attested too, as gantry's release workflow does.
func (rs *releaseServer) put(repo, version, asset string, body []byte, sums string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	path := "/" + repo + "/" + version + "/" + asset
	rs.files[path] = body
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if sums == "" {
		sums = digest + "  " + asset + "\n"
	}
	sumsPath := "/" + repo + "/" + version + "/SHA256SUMS"
	rs.files[sumsPath] = append(rs.files[sumsPath], []byte(sums)...)
	if repo == "gantry" {
		bundle := "/gantry/" + version + "/provenance.sigstore.json"
		rs.files[bundle] = append(rs.files[bundle], []byte(digest+" refs/tags/"+version+"\n")...)
	}
}

// set replaces (or, with nil, removes) what the server has at path.
func (rs *releaseServer) set(path string, body []byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if body == nil {
		delete(rs.files, path)
		return
	}
	rs.files[path] = body
}

func (rs *releaseServer) hitsOn(path string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.hits[path]
}

// env is stubEnv for this server.
func (rs *releaseServer) env(extra ...string) []string {
	return stubEnv(rs.srv.URL+"/gantry", rs.srv.URL+"/houston", append([]string{
		"CURL_CA_BUNDLE=" + filepath.Join(rs.dir, "ca.pem"),
		"PATH=" + filepath.Join(rs.dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
	}, extra...)...)
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
	raw, err := fs.ReadFile(templates, "templates/new/bin/gantry.tmpl")
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
	return dir, rs, rs.env()
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
			code, out := runStub(t, dir, rs.env(), "dev")
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
	code, out := runStub(t, dir, rs.env(), "dev")
	if code == 0 || !strings.Contains(out, "v0.11.3") || !strings.Contains(out, osName+"/"+arch) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gantry", "bin", "gantry-v0.11.3-"+osName+"-"+arch)); err == nil {
		t.Error("404 left a binary")
	}
}

// bin/gantry downloads only over https: an overridden base must be https,
// and no redirect leaves it.
func TestBinstubRequiresHTTPS(t *testing.T) {
	osName, arch := platform(t)
	var plainHits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		w.Write(cliScript("gantry-plain", ""))
	}))
	t.Cleanup(plain.Close)
	asset := "gantry-" + osName + "-" + arch
	for _, tc := range []struct {
		name string
		env  func(rs *releaseServer) []string
		want string
	}{
		{"gantry base", func(rs *releaseServer) []string { return rs.env("GANTRY_RELEASE_BASE=" + plain.URL + "/gantry") }, "isn't https"},
		{"houston base", func(rs *releaseServer) []string { return rs.env("HOUSTON_RELEASE_BASE=" + plain.URL + "/houston") }, "isn't https"},
		{"file base", func(rs *releaseServer) []string { return rs.env("GANTRY_RELEASE_BASE=file://" + t.TempDir()) }, "isn't https"},
		{"redirect to http", func(rs *releaseServer) []string {
			rs.mu.Lock()
			rs.moved["/gantry/v0.11.3/"+asset] = plain.URL + "/gantry/v0.11.3/" + asset
			rs.mu.Unlock()
			return rs.env()
		}, "v0.11.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, rs, _ := appWithCLIs(t, "v0.11.3")
			code, out := runStub(t, dir, tc.env(rs), "dev")
			if code == 0 || !strings.Contains(out, tc.want) || strings.Contains(out, "gantry-plain") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if n := plainHits.Load(); n != 0 {
				t.Errorf("%d downloads over http", n)
			}
		})
	}
}

// A gantry release after the last one published without build provenance
// must carry an attestation from gantry's release workflow at its tag. gh
// checks it when it can; without gh, SHA256SUMS stands alone, and says so.
func TestBinstubChecksProvenance(t *testing.T) {
	osName, arch := platform(t)
	kept := filepath.Join(".gantry", "bin", "gantry-v0.20.0-"+osName+"-"+arch)
	bundle := "/gantry/v0.20.0/provenance.sigstore.json"
	digest := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	for _, tc := range []struct {
		name string
		env  func(t *testing.T, rs *releaseServer) []string
		want string // in the output; a run that fails has no other check
		ok   bool
	}{
		{"attested", func(t *testing.T, rs *releaseServer) []string { return rs.env() }, "gantry-ok", true},
		{"another binary attested", func(t *testing.T, rs *releaseServer) []string {
			rs.set(bundle, []byte(digest([]byte("another build"))+" refs/tags/v0.20.0\n"))
			return rs.env()
		}, "failed its build provenance check", false},
		{"attested at another tag", func(t *testing.T, rs *releaseServer) []string {
			rs.set(bundle, []byte(digest(cliScript("gantry-ok", ""))+" refs/tags/v0.19.0\n"))
			return rs.env()
		}, "failed its build provenance check", false},
		{"no provenance published", func(t *testing.T, rs *releaseServer) []string {
			rs.set(bundle, nil)
			return rs.env()
		}, "published no build provenance", false},
		{"gh too old", func(t *testing.T, rs *releaseServer) []string {
			rs.gh(t, false)
			return rs.env()
		}, "SHA256SUMS only", true},
		{"no gh", func(t *testing.T, rs *releaseServer) []string {
			for _, d := range []string{"/usr/bin", "/bin"} {
				if _, err := os.Stat(filepath.Join(d, "gh")); err == nil {
					t.Skipf("gh is in %s", d)
				}
			}
			return rs.env("PATH=/usr/bin:/bin")
		}, "SHA256SUMS only", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, rs, _ := appWithCLIs(t, "v0.20.0")
			code, out := runStub(t, dir, tc.env(t, rs), "dev")
			if (code == 0) != tc.ok || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d\n%s", code, out)
			}
			_, err := os.Stat(filepath.Join(dir, kept))
			if tc.ok && (err != nil || !strings.Contains(out, "gantry-ok")) {
				t.Errorf("not installed (%v):\n%s", err, out)
			}
			if !tc.ok && (err == nil || strings.Contains(out, "gantry-ok")) {
				t.Errorf("installed or ran a binary that failed its provenance check:\n%s", out)
			}
		})
	}
	t.Run("checked by gh", func(t *testing.T) {
		dir, rs, env := appWithCLIs(t, "v0.20.0")
		if code, out := runStub(t, dir, env, "dev"); code != 0 {
			t.Fatal(out)
		}
		if calls := rs.ghCalls(); !strings.Contains(calls, "--source-ref refs/tags/v0.20.0") {
			t.Errorf("gh attestation verify calls: %q", calls)
		}
	})
}

// A release up to the last unattested one has no provenance to check, so
// bin/gantry neither asks for it nor warns.
func TestBinstubSkipsProvenanceBeforeItWasPublished(t *testing.T) {
	dir, rs, env := appWithCLIs(t, "v0.17.0")
	code, out := runStub(t, dir, env, "dev")
	if code != 0 || !strings.Contains(out, "gantry-ok") || strings.Contains(out, "provenance") || strings.Contains(out, "SHA256SUMS only") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if n := rs.hitsOn("/gantry/v0.17.0/provenance.sigstore.json"); n != 0 || rs.ghCalls() != "" {
		t.Errorf("v0.17.0's provenance was fetched %d times; gh calls %q", n, rs.ghCalls())
	}
}

func TestBinstubRejectsBadPins(t *testing.T) {
	osName, _ := platform(t)
	rs := newReleaseServer(t)
	t.Run("pseudo-version", func(t *testing.T) {
		dir := t.TempDir()
		writeMod(t, dir, "v0.0.0-20260101120000-abcdef123456", "")
		installBinstub(t, dir)
		code, out := runStub(t, dir, rs.env(), "dev")
		if code == 0 || !strings.Contains(out, "isn't a release tag") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("no require", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/shop\n\ngo 1.27.1\n"), 0o644)
		installBinstub(t, dir)
		code, out := runStub(t, dir, rs.env(), "dev")
		if code == 0 || !strings.Contains(out, "doesn't require github.com/scttymn/gantry") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("no go.mod", func(t *testing.T) {
		dir := t.TempDir()
		installBinstub(t, dir)
		code, out := runStub(t, dir, rs.env(), "dev")
		if code == 0 || !strings.Contains(out, "no go.mod") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("bad arch", func(t *testing.T) {
		dir := t.TempDir()
		writeMod(t, dir, "v0.11.3", "")
		installBinstub(t, dir)
		code, out := runStub(t, dir, rs.env("GANTRY_OS="+osName, "GANTRY_ARCH=sparc"), "dev")
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
	code, out := runStub(t, dir, rs.env(), "dev")
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
	env := rs.env(
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
			code, out := runStub(t, dir, rs.env(), "dev")
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
			code, out := runStub(t, dir, rs.env("HOUSTON_BIN="+houston), "g", "migration", "create_posts")
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
	rs.pause.Store(int64(200 * time.Millisecond))
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
	for _, needle := range []string{"SHA256SUMS", "gantry-*", "gh release create", "provenance.sigstore.json", "id-token: write", "attestations: write", "houston-release --sha256"} {
		if !strings.Contains(string(workflow), needle) {
			t.Errorf("workflow missing %q", needle)
		}
	}
	if !regexp.MustCompile(`uses: actions/attest@[0-9a-f]{40} `).Match(workflow) {
		t.Error("workflow doesn't attest the binaries with actions/attest pinned by commit")
	}
	// bin/gantry trusts an attestation signed by this workflow file, for the
	// asset it publishes.
	for _, needle := range []string{"provenance.sigstore.json", "--signer-workflow scttymn/gantry/.github/workflows/release.yml"} {
		if !strings.Contains(string(script), needle) {
			t.Errorf("script missing %q", needle)
		}
	}
	if strings.Contains(string(script), "{{") && !strings.Contains(string(script), "{{.Houston}}") {
		t.Error("script template has a go action other than the Houston pin")
	}
}
