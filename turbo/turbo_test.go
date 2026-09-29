package turbo

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func text(s string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, templ.EscapeString(s))
		return err
	})
}

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestActions(t *testing.T) {
	for _, c := range []struct {
		a    Action
		want string
	}{
		{Append("messages", text("hi")), `<turbo-stream action="append" target="messages"><template>hi</template></turbo-stream>`},
		{Prepend("messages", text("hi")), `<turbo-stream action="prepend" target="messages"><template>hi</template></turbo-stream>`},
		{Replace("message_1", text("<b>")), `<turbo-stream action="replace" target="message_1"><template>&lt;b&gt;</template></turbo-stream>`},
		{Update("count", text("3")), `<turbo-stream action="update" target="count"><template>3</template></turbo-stream>`},
		{Before("x", text("a")), `<turbo-stream action="before" target="x"><template>a</template></turbo-stream>`},
		{After("x", text("a")), `<turbo-stream action="after" target="x"><template>a</template></turbo-stream>`},
		{Remove("message_1"), `<turbo-stream action="remove" target="message_1"></turbo-stream>`},
		{Refresh(), `<turbo-stream action="refresh"></turbo-stream>`},
		{Action{Name: "refresh", RequestID: "abc-1"}, `<turbo-stream action="refresh" request-id="abc-1"></turbo-stream>`},
		{Action{Name: "replace", Targets: `.card[data-id="1"]`, Method: "morph", Content: text("c")},
			`<turbo-stream action="replace" targets=".card[data-id=&#34;1&#34;]" method="morph"><template>c</template></turbo-stream>`},
		{Action{Name: "remove", Target: `a"><script>`}, `<turbo-stream action="remove" target="a&#34;&gt;&lt;script&gt;"></turbo-stream>`},
	} {
		if got := render(t, c.a); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

func TestStream(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/messages", nil)
	if err := Stream(w, r, Append("messages", text("hi")), Remove("empty")); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/vnd.turbo-stream.html; charset=utf-8" {
		t.Errorf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
	want := `<turbo-stream action="append" target="messages"><template>hi</template></turbo-stream><turbo-stream action="remove" target="empty"></turbo-stream>`
	if w.Body.String() != want {
		t.Errorf("%s", w.Body)
	}
}

func TestRequests(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	if Accepts(r) || Frame(r) != "" {
		t.Error("a plain request")
	}
	// What Turbo sends with a form.
	r.Header.Set("Accept", "text/vnd.turbo-stream.html, text/html, application/xhtml+xml")
	r.Header.Set("Turbo-Frame", "new_message")
	r.Header.Set("X-Turbo-Request-Id", "abc-1")
	if !Accepts(r) || Frame(r) != "new_message" || RequestID(r) != "abc-1" {
		t.Error("Turbo's request")
	}
}
