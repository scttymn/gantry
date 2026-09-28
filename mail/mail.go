// Package mail sends an app's email. Message is what to send; a Sender
// sends it: SMTP in production (through wneessen/go-mail), and Log when no
// server is configured, which logs each message as not sent rather than
// losing it silently.
package mail

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Message is an email: plain text, with an HTML alternative when HTML is set.
type Message struct {
	From, To, ReplyTo, Subject string
	Text, HTML                 string
}

// Sender sends messages.
type Sender interface {
	Send(context.Context, Message) error
}

// SMTP sends through a server, over TLS when the server offers it (port 587's
// STARTTLS), authenticating with the strongest mechanism both sides support.
type SMTP struct {
	Host               string
	Port               int // 587 when zero
	Username, Password string
}

func (s SMTP) Send(ctx context.Context, m Message) error {
	msg, err := m.msg(time.Now())
	if err != nil {
		return err
	}
	opts := []gomail.Option{gomail.WithPort(cmp(s.Port, 587)), gomail.WithTLSPortPolicy(gomail.TLSOpportunistic), gomail.WithTimeout(15 * time.Second)}
	if s.Username != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover), gomail.WithUsername(s.Username), gomail.WithPassword(s.Password))
	}
	c, err := gomail.NewClient(s.Host, opts...)
	if err != nil {
		return err
	}
	return c.DialAndSendWithContext(ctx, msg)
}

func cmp(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

// Log stands in when there's no SMTP server: each message is logged as not
// sent, with who it was for and what it was about.
type Log struct{ Logger *slog.Logger }

func (l Log) Send(_ context.Context, m Message) error {
	l.Logger.Warn("[mail] no SMTP server configured; not sent", "to", m.To, "subject", m.Subject)
	return nil
}

func (m Message) msg(now time.Time) (*gomail.Msg, error) {
	msg := gomail.NewMsg()
	if err := msg.From(m.From); err != nil {
		return nil, err
	}
	if err := msg.To(m.To); err != nil {
		return nil, err
	}
	if m.ReplyTo != "" {
		if err := msg.ReplyTo(m.ReplyTo); err != nil {
			return nil, err
		}
	}
	msg.Subject(m.Subject)
	msg.SetDateWithValue(now)
	msg.SetMessageID()
	msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	if m.HTML != "" {
		msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	}
	return msg, nil
}

// WriteTo writes the message as it would be sent, for tests and previews.
func (m Message) WriteTo(w io.Writer, now time.Time) error {
	msg, err := m.msg(now)
	if err != nil {
		return err
	}
	_, err = msg.WriteTo(w)
	return err
}

// String is the message's summary, for logs.
func (m Message) String() string {
	return "to " + m.To + ": " + strconv.Quote(m.Subject)
}
