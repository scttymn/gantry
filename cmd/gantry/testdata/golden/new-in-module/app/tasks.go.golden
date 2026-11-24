package app

import (
	"context"
	"io"
	"log/slog"

	"github.com/scttymn/gantry/db"
)

var Tasks = []Task{}

type Task struct {
	Name string
	Help string
	Run  func(ctx context.Context, env TaskEnv, args []string) error
}

type TaskEnv struct {
	DB  *db.DB
	Log *slog.Logger
	Out io.Writer
}
