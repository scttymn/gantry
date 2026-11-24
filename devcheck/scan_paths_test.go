package devcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanPathsOnlyScansRequestedFiles(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app/a.js": "const a = document.querySelector('form');\n",
		"app/b.js": "const b = document.querySelector('form');\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	warnings, err := ScanPaths(root, []string{"app/a.js", "app/deleted.js"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].File != "app/a.js" {
		t.Fatalf("warnings = %+v, want only app/a.js", warnings)
	}
}
