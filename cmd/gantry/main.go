// Command gantry makes an app and the files it's built from, in gantry's
// layout, and runs it with Houston:
//
//	gantry new myapp           a new app, at http://myapp.localhost under gantry dev
//	gantry g resource admin/pillars title:string:required body:string position:position
//	gantry db migrate          the app's own commands, in its container
//	gantry dev                 Houston's commands, run by gantry
//
// The files it writes are plain code, to finish by hand.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr)) }

const usage = `usage:
  gantry new NAME [--db sqlite|postgres] [--module PATH] [--gantry PATH] [--in-module] [--skip-houston]
  gantry g resource [NAMESPACE/]TABLE FIELD:TYPE[:required]...
  gantry g migration NAME [FIELD:TYPE[:required]...]
  gantry g error-pages [--force]
  gantry g api-tokens [--prefix PREFIX_]                      a recipe: named API tokens
  gantry db migrate|rollback [N]|status|seed|reset|console   in the app's container
  gantry task NAME [ARGS...], gantry tasks                    (--local: on this machine)
  gantry dev|test|console|deploy|...                          Houston's commands

g resource: TYPE is string, text, int, position (a sortable list's), date, or
photo (an Active Storage slot, not a column).
g migration: NAME shapes it (create_posts, add_email_to_users); TYPE is
string, text, int, position, bool, date or datetime.
`

func run(args []string, root string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		return 0
	}
	var err error = errExit(2)
	switch args[0] {
	case "new":
		err = newApp(root, args[1:], out, errOut)
	case "g", "generate":
		if len(args) < 2 {
			break
		}
		switch args[1] {
		case "resource":
			err = generateResource(root, args[2:], out)
		case "migration":
			err = generateMigration(root, args[2:], out)
		case "api-tokens":
			err = generateAPITokens(root, args[2:], out)
		case "error-pages":
			if force := len(args) == 3 && args[2] == "--force"; len(args) == 2 || force {
				err = generateErrorPages(root, force, out)
			}
		}
	case "db", "task", "tasks":
		err = appCommand(root, args, errOut)
	default:
		// Everything else is Houston's: gantry dev is houston dev.
		err = houston(root, args, errOut)
	}
	var exit errExit
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if exit == 2 && (args[0] == "g" || args[0] == "generate") {
			fmt.Fprint(errOut, usage)
		}
		return int(exit)
	}
	fmt.Fprintln(errOut, "gantry:", err)
	return 1
}
