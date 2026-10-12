package devcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type exception struct {
	Rule   string `json:"rule"`
	File   string `json:"file"`
	Reason string `json:"reason"`
}

func applyExceptions(root string, warnings []Warning, rules map[string]Rule) ([]Warning, error) {
	f, err := os.Open(filepath.Join(root, ".gantry-check.json"))
	if os.IsNotExist(err) {
		return warnings, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cfg struct {
		Exceptions []exception `json:"exceptions"`
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf(".gantry-check.json: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf(".gantry-check.json: expected one JSON object")
	}
	for _, e := range cfg.Exceptions {
		if _, ok := rules[e.Rule]; !ok {
			return nil, fmt.Errorf(".gantry-check.json: unknown rule %q", e.Rule)
		}
		if e.File == "" || filepath.IsAbs(e.File) || strings.Contains(e.File, "\\") || strings.ContainsAny(e.File, "*?[") || e.File == ".." || strings.HasPrefix(e.File, "../") || filepath.ToSlash(filepath.Clean(e.File)) != e.File || strings.TrimSpace(e.Reason) == "" {
			return nil, fmt.Errorf(".gantry-check.json: each exception needs an exact project-relative file and a nonempty reason")
		}
	}
	active := warnings[:0]
	for _, w := range warnings {
		ignored := false
		for _, e := range cfg.Exceptions {
			if e.Rule == w.Rule && e.File == w.File {
				ignored = true
				break
			}
		}
		if !ignored {
			active = append(active, w)
		}
	}
	return active, nil
}
