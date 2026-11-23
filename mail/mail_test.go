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
		Message{From: "a@example.com", To: "x@example.org", Subject: "Café", Text: "x"}.WriteTo(&b, time.Now())
		if !strings.Contains(b.String(), "Subject: =?UTF-8?q?Caf=C3=A9?=") {
			t.Error(b.String())
		}
	})
	t.Run("HTML goes as an alternative to the text", func(t *testing.T) {
		var b bytes.Buffer
		Message{From: "a@example.com", To: "x@example.org", Subject: "s", Text: "plain", HTML: "<p>rich</p>"}.WriteTo(&b, time.Now())
		if !strings.Contains(b.String(), "multipart/alternative") || !strings.Contains(b.String(), "text/html") {
			t.Error(b.String())
		}
	})
	t.Run("a bad address is an error, not a message", func(t *testing.T) {
		var b bytes.Buffer
		if err := (Message{From: "a@example.com", To: "not an address", Text: "x"}).WriteTo(&b, time.Now()); err == nil {
			t.Error("no error")
		}
	})
}

func TestLog(t *testing.T) {
	var logs bytes.Buffer
	Log{Logger: slog.New(slog.NewTextHandler(&logs, nil))}.Send(context.Background(), Message{To: "x@example.org", Subject: "Hi"})
	if !strings.Contains(logs.String(), "not sent") || !strings.Contains(logs.String(), "x@example.org") {
		t.Error(logs.String())
	}
}

// smtpServer serves one connection as an SMTP server, advertising STARTTLS
// when offerSTARTTLS is set, and reports what the client did: "tls" when it
// opened with a TLS handshake, "starttls" when it asked to upgrade,
// "plaintext" when it handed over a message unencrypted, or "nothing".
func smtpServer(t *testing.T, offerSTARTTLS bool) (port int, saw <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	// A client that never dials must not leave the test waiting on Accept.
	l.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
	got := make(chan string, 1)
	go func() {
		result := "nothing"
		defer func() { got <- result }()
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		fmt.Fprint(c, "220 test ESMTP\r\n")
		r := bufio.NewReader(c)
		if b, err := r.Peek(1); err == nil && b[0] == 0x16 {
			result = "tls"
			return
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.Fields(line + " x")[0]) {
			case "EHLO":
				if offerSTARTTLS {
					fmt.Fprint(c, "250-test\r\n250-STARTTLS\r\n250 8BITMIME\r\n")
				} else {
					fmt.Fprint(c, "250-test\r\n250 8BITMIME\r\n")
				}
			case "STARTTLS":
				result = "starttls"
				return
			case "DATA":
				fmt.Fprint(c, "354 go on\r\n")
				for line != ".\r\n" {
					if line, err = r.ReadString('\n'); err != nil {
						return
					}
				}
				result = "plaintext"
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
// receive the message (and the login code or reset link in it) in plaintext,
// including from a custom sender that leaves TLS unset.
func TestSMTPRequiresTLS(t *testing.T) {
	port, saw := smtpServer(t, false)
	if err := (SMTP{Host: "127.0.0.1", Port: port}).Send(t.Context(), codeMail); err == nil {
		t.Error("sent without TLS")
	}
	if got := <-saw; got != "nothing" {
		t.Errorf("the server saw %s", got)
	}
}

// SMTP_TLS opts out of mandatory TLS explicitly; blank keeps it. Port 465
// (implicitTLSPort) opens with TLS unless TLS is off.
func TestSMTPTLSConfig(t *testing.T) {
	for _, c := range []struct {
		setting         string
		offer, implicit bool
		want            string
	}{
		{setting: "", offer: true, want: "starttls"},
		{setting: "", want: "nothing"},
		{setting: "mandatory", offer: true, want: "starttls"},
		{setting: "mandatory", want: "nothing"},
		{setting: "opportunistic", offer: true, want: "starttls"},
		{setting: "opportunistic", want: "plaintext"},
		{setting: "none", offer: true, want: "plaintext"},
		{setting: "none", want: "plaintext"},
		{setting: "", implicit: true, want: "tls"},
		{setting: "opportunistic", implicit: true, want: "tls"},
		{setting: "none", implicit: true, want: "plaintext"},
	} {
		t.Run(fmt.Sprintf("SMTP_TLS=%s offer=%v implicit=%v", c.setting, c.offer, c.implicit), func(t *testing.T) {
			port, saw := smtpServer(t, c.offer)
			if c.implicit {
				defer func(p int) { implicitTLSPort = p }(implicitTLSPort)
				implicitTLSPort = port
			}
			cfg := LoadConfig(func(name string) string {
				return map[string]string{"MAIL_FROM": "app@example.com", "SMTP_HOST": "127.0.0.1", "SMTP_PORT": fmt.Sprint(port), "SMTP_TLS": c.setting}[name]
			})
			m, err := Open(t.Context(), "production", t.TempDir(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = m.Send(t.Context(), codeMail)
			if got := <-saw; got != c.want {
				t.Errorf("the server saw %s, want %s", got, c.want)
			}
			if (err == nil) != (c.want == "plaintext") {
				t.Errorf("send error: %v", err)
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
