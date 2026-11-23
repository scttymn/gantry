package mail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMessage(t *testing.T) {
	m := Message{From: "Example Studio <no-reply@example.com>", To: "owner@example.com", ReplyTo: "sam@example.org",
		Subject: "New website inquiry: Sam Lee (Yoga)", Text: "line one\nline — two\n"}
	var b bytes.Buffer
	if err := m.WriteTo(&b, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	raw := b.String()
	for _, want := range []string{
		`From: "Example Studio" <no-reply@example.com>`, "To: <owner@example.com>",
		"Reply-To: <sam@example.org>", "Subject: New website inquiry: Sam Lee (Yoga)",
		"Date: Wed, 07 Oct 2026 12:00:00 +0000", "Message-ID: <", "text/plain; charset=UTF-8", "line one\r\nline",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in\n%s", want, raw)
		}
	}
	t.Run("a non-ASCII subject is encoded", func(t *testing.T) {
		var b bytes.Buffer
		Message{From: "a@b.co", To: "x@y.co", Subject: "Café", Text: "x"}.WriteTo(&b, time.Now())
		if !strings.Contains(b.String(), "Subject: =?UTF-8?q?Caf=C3=A9?=") {
			t.Error(b.String())
		}
	})
	t.Run("HTML goes as an alternative to the text", func(t *testing.T) {
		var b bytes.Buffer
		Message{From: "a@b.co", To: "x@y.co", Subject: "s", Text: "plain", HTML: "<p>rich</p>"}.WriteTo(&b, time.Now())
		if !strings.Contains(b.String(), "multipart/alternative") || !strings.Contains(b.String(), "text/html") {
			t.Error(b.String())
		}
	})
	t.Run("a bad address is an error, not a message", func(t *testing.T) {
		var b bytes.Buffer
		if err := (Message{From: "a@b.co", To: "not an address", Text: "x"}).WriteTo(&b, time.Now()); err == nil {
			t.Error("no error")
		}
	})
}

func TestLog(t *testing.T) {
	var logs bytes.Buffer
	Log{Logger: slog.New(slog.NewTextHandler(&logs, nil))}.Send(context.Background(), Message{To: "x@y.co", Subject: "Hi"})
	if !strings.Contains(logs.String(), "not sent") || !strings.Contains(logs.String(), "x@y.co") {
		t.Error(logs.String())
	}
}

// plainSMTP serves one connection as an SMTP server that never offers
// STARTTLS, and reports whether a message was handed to it.
func plainSMTP(t *testing.T) (port int, delivered <-chan bool) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got := make(chan bool, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			got <- false
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		r, sent := bufio.NewReader(c), false
		defer func() { got <- sent }()
		fmt.Fprint(c, "220 plain ESMTP\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.Fields(line + " x")[0]) {
			case "EHLO":
				fmt.Fprint(c, "250-plain\r\n250 8BITMIME\r\n")
			case "DATA":
				fmt.Fprint(c, "354 go on\r\n")
				for line != ".\r\n" {
					if line, err = r.ReadString('\n'); err != nil {
						return
					}
				}
				sent = true
				fmt.Fprint(c, "250 queued\r\n")
			case "QUIT":
				fmt.Fprint(c, "221 bye\r\n")
				return
			default:
				fmt.Fprint(c, "250 ok\r\n")
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, got
}

var codeMail = Message{From: "app@example.com", To: "rider@example.com", Subject: "Your code", Text: "123456"}

// A server that doesn't offer STARTTLS, or an attacker who strips it, must not
// receive the message (and the login code or reset link in it) in plaintext.
func TestSMTPRequiresTLS(t *testing.T) {
	port, delivered := plainSMTP(t)
	if err := (SMTP{Host: "127.0.0.1", Port: port}).Send(t.Context(), codeMail); err == nil {
		t.Error("sent without TLS")
	}
	if <-delivered {
		t.Error("the server received the message in plaintext")
	}
}

// SMTP_TLS opts out of mandatory TLS explicitly; blank keeps it.
func TestSMTPTLSConfig(t *testing.T) {
	for setting, plaintext := range map[string]bool{"": false, "mandatory": false, "opportunistic": true, "none": true} {
		t.Run("SMTP_TLS="+setting, func(t *testing.T) {
			port, delivered := plainSMTP(t)
			cfg := LoadConfig(func(name string) string {
				return map[string]string{"MAIL_FROM": "app@example.com", "SMTP_HOST": "127.0.0.1", "SMTP_PORT": fmt.Sprint(port), "SMTP_TLS": setting}[name]
			})
			m, err := Open(t.Context(), "production", t.TempDir(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = m.Send(t.Context(), codeMail)
			if got := <-delivered; got != plaintext || (err == nil) != plaintext {
				t.Errorf("delivered in plaintext: %v, want %v (send error: %v)", got, plaintext, err)
			}
		})
	}
	bad := Config{Host: "smtp.example.com", From: "app@example.com", TLS: "off"}
	if _, err := Open(t.Context(), "production", t.TempDir(), bad, nil); err == nil {
		t.Error("SMTP_TLS=off accepted")
	}
	t.Setenv("GANTRY_ENV", "development")
	dev, err := Open(t.Context(), "development", t.TempDir(), bad, nil)
	if err != nil || dev.Inbox == nil {
		t.Fatal("the development inbox doesn't use SMTP_TLS", err)
	}
	dev.Close()
}

// Port 465 is implicit TLS: the client's first bytes are a TLS handshake, not
// a wait for the server's plaintext greeting.
func TestSMTPImplicitTLSOn465(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:465")
	if err != nil {
		t.Skip("port 465 is unavailable (bin/go runs the tests as root):", err)
	}
	defer l.Close()
	first := make(chan byte, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			first <- 0
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 1)
		c.Read(b)
		first <- b[0]
	}()
	(SMTP{Host: "127.0.0.1", Port: 465}).Send(t.Context(), codeMail)
	if b := <-first; b != 0x16 {
		t.Errorf("first byte %#x, want a TLS handshake record (0x16)", b)
	}
}
