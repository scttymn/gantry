package web

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type memorySet struct {
	limit, container int64
	said             sync.Once
}

var memory = &memorySet{}

func init() {
	files := memoryFiles{self: "/proc/self/cgroup", mounts: "/proc/self/mountinfo", meminfo: "/proc/meminfo"}
	memory.limit, memory.container = setMemoryLimit(files, os.Getenv("GOMEMLIMIT"), func(n int64) { debug.SetMemoryLimit(n) })
}

type memoryFiles struct {
	self    string
	mounts  string
	meminfo string
}

func setMemoryLimit(files memoryFiles, env string, apply func(int64)) (limit, container int64) {
	if env != "" {
		return 0, 0
	}
	if limit, container = memoryLimit(files); limit > 0 {
		apply(limit)
	}
	return limit, container
}

func memoryLimit(files memoryFiles) (limit, container int64) {
	least := func(n int64) {
		if n > 0 && n < 1<<62 && (container == 0 || n < container) {
			container = n
		}
	}
	mounts := cgroupMounts(files.mounts)
	if b, err := os.ReadFile(files.self); err == nil {
		for line := range strings.Lines(string(b)) {
			parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
			if len(parts) != 3 {
				continue
			}
			v2 := parts[0] == "0" && parts[1] == ""
			if !v2 && !slices.Contains(strings.Split(parts[1], ","), "memory") {
				continue
			}
			for _, m := range mounts {
				dir, ok := m.dir(parts[2])
				if !ok || m.v2 != v2 {
					continue
				}
				for ; ; dir = filepath.Dir(dir) {
					if v2 {
						least(number(filepath.Join(dir, "memory.max")))
						least(number(filepath.Join(dir, "memory.high")))
					} else {
						least(number(filepath.Join(dir, "memory.limit_in_bytes")))
					}
					if dir == m.point || !strings.HasPrefix(dir, m.point) {
						break
					}
				}
				break
			}
		}
	}
	if b, err := os.ReadFile(files.meminfo); err == nil {
		for line := range strings.Lines(string(b)) {
			if f := strings.Fields(line); len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					least(kb << 10)
				}
			}
		}
	}
	return container / 5 * 4, container
}

type cgroupMount struct {
	root, point string
	v2          bool
}

func (m cgroupMount) dir(path string) (string, bool) {
	var rel string
	switch {
	case m.root == "/":
		rel = path
	case path == m.root:
		rel = "/"
	case strings.HasPrefix(path, m.root+"/"):
		rel = path[len(m.root):]
	default:
		return "", false
	}
	if !strings.HasPrefix(rel, "/") || rel != "/" && !filepath.IsLocal(rel[1:]) {
		return "", false
	}
	return filepath.Join(m.point, rel), true
}

func cgroupMounts(path string) (mounts []cgroupMount) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		dash := slices.Index(f, "-")
		if dash < 6 || len(f) < dash+4 {
			continue
		}
		fs, super := f[dash+1], f[dash+3]
		v2 := fs == "cgroup2"
		if !v2 && !(fs == "cgroup" && slices.Contains(strings.Split(super, ","), "memory")) {
			continue
		}
		mounts = append(mounts, cgroupMount{root: unescape(f[3]), point: filepath.Clean(unescape(f[4])), v2: v2})
	}
	return mounts
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func number(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (m *memorySet) say(log *slog.Logger) {
	if m.limit == 0 || log == nil {
		return
	}
	m.said.Do(func() {
		size := func(n int64) string {
			if n < 1<<30 {
				return fmt.Sprintf("%dMiB", n>>20)
			}
			return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
		}
		log.Info("memory limit", "limit", size(m.limit), "of", size(m.container))
	})
}
