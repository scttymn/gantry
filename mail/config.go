package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"path/filepath"
	"strconv"
)

// Config is app-wide mail configuration, shared by auth, jobs and controllers.
// LoadConfig reads MAIL_DRIVER, MAIL_FROM, SMTP_HOST, SMTP_PORT, SMTP_USERNAME,
// SMTP_PASSWORD and MAIL_INBOX_PATH. Open selects environment-safe defaults.
type Config struct {
	Driver, From                   string
	Host, Port, Username, Password string
	InboxPath                      string
}

func LoadConfig(getenv func(string) string) Config {
	return Config{Driver: getenv("MAIL_DRIVER"), From: getenv("MAIL_FROM"),
		Host: getenv("SMTP_HOST"), Port: getenv("SMTP_PORT"), Username: getenv("SMTP_USERNAME"), Password: getenv("SMTP_PASSWORD"),
		InboxPath: getenv("MAIL_INBOX_PATH")}
}

// Mailer is the app's one mail service. Send supplies the default From when a
// message omits it. Inbox is present only for the development capture driver.
type Mailer struct {
	From   string
	Inbox  *Inbox
	sender Sender
}

var ErrDisabled = errors.New("mail: delivery is disabled; configure the app's mail service")

// Open uses the development inbox by default in development, SMTP when a host
// is configured elsewhere, or a disabled sender. Captured messages use their
// own local database under dataDir, never the application's production tables.
func Open(ctx context.Context, environment, dataDir string, cfg Config, log *slog.Logger) (*Mailer, error) {
	driver := cfg.Driver
	if driver == "" {
		switch {
		case environment == "development":
			driver = "inbox"
		case environment == "test":
			driver = "disabled"
		case cfg.Host != "":
			driver = "smtp"
		default:
			driver = "disabled"
		}
	}
	m := &Mailer{From: cfg.From}
	switch driver {
	case "disabled":
		return m, nil
	case "inbox":
		if environment != "development" {
			return nil, errors.New("mail: the inbox is only available in development")
		}
		if m.From == "" {
			m.From = "Gantry <no-reply@example.test>"
		}
		path := cfg.InboxPath
		if path == "" {
			path = filepath.Join(dataDir, "mail-inbox.sqlite3")
		}
		inbox, err := NewInbox(ctx, path)
		if err != nil {
			return nil, err
		}
		m.Inbox, m.sender = inbox, inbox
	case "smtp":
		if cfg.Host == "" {
			return nil, errors.New("mail: SMTP_HOST is required")
		}
		port := 587
		if cfg.Port != "" {
			var err error
			port, err = strconv.Atoi(cfg.Port)
			if err != nil || port < 1 || port > 65535 {
				return nil, errors.New("mail: SMTP_PORT must be between 1 and 65535")
			}
		}
		m.sender = SMTP{Host: cfg.Host, Port: port, Username: cfg.Username, Password: cfg.Password}
	case "log":
		if log == nil {
			log = slog.Default()
		}
		m.sender = Log{Logger: log}
		return m, nil
	default:
		return nil, fmt.Errorf("mail: unknown MAIL_DRIVER %q (inbox, smtp, log or disabled)", driver)
	}
	if _, err := netmail.ParseAddress(m.From); err != nil {
		m.Close()
		return nil, errors.New("mail: MAIL_FROM must be a valid sender address")
	}
	return m, nil
}

// Ready reports whether this mailer delivers or captures complete messages.
// Disabled and summary-only logging cannot deliver an authentication code.
func (m *Mailer) Ready() bool {
	if m == nil || m.sender == nil {
		return false
	}
	_, logging := m.sender.(Log)
	return !logging
}

func (m *Mailer) Send(ctx context.Context, msg Message) error {
	if m == nil || m.sender == nil {
		return ErrDisabled
	}
	if msg.From == "" {
		msg.From = m.From
	}
	return m.sender.Send(ctx, msg)
}

// Close closes the inbox when present; SMTP has no persistent connection.
func (m *Mailer) Close() error {
	if m != nil && m.Inbox != nil {
		return m.Inbox.Close()
	}
	return nil
}
