package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"feed/config"
)

func TestRoutesJSON(t *testing.T) {
	t.Setenv("GANTRY_ENV", "test")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("DATABASE_URL", "")
	cfg := config.FromEnv()
	var out, errOut bytes.Buffer
	if code := command(t.Context(), cfg, slog.New(slog.DiscardHandler), []string{"routes", "--json"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("feed routes --json = %d\n%s", code, errOut.String())
	}
	var list []struct {
		Method  string   `json:"method"`
		Path    string   `json:"path"`
		Filters []string `json:"filters"`
	}
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("feed routes --json isn't JSON alone: %v\n%s", err, out.String())
	}
	filters := map[string]int{}
	for _, r := range list {
		filters[r.Method+" "+r.Path] = len(r.Filters)
	}
	for _, route := range []string{"GET /{$}", "GET /up", "GET /health", "GET /feed", "GET /posts/{id}", "POST /posts", "POST /posts/{id}/like", "GET /live"} {
		if _, ok := filters[route]; !ok {
			t.Errorf("feed routes --json has no %s:\n%s", route, out.String())
		}
	}
	if filters["POST /posts"] <= filters["GET /feed"] {
		t.Errorf("POST /posts has no filter GET /feed hasn't: the bearer token's\n%s", out.String())
	}
	if code := command(t.Context(), cfg, slog.New(slog.DiscardHandler), []string{"routes", "--yaml"}, nil, &out, &errOut); code == 0 {
		t.Error("feed routes --yaml succeeded")
	}
}
