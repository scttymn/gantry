package devcheck

import (
	"path/filepath"
	"testing"
)

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
