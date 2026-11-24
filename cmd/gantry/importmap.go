package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	jspmAPI    = "https://api.jspm.io/generate"
	httpClient = &http.Client{Timeout: time.Minute}
)

var vendorDir = filepath.Join("assets", "js", "vendor")

func importmapCommand(root string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return usageError{}
	}
	if _, err := os.Stat(filepath.Join(root, "assets")); err != nil {
		return errors.New("no assets/ here: run gantry at the app's root")
	}
	switch args[0] {
	case "pin":
		return pinPackages(root, args[1:], out)
	case "unpin":
		for _, name := range args[1:] {
			path := filepath.Join(root, vendorDir, vendorFile(packageName(name)))
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("%s isn't pinned (%s)", name, relTo(root, path))
			}
			fmt.Fprintln(out, "  removed", relTo(root, path))
		}
		return nil
	}
	return usageError{}
}

func packageName(spec string) string {
	if i := strings.LastIndex(spec, "@"); i > 0 {
		return spec[:i]
	}
	return spec
}

func vendorFile(name string) string { return strings.ReplaceAll(name, "/", "--") + ".js" }

var (
	sourceMap = regexp.MustCompile(`(?m)^//# sourceMappingURL=.*$\n?`)
	version   = regexp.MustCompile(`@(\d+\.\d+\.\d+[^/\s"]*)`)
)

var licenseFiles = []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "license", "license.md", "LICENCE"}

func pinPackages(root string, specs []string, out io.Writer) error {
	body, _ := json.Marshal(map[string]any{"install": specs, "flattenScope": true, "env": []string{"browser", "module", "production"}})
	resp, err := httpClient.Post(jspmAPI, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resolving %s: %w", strings.Join(specs, " "), err)
	}
	defer resp.Body.Close()
	var got struct {
		Error string
		Map   struct{ Imports map[string]string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		if got.Error != "" {
			return fmt.Errorf("resolving %s: %s", strings.Join(specs, " "), got.Error)
		}
		return fmt.Errorf("resolving %s: jspm answered %s", strings.Join(specs, " "), resp.Status)
	}
	if len(got.Map.Imports) == 0 {
		return fmt.Errorf("resolving %s: no files", strings.Join(specs, " "))
	}
	names := make([]string, 0, len(got.Map.Imports))
	for name := range got.Map.Imports {
		names = append(names, name)
	}
	sort.Strings(names)
	files := map[string][]byte{}
	notes := map[string]string{}
	for _, name := range names {
		url := got.Map.Imports[name]
		path := filepath.Join(root, vendorDir, vendorFile(name))
		src, err := fetch(url)
		if err != nil {
			return err
		}
		v, at, notice, named := "", "", "", ""
		if m := version.FindStringSubmatchIndex(url); m != nil {
			v = url[m[2]:m[3]]
			at = "@" + v
			notice, named = license(name+" "+v, url[:m[1]]+"/")
		}
		if notice == "" {
			if notice = keptNotice(path, v); notice != "" {
				notes[name] = "kept " + name + "'s license notice from the file it replaced (no license file found)"
			} else {
				if named != "" {
					named = " (package.json says " + named + ")"
				}
				notes[name] = "no license file found for " + name + named + ": add its notice to " + relTo(root, path) + " by hand"
			}
		}
		files[name] = append([]byte("// "+name+at+" downloaded from "+url+"\n\n"+notice), sourceMap.ReplaceAll(src, nil)...)
	}
	for _, name := range names {
		path := filepath.Join(root, vendorDir, vendorFile(name))
		if err := write(path, files[name]); err != nil {
			return err
		}
		fmt.Fprintf(out, "  pinned %s (%s)\n", name, relTo(root, path))
		if note, ok := notes[name]; ok {
			fmt.Fprintln(out, "  "+note)
		}
	}
	return nil
}

var titleVersion = regexp.MustCompile(`\d+\.\d+\.\d+\S*$`)

func keptNotice(path, v string) string {
	src, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_, rest, _ := strings.Cut(string(src), "\n")
	rest, opened := strings.CutPrefix(rest, "\n/*!\n")
	body, _, closed := strings.Cut(rest, "\n*/\n")
	if !opened || !closed {
		return ""
	}
	title, text, _ := strings.Cut(body, "\n")
	if v != "" {
		title = titleVersion.ReplaceAllLiteralString(title, v)
	}
	return "/*!\n" + title + "\n" + text + "\n*/\n\n"
}

func license(title, pkg string) (comment, named string) {
	var meta struct{ Homepage, License string }
	if src, err := fetch(pkg + "package.json"); err == nil {
		_ = json.Unmarshal(src, &meta)
	}
	for _, file := range licenseFiles {
		text, err := fetch(pkg + file)
		if err != nil {
			continue
		}
		if meta.Homepage != "" {
			title += "\n" + meta.Homepage
		}
		return "/*!\n" + title + "\n\n" + strings.TrimSpace(string(text)) + "\n*/\n\n", meta.License
	}
	return "", meta.License
}

func fetch(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
