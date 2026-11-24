package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/scttymn/gantry/mail"
)

func TestDevelopmentMail(t *testing.T) {
	t.Setenv("GANTRY_ENV", "development")
	a := newApp(t)
	var err error
	a.Mail, err = mail.Open(t.Context(), "development", t.TempDir(), mail.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Mail.Close() })
	if err := a.Mail.Send(t.Context(), mail.Message{To: "rider@example.com", Subject: "An invitation", Text: "See you Saturday"}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	w := get(t, h, "http://shop.localhost/dev/inbox")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "An invitation") {
		t.Fatalf("inbox: %d %s", w.Code, w.Body)
	}
	t.Setenv("GANTRY_ENV", "production")
	if w := get(t, h, "http://shop.localhost/dev/inbox"); w.Code != http.StatusNotFound {
		t.Fatal("production exposed the inbox")
	}
}
