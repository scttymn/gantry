package mail

import (
	"bytes"
	"context"
	"log/slog"
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
