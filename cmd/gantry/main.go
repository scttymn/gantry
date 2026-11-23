// Command gantry makes an app and the files it's built from, in gantry's
// layout, and runs it with Houston:
//
//	gantry new myapp           a new app, at http://myapp.localhost under gantry dev
//	gantry g resource posts title:string:required body:text
//	gantry db migrate          the app's own commands, in its container
//	gantry dev                 Houston's commands, run by gantry
//
// The files it writes are plain code, to finish by hand.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr)) }

// command is one of gantry's commands. The table of them, commands, is what
// run dispatches, what the usage says, and what the docs' examples are
// checked against (TestDocsCommands), so none of them can say a command or
// a flag gantry doesn't have.
type command struct {
	name  string // "new", "g resource"
	args  string // what it takes but flags: "NAME"
	about string
	// flags is its flags, as it parses them (nil: none). Its usage lists
	// them, a flag with a value named by its usage's `quoted` word.
	flags func() *flag.FlagSet
	run   func(root string, args []string, out, errOut io.Writer) error
	// houston: Houston's command, which gantry runs with every argument as
	// it is, so its flags are Houston's.
	houston bool
}

var commands = []command{
	{name: "check", about: "development checks (no server needed)", flags: func() *flag.FlagSet { fs, _, _, _ := checkFlags(); return fs },
		run: func(root string, args []string, out, _ io.Writer) error { return developmentCheck(root, args, out) }},
	{name: "new", args: "NAME", flags: func() *flag.FlagSet { return new(newOptions).flags("NAME") }, run: newApp},
	{name: "g resource", args: "[NAMESPACE/]TABLE FIELD:TYPE[:required]...", about: "a table, its model and its pages",
		run: func(root string, args []string, out, _ io.Writer) error { return generateResource(root, args, out) }},
	{name: "g migration", args: "NAME [FIELD:TYPE[:required]...]",
		run: func(root string, args []string, out, _ io.Writer) error { return generateMigration(root, args, out) }},
	{name: "g error-pages", flags: func() *flag.FlagSet { fs, _ := errorPagesFlags(); return fs },
		run: func(root string, args []string, out, _ io.Writer) error { return errorPagesCommand(root, args, out) }},
	{name: "g api-tokens", about: "a recipe: named API tokens", flags: func() *flag.FlagSet { fs, _ := apiTokensFlags(); return fs },
		run: func(root string, args []string, out, _ io.Writer) error { return generateAPITokens(root, args, out) }},
	{name: "g auth", about: "a recipe: password, email-code (--signup) or social sign-in", flags: func() *flag.FlagSet { fs, _, _, _ := authFlags(); return fs },
		run: func(root string, args []string, out, _ io.Writer) error { return generateAuth(root, args, out) }},
	{name: "g storage", about: "Active Storage's tables, for attached files",
		run: func(root string, args []string, out, _ io.Writer) error { return generateStorage(root, args, out) }},
	{name: "g fonts", args: "FAMILY[:400,600,400i]...", about: "Google Fonts, served from the app", flags: func() *flag.FlagSet { fs, _, _ := fontsFlags(); return fs },
		run: func(root string, args []string, out, _ io.Writer) error { return generateFonts(root, args, out) }},
	{name: "importmap", args: "pin|unpin PACKAGE[@VERSION]...", about: "JavaScript packages, into assets/js/vendor",
		run: func(root string, args []string, out, _ io.Writer) error { return importmapCommand(root, args, out) }},
	{name: "db", args: "migrate|rollback [N]|status|seed|reset|console", about: "in the app's container, or on this machine", flags: localFlag, run: appCommand("db")},
	{name: "task", args: "NAME [ARGS...]", about: "the app's tasks, in its container", flags: localFlag, run: appCommand("task")},
	{name: "tasks", about: "the tasks there are", flags: localFlag, run: appCommand("tasks")},
	{name: "routes", about: "the app's routes, as its router has them", flags: func() *flag.FlagSet { fs, _, _ := routesFlags(); return fs }, run: routes},
	{name: "dev", houston: true, run: func(root string, args []string, _, errOut io.Writer) error {
		// Advice must never prevent the development server from starting.
		if err := developmentCheck(root, nil, errOut); err != nil {
			fmt.Fprintln(errOut, "gantry check:", err)
		}
		return houston(root, append([]string{"dev"}, args...), errOut)
	}},
	{name: "test", houston: true},
	{name: "console", houston: true},
	{name: "exec", houston: true},
	{name: "status", houston: true},
	{name: "deploy", houston: true},
}

