package config

import (
	"os"

	"github.com/scttymn/gantry/mail"
)

type Config struct {
	Mail         mail.Config
	Env          string
	Addr         string
	DataDir      string
	DatabaseURL  string
	SecretKey    string
	SiteURL      string
	JWTSecret    string
	JobsInServer bool
}

func (c Config) Development() bool { return c.Env == "development" }

func Load(getenv func(string) string) Config {
	or := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		Mail:         mail.LoadConfig(getenv),
		Env:          or("GANTRY_ENV", "production"),
		Addr:         or("ADDR", ":8080"),
		DataDir:      or("DATA_DIR", "/data"),
		SecretKey:    getenv("SECRET_KEY"),
		SiteURL:      getenv("SITE_URL"),
		JWTSecret:    or("JWT_SECRET", "twelve-dollar-challenge"),
		JobsInServer: getenv("JOBS_IN_SERVER") != "false",
	}
	c.DatabaseURL = or("DATABASE_URL", "sqlite://"+c.DataDir+"/feed.sqlite3")
	return c
}

func FromEnv() Config { return Load(os.Getenv) }
