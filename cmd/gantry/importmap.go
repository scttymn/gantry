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

// jspmAPI is jspm.org's generator, which resolves packages to their files,
// importmap-rails' source; tests point it, and httpClient, elsewhere.
// httpClient gives up on a request after a minute, so a stalled server
// fails gantry importmap and gantry fonts instead of hanging them.
var (
	jspmAPI    = "https://api.jspm.io/generate"
	httpClient = &http.Client{Timeout: time.Minute}
)

// vendorDir is where pinned packages go, under the app's root: the skeleton
// pins every file there (assets.PinVendor).
var vendorDir = filepath.Join("assets", "js", "vendor")

// importmapCommand is `gantry importmap pin|unpin PACKAGE...`, Rails'
// bin/importmap: pin downloads packages and what they import into
// assets/js/vendor/, served from the app, never a CDN; unpin removes them.
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

// licenseFiles are the names npm packs a package's license under, in the
// order pin asks the CDN for them, beside the package's package.json.
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
	// Everything is fetched before anything is written, so a failure
	// leaves the folder as it was.
	files := map[string][]byte{}
	unlicensed := map[string]string{} // a package with no license file: the license its package.json names
	for _, name := range names {
		url := got.Map.Imports[name]
		src, err := fetch(url)
		if err != nil {
			return err
		}
		v, notice, named := "", "", ""
		if m := version.FindStringSubmatchIndex(url); m != nil {
			v = "@" + url[m[2]:m[3]]
			notice, named = license(name+" "+url[m[2]:m[3]], url[:m[1]]+"/")
		}
		if notice == "" {
			unlicensed[name] = named
		}
		files[name] = append([]byte("// "+name+v+" downloaded from "+url+"\n\n"+notice), sourceMap.ReplaceAll(src, nil)...)
	}
	for _, name := range names {
		path := filepath.Join(root, vendorDir, vendorFile(name))
		if err := write(path, files[name]); err != nil {
			return err
		}
		fmt.Fprintf(out, "  pinned %s (%s)\n", name, relTo(root, path))
		if named, ok := unlicensed[name]; ok {
			if named != "" {
				named = " (package.json says " + named + ")"
			}
			fmt.Fprintf(out, "  no license file found for %s%s: add its notice to %s by hand\n", name, named, relTo(root, path))
		}
	}
	return nil
}

// license is the comment pin puts under a pinned file's first line, as
// gantry new's Turbo and Stimulus have it: a /*! */ comment, which
// minifiers keep, with the package and its version (title), its homepage,
// and its license file, from the CDN folder pkg, where its package.json is.
// With no license file there it's "", and named, the license the
// package.json names, if it names one, is what pin can say instead.
func license(title, pkg string) (comment, named string) {
	var meta struct{ Homepage, License string }
	if src, err := fetch(pkg + "package.json"); err == nil {
		// What won't parse (an old package's {"type": "MIT"} license, say)
		// is left out of the comment and the warning, which do without it.
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
