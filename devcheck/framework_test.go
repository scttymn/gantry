package devcheck

import (
	"path/filepath"
	"testing"
)

// Strict reads gantry itself, not only the apps it writes: the framework's
// code, where the rules that aren't about an app's layout apply, and
// bench/feed, an app gantry new wrote. Advice fails too, as does an
// exception in .gantry-check.json that's gone stale.
func TestGantryPassesStrict(t *testing.T) {
	for _, root := range []string{"..", filepath.Join("..", "bench", "feed")} {
		warnings, err := Scan(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range warnings {
			t.Errorf("%s: %s:%d %s: %s", root, w.File, w.Line, w.Rule, w.Message)
		}
	}
}
