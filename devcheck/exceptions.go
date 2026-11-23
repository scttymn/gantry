package devcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type exception struct {
	Rule   string `json:"rule"`
	File   string `json:"file"`
	Reason string `json:"reason"`
}

var citesIssue = regexp.MustCompile(`#[0-9]+|https?://\S`)

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
		RequireIssue bool        `json:"requireIssue"`
		Exceptions   []exception `json:"exceptions"`
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
		if cfg.RequireIssue && !citesIssue.MatchString(e.Reason) {
			return nil, fmt.Errorf(`.gantry-check.json: exception %s %s: reason must cite an issue (#123) or link an issue or decision record; "requireIssue" is set`, e.Rule, e.File)
		}
	}
	matched := make([]bool, len(cfg.Exceptions))
	active := warnings[:0]
	for _, w := range warnings {
		ignored := false
		for i, e := range cfg.Exceptions {
			if e.Rule == w.Rule && e.File == w.File {
				matched[i] = true
				ignored = true
			}
		}
		if !ignored {
			active = append(active, w)
		}
	}
	// Advice, not a violation: an upgrade that fixes a rule's false positive
	// shouldn't fail CI until someone deletes the exception.
	for i, e := range cfg.Exceptions {
		if !matched[i] {
			active = append(active, Warning{File: ".gantry-check.json", Rule: "stale-exception", Severity: Advice, Message: fmt.Sprintf("exception %s %s matched nothing; remove it so it can't hide a future violation", e.Rule, e.File)})
		}
	}
	return active, nil
}
