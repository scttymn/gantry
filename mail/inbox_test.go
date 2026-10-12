package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testInbox(t *testing.T) *Inbox {
	t.Helper()
	t.Setenv("GANTRY_ENV", "development")
	i, err := NewInbox(t.Context(), filepath.Join(t.TempDir(), "mail.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { i.Close() })
	return i
}
func exampleMail(subject string) Message {
	return Message{From: "Example <from@example.com>", To: "rider@example.com", Subject: subject, Text: "Full email body", HTML: "<p>Full email body</p>"}
}

func TestInboxPersistenceAndConcurrency(t *testing.T) {
	t.Setenv("GANTRY_ENV", "development")
	filename := filepath.Join(t.TempDir(), "mail.sqlite3")
	i, err := NewInbox(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewInbox(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := range 24 {
		wg.Go(func() {
			sender := i
			if n%2 == 0 {
				sender = second
			}
			if err := sender.Send(t.Context(), exampleMail(fmt.Sprint(n))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	i.Close()
	second.Close()
	reopened, err := NewInbox(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	messages, err := reopened.Messages(t.Context())
	if err != nil || len(messages) != 24 {
		t.Fatalf("retained messages: %d %v", len(messages), err)
	}
	for n, m := range messages {
		if m.Text != "Full email body" || m.HTML != "<p>Full email body</p>" || m.ReceivedAt.IsZero() {
			t.Fatal("message lost content")
		}
		if n > 0 && messages[n-1].ID <= m.ID {
			t.Fatal("not newest first")
		}
	}
	stat, err := os.Stat(filename)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("mailbox file isn't private")
	}
	for range inboxLimit {
		if err := reopened.Send(t.Context(), exampleMail("More mail")); err != nil {
			t.Fatal(err)
		}
	}
	messages, err = reopened.Messages(t.Context())
	if err != nil || len(messages) != inboxLimit || messages[len(messages)-1].ID != 25 {
		t.Fatal("retention did not remove oldest messages")
	}
}

func TestInboxValidation(t *testing.T) {
	i := testInbox(t)
	bad := exampleMail("bad")
	bad.To = "invalid"
	if err := i.Send(t.Context(), bad); err == nil {
		t.Fatal("invalid address accepted")
	}
	big := exampleMail("large")
	big.Text = strings.Repeat("x", inboxMaxBytes)
	if err := i.Send(t.Context(), big); err == nil {
		t.Fatal("oversized mail accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := i.Send(ctx, exampleMail("cancelled")); err == nil {
		t.Fatal("cancelled send accepted")
	}
	messages, _ := i.Messages(t.Context())
	if len(messages) != 0 {
		t.Fatal("invalid email stored")
	}
}

func TestInboxHTTP(t *testing.T) {
	i := testInbox(t)
	first := exampleMail("Welcome to Ride Club")
	first.ReplyTo = "support@example.com"
	first.Text = "Hello rider!"
	if err := i.Send(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	other := exampleMail("<script>secret</script>")
	other.To = "other@example.com"
	other.Text = "<img src=x onerror=alert(1)>"
	other.HTML = `<p>Hello <b>rider</b></p><script>alert(1)</script><meta http-equiv="refresh" content="0;url=https://tracker.invalid"><img src="https://tracker.invalid/pixel"><a href="https://tracker.invalid">Link</a><iframe srcdoc="bad"></iframe>`
	if err := i.Send(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	i.Routes(mux.Handle, "/dev/inbox")
	request := func(method, path, host, site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+path, nil)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	get := func(path string) *httptest.ResponseRecorder { return request("GET", path, "shop.localhost", "") }
	w := get("/dev/inbox")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "2 messages") || !strings.Contains(w.Body.String(), "Welcome to Ride Club") || strings.Contains(w.Body.String(), "<script>secret</script>") || strings.Contains(w.Body.String(), "<img src=x") {
		t.Fatalf("inbox escaping/list: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing privacy headers")
	}
	if got := get("/dev/inbox?q=welcome%3A").Body.String(); !strings.Contains(strings.ToLower(got), "q=welcome%3a") {
		t.Fatal("search query was double-escaped in navigation links")
	}
	if got := get("/dev/inbox?q=welcome").Body.String(); strings.Contains(got, "other@example.com") || !strings.Contains(got, "Hello rider!") {
		t.Fatal("search did not filter")
	}
	if got := get("/dev/inbox/1").Body.String(); !strings.Contains(got, "support@example.com") || !strings.Contains(got, "Hello rider!") {
		t.Fatal("selected message missing")
	}
	if got := get("/dev/inbox/2?view=html").Body.String(); !strings.Contains(got, `sandbox=""`) || !strings.Contains(got, `src="/dev/inbox/2/html"`) {
		t.Fatal("HTML preview not isolated")
	}
	w = get("/dev/inbox/2/html")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<b>rider</b>") || strings.Contains(w.Body.String(), "tracker.invalid") || strings.Contains(w.Body.String(), "<script") || strings.Contains(w.Body.String(), "<iframe") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("unsafe HTML preview", w.Body.String())
	}
	w = get("/dev/inbox/1/raw")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Body.String(), "Welcome to Ride Club") {
		t.Fatal("raw email unavailable")
	}
	for _, path := range []string{"/dev/inbox/999", "/dev/inbox/nope", "/dev/inbox/1/bad", "/dev/inbox/1/html/extra"} {
		if get(path).Code != 404 {
			t.Fatal("invalid path accepted", path)
		}
	}
	if request("GET", "/dev/inbox", "public.example.com", "").Code != 404 {
		t.Fatal("public host accepted")
	}
	if request("POST", "/dev/inbox/clear", "shop.localhost", "cross-site").Code != 403 {
		t.Fatal("cross-site delete accepted")
	}
	if w := request("POST", "/dev/inbox/1/delete", "shop.localhost", "same-origin"); w.Code != 303 {
		t.Fatal("delete failed")
	}
	if get("/dev/inbox/1").Code != 404 {
		t.Fatal("message not deleted")
	}
	request("POST", "/dev/inbox/clear", "shop.localhost", "same-origin")
	if got := get("/dev/inbox").Body.String(); !strings.Contains(got, "Your inbox is ready") || !strings.Contains(got, "0 messages") {
		t.Fatal("empty state missing")
	}
	t.Setenv("GANTRY_ENV", "production")
	if get("/dev/inbox").Code != 404 {
		t.Fatal("production exposed inbox")
	}
	if err := i.Send(t.Context(), exampleMail("production")); err == nil {
		t.Fatal("production captured mail")
	}
	if _, err := NewInbox(t.Context(), filepath.Join(t.TempDir(), "forbidden.sqlite3")); err == nil {
		t.Fatal("production opened inbox")
	}
}

func TestAppMailConfiguration(t *testing.T) {
	t.Setenv("GANTRY_ENV", "development")
	cfg := LoadConfig(func(name string) string {
		return map[string]string{"MAIL_FROM": "App <app@example.com>", "SMTP_HOST": "smtp.example.com", "SMTP_PORT": "587", "SMTP_USERNAME": "user", "SMTP_PASSWORD": "test-password"}[name]
	})
	dev, err := Open(t.Context(), "development", t.TempDir(), cfg, nil)
	if err != nil || dev.Inbox == nil || !dev.Ready() {
		t.Fatal("development must capture despite SMTP settings", err)
	}
	defer dev.Close()
	if err := dev.Send(t.Context(), Message{To: "rider@example.com", Subject: "An invitation", Text: "You are invited"}); err != nil {
		t.Fatal(err)
	}
	got, _ := dev.Inbox.Messages(t.Context())
	if len(got) != 1 || got[0].From != cfg.From {
		t.Fatal("shared default sender not applied")
	}
	production, err := Open(t.Context(), "production", t.TempDir(), cfg, nil)
	if err != nil || production.Inbox != nil || !production.Ready() {
		t.Fatal("production SMTP selection", err)
	}
	if _, ok := production.sender.(SMTP); !ok {
		t.Fatal("not SMTP")
	}
	test, err := Open(t.Context(), "test", t.TempDir(), cfg, nil)
	if err != nil || test.Ready() {
		t.Fatal("tests must not send real mail")
	}
	disabled, err := Open(t.Context(), "production", t.TempDir(), Config{}, nil)
	if err != nil || disabled.Ready() || disabled.Send(t.Context(), Message{}) != ErrDisabled {
		t.Fatal("unconfigured mail should be disabled")
	}
	for _, cfg := range []Config{{Driver: "inbox"}, {Driver: "other"}, {Driver: "smtp"}, {Driver: "smtp", Host: "smtp.example.com", From: "invalid"}, {Driver: "smtp", Host: "smtp.example.com", From: "a@example.com", Port: "not-a-port"}, {Driver: "smtp", Host: "smtp.example.com", From: "a@example.com", Port: "65536"}} {
		if _, err := Open(t.Context(), "production", t.TempDir(), cfg, nil); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

// The browser and a background worker can open the same capture file. Polling
// must see those external commits, preserve a reader, and recover after restart.
func TestInboxUpdates(t *testing.T) {
	t.Setenv("GANTRY_ENV", "development")
	filename := filepath.Join(t.TempDir(), "mail.sqlite3")
	inbox, err := NewInbox(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	worker, err := NewInbox(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	mux := http.NewServeMux()
	inbox.Routes(mux.Handle, "/dev/mail")
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "http://app.localhost/dev/mail"+path, nil))
		return w
	}
	read := func(path string) inboxUpdate {
		t.Helper()
		w := request(path)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("update: %d %s", w.Code, w.Body)
		}
		var update inboxUpdate
		if err := json.Unmarshal(w.Body.Bytes(), &update); err != nil {
			t.Fatal(err)
		}
		return update
	}
	empty := read("/updates")
	if empty.Total != 0 || empty.SelectedID != "0" {
		t.Fatal("initial snapshot", empty)
	}
	poll := "/updates?revision=" + url.QueryEscape(empty.Revision)
	if w := request(poll); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("unchanged snapshot returned content")
	}
	if err := worker.Send(t.Context(), exampleMail("Welcome")); err != nil {
		t.Fatal(err)
	}
	first := read(poll)
	if first.Total != 1 || first.SelectedID != "1" || first.Reader == nil || !strings.Contains(*first.Reader, "Full email body") {
		t.Fatal("first capture missing", first)
	}
	if first.Revision == empty.Revision {
		t.Fatal("external write was not detected")
	}
	if err := worker.Send(t.Context(), exampleMail("<script>New invite</script>")); err != nil {
		t.Fatal(err)
	}
	next := read("/updates?selected=1&view=html&revision=" + url.QueryEscape(first.Revision))
	if next.Total != 2 || next.SelectedID != "1" || next.Reader != nil || !strings.Contains(next.List, "&lt;script&gt;") || strings.Contains(next.List, "<script>") {
		t.Fatal("reader preservation or escaping failed", next)
	}
	filtered := read("/updates?selected=1&q=welcome")
	if !strings.Contains(filtered.List, "Welcome") || strings.Contains(filtered.List, "invite") || filtered.Reader != nil {
		t.Fatal("search changed", filtered)
	}
	if _, err := worker.database.Write.ExecContext(t.Context(), "DELETE FROM inbox_messages WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	removed := read("/updates?selected=1&view=html&revision=" + url.QueryEscape(next.Revision))
	if removed.SelectedID != "2" || removed.Reader == nil || !strings.Contains(*removed.Reader, `sandbox=""`) {
		t.Fatal("deleted selection not replaced", removed)
	}
	if _, err := worker.database.Write.ExecContext(t.Context(), "DELETE FROM inbox_messages"); err != nil {
		t.Fatal(err)
	}
	cleared := read("/updates?selected=2&revision=" + url.QueryEscape(removed.Revision))
	if cleared.Total != 0 || cleared.SelectedID != "0" || cleared.Reader == nil || !strings.Contains(*cleared.Reader, "Your inbox is ready") {
		t.Fatal("clear not reflected", cleared)
	}
	for _, value := range []string{"-1", "invalid", "999999999999999999999"} {
		if request("/updates?selected="+value).Code != 400 {
			t.Fatal("invalid selection accepted", value)
		}
	}
	script := inboxAssets.Path("inbox.js")
	w := request(script)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Body.String(), "visibilitychange") {
		t.Fatal("fingerprinted script unavailable")
	}
	page := request("")
	if !strings.Contains(page.Body.String(), "/dev/mail"+script) || !strings.Contains(page.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal("polling not wired into page")
	}
	// A restarted handler must not confuse a previous connection's counter for
	// its own, even when no new message has been received.
	replacement, err := NewInbox(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	revision, err := replacement.revision(t.Context())
	if err != nil || revision == cleared.Revision {
		t.Fatal("restart kept old revision", err)
	}
	for _, path := range []string{"/updates", script} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "http://public.example.com/dev/mail"+path, nil))
		if w.Code != 404 {
			t.Fatal("public inbox endpoint exposed", path)
		}
	}
	t.Setenv("GANTRY_ENV", "production")
	for _, path := range []string{"/updates", script} {
		if request(path).Code != 404 {
			t.Fatal("production inbox endpoint exposed", path)
		}
	}
}
