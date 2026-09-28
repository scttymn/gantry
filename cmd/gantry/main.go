// Command gantry makes the files an app is built from, in gantry's layout.
//
//	gantry g resource admin/pillars title:string:required body:string position:position
//
// writes a table's queries and rules beside its model (app/models) and its
// pages in a folder of their own (app/admin/pillars), and prints the route to
// add. Run it at the app's root; then sqlc generate and templ generate, as
// the app's toolchain does. The files are plain code, to finish by hand.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr)) }

const usage = `usage:
  gantry g resource [NAMESPACE/]TABLE FIELD:TYPE[:required]...

TYPE is string, text, int, position (a sortable list's), date, or photo (an
Active Storage slot, not a column).
`

func run(args []string, root string, out, errOut io.Writer) int {
	if len(args) >= 2 && (args[0] == "g" || args[0] == "generate") && args[1] == "resource" {
		if err := generateResource(root, args[2:], out); err != nil {
			fmt.Fprintln(errOut, "gantry:", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(errOut, usage)
	return 2
}
