// twelve-dollar: it serves by default, and its other commands are `twelve-dollar db
// ...`, `twelve-dollar jobs` and `twelve-dollar task NAME` (in development, `gantry db
// migrate` runs them in the app's container).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/images"

	"twelve-dollar/app"
	"twelve-dollar/assets"
	"twelve-dollar/config"
	"twelve-dollar/db/migrations"
	"twelve-dollar/db/seeds"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	os.Exit(command(ctx, config.FromEnv(), logger, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `usage:
  twelve-dollar                        serve (the default)
  twelve-dollar assets [DIR]           the assets precompiled, into DIR (assets/built; the build runs it)
  twelve-dollar db migrate|rollback [N]|status|seed|reset|console
  twelve-dollar tasks                  the app's own tasks (app/tasks.go)
  twelve-dollar task NAME [ARGS...]    run one
`

// command runs the command args name, and is its exit code.
func command(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string, in io.Reader, out, errOut io.Writer) int {
	name := "serve"
	if len(args) > 0 {
		name, args = args[0], args[1:]
	}
	var err error
	switch name {
	case "serve":
		err = serve(ctx, cfg, logger, nil)
	case "assets":
		// Rails' assets:precompile: the Dockerfile runs it before go build,
		// which embeds what it makes (the scripts minified, the pictures'
		// copies).
		dir := filepath.Join("assets", "built")
		if len(args) > 0 {
			dir = args[0]
		}
		if why := images.AVIFSlow(); why != "" {
			fmt.Fprintf(errOut, "assets: no AVIF copies, as %s: the encoder would take minutes a copy here; the pictures are WebP alone\n", why)
		}
		var made int
		made, err = assets.All.Precompile(ctx, dir)
		if err == nil {
			fmt.Fprintf(out, "assets: precompiled into %s (%d picture copies made)\n", dir, made)
		}
	case "db":
		return dbCommand(ctx, cfg, args, in, out, errOut)
	case "tasks":
		for _, t := range app.Tasks {
			fmt.Fprintf(out, "%-20s %s\n", t.Name, t.Help)
		}
		return 0
	case "task":
		err = task(ctx, cfg, logger, args, out)
	default:
		fmt.Fprint(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, "twelve-dollar:", err)
		return 1
	}
	return 0
}

// build opens the database and is the app on it. The app's own database is
// brought up to date and seeded; the challenge's (cfg.Given) arrives with its
// schema and data, which aren't the app's to change. close closes it.
func build(ctx context.Context, cfg config.Config, logger *slog.Logger) (a *app.App, close func(), err error) {
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if !cfg.Given {
		if err := errors.Join(migrations.Up(ctx, database), seeds.Run(ctx, database)); err != nil {
			database.Close()
			return nil, nil, err
		}
	}
	a = &app.App{DB: database, Log: logger, Secret: []byte(cfg.JWTSecret), Started: time.Now()}
	return a, func() { database.Close() }, nil
}

// serve builds the app, then serves until ctx ends. Anything that fails before listening is
// returned, so the process exits non-zero and its health check never
// passes. listening, when set, gets the address.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, listening chan<- string) error {
	a, closeDB, err := build(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeDB()
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	logger.Info("listening", "addr", ln.Addr().String(), "env", cfg.Env)
	if listening != nil {
		listening <- ln.Addr().String()
	}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(ln) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// task runs one of app.Tasks, on the migrated database.
func task(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("which task? `twelve-dollar tasks` lists them")
	}
	for _, t := range app.Tasks {
		if t.Name != args[0] {
			continue
		}
		database, err := db.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer database.Close()
		if err := migrations.Up(ctx, database); err != nil {
			return err
		}
		return t.Run(ctx, app.TaskEnv{DB: database, Log: logger, Out: out}, args[1:])
	}
	return fmt.Errorf("no task %q: `twelve-dollar tasks` lists them", args[0])
}
