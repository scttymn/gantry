package main

import (
	"flag"
	"io"
)

func routesFlags() (fs *flag.FlagSet, local, asJSON *bool) {
	fs, local = appFlags()
	asJSON = fs.Bool("json", false, "a JSON array of web.Route, with stable field names")
	return fs, local, asJSON
}

func routes(root string, args []string, _, errOut io.Writer) error {
	fs, local, asJSON := routesFlags()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError{}
	}
	app := []string{"routes"}
	if *asJSON {
		app = append(app, "--json")
	}
	return runApp(root, app, *local, errOut)
}