func localFlag() *flag.FlagSet { fs, _ := appFlags(); return fs }

// helpArgs ask for the usage.
var helpArgs = map[string]bool{"help": true, "-h": true, "--help": true}

const usageNotes = `
g resource: TYPE is string, text, int, position (a sortable list's), date, or
photo (an Active Storage slot, not a column).
g migration: NAME shapes it (create_posts, add_email_to_users); TYPE is
string, text, int, position, bool, date or datetime.
`

// usage is every command's synopsis, then Houston's, which gantry runs:
// any command it doesn't have is Houston's.
func usage() string {
	var b strings.Builder
	b.WriteString("usage:\n")
	line := func(synopsis, about string) {
		const column = 62
		switch {
		case about == "":
			fmt.Fprintf(&b, "  %s\n", synopsis)
		case len(synopsis) < column-2:
			fmt.Fprintf(&b, "  %-*s%s\n", column-2, synopsis, about)
		default:
			fmt.Fprintf(&b, "  %s\n  %*s%s\n", synopsis, column-2, "", about)
		}
	}
	var houston []string
	for _, c := range commands {
		if c.houston {
			houston = append(houston, c.name)
			continue
		}
		line(c.synopsis(), c.about)
	}
	line("gantry "+strings.Join(houston, "|")+"|...", "Houston's commands")
	b.WriteString(usageNotes)
	return b.String()
}

// synopsis is the command as its usage shows it: its arguments, then its
// flags.
func (c command) synopsis() string {
	s := "gantry " + c.name
	if c.args != "" {
		s += " " + c.args
	}
	if c.flags != nil {
		c.flags().VisitAll(func(f *flag.Flag) {
			if value, _ := flag.UnquoteUsage(f); value != "" {
				s += " [--" + f.Name + " " + value + "]"
			} else {
				s += " [--" + f.Name + "]"
			}
		})
	}
	return s
}

// lookup is the command args name ("g resource posts ..." is g resource's),
// and its arguments.
func lookup(args []string) (command, []string, bool) {
	name, rest := args[0], args[1:]
	if name == "generate" {
		name = "g"
	}
	if name == "g" && len(rest) > 0 {
		name, rest = "g "+rest[0], rest[1:]
	}
	for _, c := range commands {
		if c.name == name {
			return c, rest, true
		}
	}
	return command{}, nil, false
}

// flagSet is a command's flags, which run reports the errors of.
func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// usageError is a command given what it doesn't take: run says why, when
// there's more to say than its usage, then shows its usage.
type usageError struct{ why error }

func (e usageError) Unwrap() error { return e.why }

func (e usageError) Error() string {
	if e.why == nil {
		return "usage"
	}
	return "usage: " + e.why.Error()
}

// helpFlag asks a command for its usage where its flags aren't parsed:
// gantry new --help, gantry db -h. A word ("help") may be an app's name or
// its command's own argument, so only the flags are.
func helpFlag(arg string) bool { return arg == "-h" || arg == "--help" }

// parseFlags is fs.Parse, its errors a usage error.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	return nil
}

func run(args []string, root string, out, errOut io.Writer) int {
	if len(args) == 0 || helpArgs[args[0]] {
		fmt.Fprint(out, usage())
		return 0
	}
	if args[0] == "houston-release" {
		// What bin/gantry asks: the Houston release this gantry runs apps
		// with, and with --sha256 its CLIs' digests. Nothing else happens
		// first.
		if len(args) > 1 && args[1] == "--sha256" {
			fmt.Fprint(out, houstonSHA256SUMS)
			return 0
		}
		fmt.Fprintln(out, houstonRelease)
		return 0
	}
	if args[0] != "new" {
		refreshBinstub(root, errOut)
	}
	c, rest, ok := lookup(args)
	var err error
	switch {
	case ok && c.run != nil:
		err = c.run(root, rest, out, errOut)
	case args[0] == "g" || args[0] == "generate":
		fmt.Fprint(errOut, usage())
		return 2
	default:
		// Everything else is Houston's: gantry test is houston test.
		err = houston(root, args, errOut)
	}
	var exit errExit
	var wrong usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(out, "usage:", c.synopsis())
		return 0
	case errors.As(err, &wrong):
		if wrong.why != nil {
			fmt.Fprintln(errOut, "gantry:", wrong.why)
		}
		fmt.Fprintln(errOut, "usage:", c.synopsis())
		return 2
	case errors.As(err, &exit):
		return int(exit)
	}
	fmt.Fprintln(errOut, "gantry:", err)
	return 1
}
