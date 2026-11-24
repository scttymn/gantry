package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestHoustonReleaseCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"houston-release"}, t.TempDir(), &out, &errOut); code != 0 || out.String() != houstonRelease+"\n" {
		t.Fatalf("gantry houston-release = %d %q %q", code, out.String(), errOut.String())
	}
}

func TestHoustonReleaseSHA256Command(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"houston-release", "--sha256"}, t.TempDir(), &out, &errOut); code != 0 {
		t.Fatalf("gantry houston-release --sha256 = %d %q", code, errOut.String())
	}
	line := regexp.MustCompile(`\A[0-9a-f]{64}  houston-(darwin|linux)-(amd64|arm64)\z`)
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if !line.MatchString(l) {
			t.Errorf("not a SHA256SUMS line for a houston asset: %q", l)
			continue
		}
		seen[strings.Fields(l)[1]] = true
	}
	for _, asset := range []string{"houston-darwin-amd64", "houston-darwin-arm64", "houston-linux-amd64", "houston-linux-arm64"} {
		if !seen[asset] {
			t.Errorf("no digest for %s in\n%s", asset, out.String())
		}
	}
}

func gantryNaming(houston string) []byte {
	return []byte("#!/bin/sh\nif [ \"$1\" = houston-release ]; then echo " + houston + "; exit 0; fi\necho gantry-ok\nhouston\n")
}

func gantryPinning(houston, sums string) []byte {
	return []byte("#!/bin/sh\nif [ \"$1 $2\" = 'houston-release --sha256' ]; then printf '%s' '" + sums + "'; exit 0; fi\n" +
		"if [ \"$1\" = houston-release ]; then echo " + houston + "; exit 0; fi\necho gantry-ok\nhouston\n")
}

func TestBinstubChecksHoustonAgainstGantrysDigests(t *testing.T) {
	osName, arch := platform(t)
	asset := "houston-" + osName + "-" + arch
	good := []byte("#!/bin/sh\necho houston-good\n")
	sum := sha256.Sum256(good)
	pins := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	for _, tc := range []struct {
		name    string
		gantry  []byte
		houston []byte
		want    string
		ok      bool
	}{
		{"pinned", gantryPinning("v9.9.9", pins), good, "houston-good", true},
		{"replaced release", gantryPinning("v9.9.9", pins), []byte("#!/bin/sh\necho houston-evil\n"), "failed its checksum", false},
		{"gantry pins none", gantryNaming("v9.9.9"), good, "checked against its own release's SHA256SUMS", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMod(t, dir, "v0.20.0", "")
			installBinstub(t, dir)
			rs := newReleaseServer(t)
			rs.put("gantry", "v0.20.0", "gantry-"+osName+"-"+arch, tc.gantry, "")
			rs.put("houston", "v9.9.9", asset, tc.houston, "")
			code, out := runStub(t, dir, rs.env(), "dev")
			if (code == 0) != tc.ok || !strings.Contains(out, tc.want) || strings.Contains(out, "houston-evil") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			_, err := os.Stat(filepath.Join(dir, ".gantry", "bin", "houston-v9.9.9-"+osName+"-"+arch))
			if tc.ok != (err == nil) {
				t.Errorf("installed = %v, want %v", err == nil, tc.ok)
			}
			if pinned := tc.name != "gantry pins none"; pinned && strings.Contains(out, "own release's SHA256SUMS") {
				t.Errorf("warned with gantry's digests in hand:\n%s", out)
			}
		})
	}
}

func TestBinstubUsesTheHoustonGantryNames(t *testing.T) {
	osName, arch := platform(t)
	dir := t.TempDir()
	writeMod(t, dir, "v0.20.0", "")
	installBinstub(t, dir)
	rs := newReleaseServer(t)
	rs.put("gantry", "v0.20.0", "gantry-"+osName+"-"+arch, gantryNaming("v9.9.9"), "")
	rs.put("houston", "v9.9.9", "houston-"+osName+"-"+arch, []byte("#!/bin/sh\necho houston-named\n"), "")
	code, out := runStub(t, dir, rs.env(), "dev")
	if code != 0 || !strings.Contains(out, "houston-named") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gantry", "bin", "houston-v9.9.9-"+osName+"-"+arch)); err != nil {
		t.Fatalf("Houston v9.9.9 wasn't fetched: %v", err)
	}
}

func TestBinstubFallsBackToItsPin(t *testing.T) {
	dir, rs, env := appWithCLIs(t, "v0.11.3")
	code, out := runStub(t, dir, env, "dev")
	if code != 0 || !strings.Contains(out, "houston-ok") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	osName, arch := platform(t)
	if rs.hits["/houston/"+houstonRelease+"/houston-"+osName+"-"+arch] == 0 {
		t.Fatalf("the pinned Houston %s wasn't fetched", houstonRelease)
	}
}

func TestRefreshBinstub(t *testing.T) {
	current := renderBinstub(t)
	t.Run("an older bin/gantry is rewritten", func(t *testing.T) {
		dir := t.TempDir()
		old := strings.Replace(string(current), `houston_version="`+houstonRelease+`"`, `houston_version="v0.5.4"`, 1)
		writeBinstub(t, dir, []byte(old))
		var note bytes.Buffer
		refreshBinstub(dir, &note)
		if got := readBinstub(t, dir); !bytes.Equal(got, current) {
			t.Fatalf("bin/gantry wasn't refreshed:\n%s", got)
		}
		if !strings.Contains(note.String(), "updated bin/gantry") {
			t.Errorf("no note: %q", note.String())
		}
		if info, _ := os.Stat(filepath.Join(dir, "bin", "gantry")); info.Mode()&0o111 == 0 {
			t.Errorf("bin/gantry isn't executable: %v", info.Mode())
		}
	})
	t.Run("a current one is left alone, silently", func(t *testing.T) {
		dir := t.TempDir()
		writeBinstub(t, dir, current)
		var note bytes.Buffer
		refreshBinstub(dir, &note)
		if note.Len() != 0 {
			t.Errorf("note for a current bin/gantry: %q", note.String())
		}
	})
	t.Run("a bin/gantry gantry didn't write is left alone", func(t *testing.T) {
		dir := t.TempDir()
		mine := []byte("#!/bin/sh\nexec my-own-gantry \"$@\"\n")
		writeBinstub(t, dir, mine)
		refreshBinstub(dir, &bytes.Buffer{})
		if !bytes.Equal(readBinstub(t, dir), mine) {
			t.Fatal("a hand-written bin/gantry was replaced")
		}
	})
	t.Run("no bin/gantry: nothing written", func(t *testing.T) {
		dir := t.TempDir()
		refreshBinstub(dir, &bytes.Buffer{})
		if _, err := os.Stat(filepath.Join(dir, "bin", "gantry")); !os.IsNotExist(err) {
			t.Fatalf("bin/gantry appeared: %v", err)
		}
	})
}

func renderBinstub(t *testing.T) []byte {
	t.Helper()
	b, err := binstub()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeBinstub(t *testing.T, dir string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "gantry"), body, 0o755); err != nil {
		t.Fatal(err)
	}
}

func readBinstub(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "bin", "gantry"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
