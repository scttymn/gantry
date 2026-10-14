// Package config is the app's settings, from the environment: each has a
// default, so a blank one falls back.
package config

import (
	"net"
	"os"
)

// Config is what the environment sets.
type Config struct {
	Env         string      // GANTRY_ENV: development, test or production (the default)
	Addr        string      // ADDR, default :8080
	DataDir     string      // DATA_DIR: the database and uploads; default /data
	DatabaseURL string      // DATABASE_URL; default sqlite://DATA_DIR/twelve-dollar.sqlite3
	JWTSecret   string      // JWT_SECRET: what the API's bearer tokens are signed with (HS256)
	// Given: the database is the challenge's (SQLITE_PATH), its schema and data
	// fixed, so the app neither migrates nor seeds it.
	Given bool
}

// Development is true where the code is mounted and changes as you work:
// db migrate rewrites db/schema.sql there, and db reset is allowed.
func (c Config) Development() bool { return c.Env == "development" }

// Load reads the environment through getenv (os.Getenv, or a test's).
func Load(getenv func(string) string) Config {
	or := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		Env:       or("GANTRY_ENV", "production"),
		Addr:      or("ADDR", ":8080"),
		DataDir:   or("DATA_DIR", "/data"),
		JWTSecret: or("JWT_SECRET", "twelve-dollar-challenge"),
	}
	c.DatabaseURL = or("DATABASE_URL", "sqlite://"+c.DataDir+"/twelve-dollar.sqlite3")
	// The challenge's contract (its SPEC.md): the database at SQLITE_PATH,
	// served on HOST:PORT.
	if path := getenv("SQLITE_PATH"); path != "" {
		c.DatabaseURL, c.Given = "sqlite://"+path, true
	}
	if port := getenv("PORT"); port != "" {
		c.Addr = net.JoinHostPort(getenv("HOST"), port)
	}
	return c
}

// FromEnv is the process's settings.
func FromEnv() Config { return Load(os.Getenv) }
