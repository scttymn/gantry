package web

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// system is a /proc/self/cgroup, a /proc/self/mountinfo and a /proc/meminfo,
// and the cgroup filesystems they name, made of these files under a new
// directory; "@" in mountinfo is that directory, where it's mounted.
func system(t *testing.T, files map[string]string) memoryFiles {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "@", dir)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return memoryFiles{self: filepath.Join(dir, "self"), mounts: filepath.Join(dir, "mountinfo"), meminfo: filepath.Join(dir, "meminfo")}
}

const (
	gib     = 1 << 30
	machine = "MemTotal:       16000000 kB\nMemFree:  1 kB\n"
	// cgroup2 mounted with its root at /, and cgroup v1's memory controller.
	v2Root = "30 25 0:26 / @/cg rw,nosuid - cgroup2 cgroup2 rw\n"
	v1Root = "40 25 0:30 / @/cg/memory rw - cgroup cgroup rw,memory\n41 25 0:31 / @/cg/cpu rw - cgroup cgroup rw,cpu\n"
)

// The runtime is told four fifths of the least the process may have: its
// cgroup's limit (memory.max, or memory.high when lower), every one's above
// it that the mount shows, on cgroup v2 or v1; and the machine's memory,
// which bounds them all, and is the limit where there's no other (a server
// of its own).
func TestMemoryLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		files     map[string]string
		container int64
	}{
		"a container: its cgroup at the mount's root": {map[string]string{
			"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "2147483648\n", "cg/memory.high": "max\n", "meminfo": machine}, 2 * gib},
		"memory.high lower": {map[string]string{
			"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "2147483648\n", "cg/memory.high": "1073741824\n", "meminfo": machine}, gib},
		"a service, its limit below the slice's": {map[string]string{
			"self": "0::/system.slice/app.service\n", "mountinfo": v2Root, "cg/system.slice/app.service/memory.max": "536870912\n",
			"cg/system.slice/memory.max": "1073741824\n", "meminfo": machine}, gib / 2},
		"a service, the slice's limit the lower": {map[string]string{
			"self": "0::/system.slice/app.service\n", "mountinfo": v2Root, "cg/system.slice/app.service/memory.max": "max\n",
			"cg/system.slice/memory.max": "1073741824\n", "meminfo": machine}, gib},
		"a container with the host's view: the mount's root is its cgroup": {map[string]string{
			"self": "0::/system.slice/docker-abc.scope\n", "mountinfo": "30 25 0:26 /system.slice/docker-abc.scope @/cg rw - cgroup2 cgroup2 rw\n",
			"cg/memory.max": "1073741824\n", "meminfo": machine}, gib},
		"the mount's root above the process's cgroup": {map[string]string{
			"self": "0::/tenant/worker\n", "mountinfo": "30 25 0:26 /tenant @/cg rw - cgroup2 cgroup2 rw\n",
			"cg/worker/memory.max": "536870912\n", "cg/memory.max": "1073741824\n", "meminfo": machine}, gib / 2},
		"cgroup v1": {map[string]string{
			"self": "12:memory:/docker/abc\n11:cpu:/docker/abc\n", "mountinfo": v1Root, "cg/memory/docker/abc/memory.limit_in_bytes": "1073741824\n", "meminfo": machine}, gib},
		"cgroup v1, the parent's limit the lower": {map[string]string{
			"self": "12:memory:/parent/child\n", "mountinfo": v1Root, "cg/memory/parent/child/memory.limit_in_bytes": "9223372036854771712\n",
			"cg/memory/parent/memory.limit_in_bytes": "1073741824\n", "meminfo": machine}, gib},
		"cgroup v1, the memory controller mounted with cpu": {map[string]string{
			"self": "5:cpu,memory:/app\n", "mountinfo": "40 25 0:30 / @/cg/cpu,memory rw - cgroup cgroup rw,cpu,memory\n",
			"cg/cpu,memory/app/memory.limit_in_bytes": "1073741824\n", "meminfo": machine}, gib},
		"a mount point with a space in it": {map[string]string{
			"self": "0::/\n", "mountinfo": "30 25 0:26 / @/c\\040g rw - cgroup2 cgroup2 rw\n", "c g/memory.max": "1073741824\n", "meminfo": machine}, gib},
		"a server of its own, 2 GB": {map[string]string{
			"self": "0::/user.slice/user-0.slice/session-1.scope\n", "mountinfo": v2Root, "meminfo": "MemTotal:        2014312 kB\n"}, 2014312 << 10},
		"a container past the machine": {map[string]string{
			"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "8589934592\n", "meminfo": "MemTotal:        2014312 kB\n"}, 2014312 << 10},
		"a machine of 2 TiB": {map[string]string{"meminfo": "MemTotal:  2147483648 kB\n"}, 2 << 40},
	} {
		limit, container := memoryLimit(system(t, tc.files))
		if want := tc.container / 5 * 4; limit != want || container != tc.container {
			t.Errorf("%s: memoryLimit = %d of %d; want %d of %d", name, limit, container, want, tc.container)
		}
	}
}

// Where there's nothing to read, nothing that's a limit, or a cgroup the
// mount doesn't show, none is set.
func TestMemoryLimitNone(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"not Linux":       {},
		"no limit at all": {"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "max\n"},
		"v1, no limit":    {"self": "4:memory:/\n", "mountinfo": v1Root, "cg/memory/memory.limit_in_bytes": "9223372036854771712\n"},
		"not a number":    {"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "plenty\n", "meminfo": "MemTotal: lots kB\n"},
		"nothing":         {"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "0\n"},
		"negative":        {"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "-1\n"},
		"above the mount": {"self": "0::/../../etc\n", "mountinfo": v2Root, "etc/memory.max": "1073741824\n"},
		"another subtree": {"self": "0::/other/job\n", "mountinfo": "30 25 0:26 /tenant @/cg rw - cgroup2 cgroup2 rw\n", "cg/memory.max": "1073741824\n"},
		"no mountinfo":    {"self": "0::/\n", "cg/memory.max": "1073741824\n"},
	} {
		if limit, container := memoryLimit(system(t, files)); limit != 0 || container != 0 {
			t.Errorf("%s: memoryLimit = %d of %d; want none", name, limit, container)
		}
	}
}

// GOMEMLIMIT is the operator's: with it set, gantry sets nothing.
func TestMemoryLimitLeavesGOMEMLIMIT(t *testing.T) {
	files := system(t, map[string]string{"self": "0::/\n", "mountinfo": v2Root, "cg/memory.max": "2147483648\n"})
	var set []int64
	apply := func(n int64) { set = append(set, n) }
	if limit, _ := setMemoryLimit(files, "1GiB", apply); limit != 0 || len(set) != 0 {
		t.Errorf("with GOMEMLIMIT set: limit %d, set %v; want nothing set", limit, set)
	}
	if limit, container := setMemoryLimit(files, "", apply); limit != 2*gib/5*4 || container != 2*gib || len(set) != 1 || set[0] != limit {
		t.Errorf("without it: limit %d of %d, set %v", limit, container, set)
	}
	if limit, _ := setMemoryLimit(system(t, nil), "", apply); limit != 0 || len(set) != 1 {
		t.Errorf("with nothing to read: limit %d, set %v; want nothing more set", limit, set)
	}
}

// The router says what was set, once, however many handlers are built.
func TestMemoryLimitLogged(t *testing.T) {
	was := memory
	t.Cleanup(func() { memory = was })
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))

	memory = &memorySet{limit: 2 * gib / 5 * 4, container: 2 * gib}
	NewRouter(log, nil).Handler()
	NewRouter(log, nil).Handler()
	if n := strings.Count(out.String(), "memory limit"); n != 1 || !strings.Contains(out.String(), "limit=1.6GiB") || !strings.Contains(out.String(), "of=2.0GiB") {
		t.Errorf("logged %d times:\n%s", n, out.String())
	}

	out.Reset()
	memory = &memorySet{}
	NewRouter(log, nil).Handler()
	if out.Len() != 0 {
		t.Errorf("nothing was set, and it said:\n%s", out.String())
	}
}
