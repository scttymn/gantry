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
)

// jspmAPI is jspm.org's generator, which resolves packages to their files,
// importmap-rails' source; tests point it, and httpClient, elsewhere.
var (
	jspmAPI    = "https://api.jspm.io/generate"
	httpClient = http.DefaultClient
)

// vendorDir is where pinned packages go, under the app's root: the skeleton
// pins every file there (assets.PinVendor).
var vendorDir = filepath.Join("assets", "js", "vendor")

// importmapCommand is `gantry importmap pin|unpin PACKAGE...`, Rails'
// bin/importmap: pin downloads packages and what they import into
// assets/js/vendor/, served from the app, never a CDN; unpin removes them.
func importmapCommand(root string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errExit(2)
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
	return errExit(2)
}

// packageName is a package without its version: "@hotwired/turbo@8" is
// "@hotwired/turbo".
func packageName(spec string) string {
	if i := strings.LastIndex(spec, "@"); i > 0 {
		return spec[:i]
	}
	return spec
}

// vendorFile is a package's file: "/" is "--", as importmap-rails names it.
func vendorFile(name string) string { return strings.ReplaceAll(name, "/", "--") + ".js" }

var (
	sourceMap = regexp.MustCompile(`(?m)^//# sourceMappingURL=.*$\n?`)
	version   = regexp.MustCompile(`@(\d+\.\d+\.\d+[^/\s"]*)`)
)

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
	// Everything is fetched before anything is written, so a failure
	// leaves the folder as it was.
	files := map[string][]byte{}
	for _, name := range names {
		url := got.Map.Imports[name]
		src, err := fetch(url)
		if err != nil {
			return err
		}
		v := ""
		if m := version.FindStringSubmatch(url); m != nil {
			v = "@" + m[1]
		}
		files[name] = append([]byte("// "+name+v+" downloaded from "+url+"\n\n"), sourceMap.ReplaceAll(src, nil)...)
	}
	for _, name := range names {
		path := filepath.Join(root, vendorDir, vendorFile(name))
		if err := write(path, files[name]); err != nil {
			return err
		}
		fmt.Fprintf(out, "  pinned %s (%s)\n", name, relTo(root, path))
	}
	return nil
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
