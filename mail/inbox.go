package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/gantry/assets"
	"github.com/scttymn/gantry/db"
	"golang.org/x/net/html"
)

// Inbox is a development mail sender and mailbox UI. It captures every kind
// of Message without contacting an SMTP server. The newest 200 messages are
// persisted across development restarts, in a separate local SQLite file.
// NewInbox, Send and its HTTP routes require GANTRY_ENV=development.
type Inbox struct {
	database   *db.DB
	revisionMu sync.Mutex
	epoch      string
}

// Captured is a complete development message, with its capture time and ID.
type Captured struct {
	ID         int64
	ReceivedAt time.Time
	Message
}

const inboxLimit = 200
const inboxMaxBytes = 2 << 20

func NewInbox(ctx context.Context, filename string) (*Inbox, error) {
	if os.Getenv("GANTRY_ENV") != "development" {
		return nil, errors.New("mail: inbox requires GANTRY_ENV=development")
	}
	if filename == "" {
		return nil, errors.New("mail: inbox needs a local filename")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	d, err := db.Open(ctx, "sqlite://"+filename)
	if err != nil {
		return nil, err
	}
	_, err = d.Write.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS inbox_messages (
 id INTEGER PRIMARY KEY AUTOINCREMENT, received_at bigint NOT NULL,
 from_address text NOT NULL, to_address text NOT NULL, reply_to text NOT NULL,
 subject text NOT NULL, text_body text NOT NULL, html_body text NOT NULL
 )`)
	if err != nil {
		d.Close()
		return nil, err
	}
	return &Inbox{database: d, epoch: rand.Text()}, nil
}

func (i *Inbox) Close() error { return i.database.Close() }

func (i *Inbox) Send(ctx context.Context, m Message) error {
	if os.Getenv("GANTRY_ENV") != "development" {
		return errors.New("mail: inbox capture is disabled outside development")
	}
	if len(m.From)+len(m.To)+len(m.ReplyTo)+len(m.Subject)+len(m.Text)+len(m.HTML) > inboxMaxBytes {
		return errors.New("mail: development message exceeds 2 MiB")
	}
	if _, err := m.msg(time.Now()); err != nil {
		return err
	}
	return i.database.Tx(ctx, func(tx *db.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO inbox_messages(received_at,from_address,to_address,reply_to,subject,text_body,html_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, time.Now().UnixMilli(), m.From, m.To, m.ReplyTo, m.Subject, m.Text, m.HTML); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM inbox_messages WHERE id NOT IN (SELECT id FROM inbox_messages ORDER BY id DESC LIMIT $1)`, inboxLimit)
		return err
	})
}

// Messages returns the retained messages, newest first. Bodies contain the
// original email, including any development-only login codes or reset links.
func (i *Inbox) Messages(ctx context.Context) ([]Captured, error) {
	rows, err := i.database.Read.QueryContext(ctx, `SELECT id,received_at,from_address,to_address,reply_to,subject,text_body,html_body FROM inbox_messages ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Captured
	for rows.Next() {
		var m Captured
		var received int64
		if err := rows.Scan(&m.ID, &received, &m.From, &m.To, &m.ReplyTo, &m.Subject, &m.Text, &m.HTML); err != nil {
			return nil, err
		}
		m.ReceivedAt = time.UnixMilli(received).UTC()
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// Routes mounts a mailbox at prefix (normally /dev/inbox). Only development
// requests to localhost/loopback hosts can view or delete captured messages.
// HTML messages are sandboxed and remote content is blocked.
func (i *Inbox) Routes(mount func(string, http.Handler), prefix string) {
	prefix = strings.TrimRight(prefix, "/")
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "{}?# ") || prefix == "" {
		panic("mail: inbox needs an absolute route prefix")
	}
	h := http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if os.Getenv("GANTRY_ENV") != "development" || !localInboxHost(r.Host) {
			http.NotFound(w, r)
			return
		}
		if err := i.serve(w, r, prefix); err != nil {
			http.Error(w, "Unable to load the development inbox.", http.StatusInternalServerError)
		}
	}))
	mount(prefix, h)
	mount(prefix+"/", h)
}

func localInboxHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host).IsLoopback()
}

type inboxPage struct {
	Base, Query, SelectedURL string
	Revision, Script         string
	Messages                 []Captured
	Selected                 *Captured
	Total                    int
	HTML                     bool
	Detail                   bool
}

func (i *Inbox) serve(w http.ResponseWriter, r *http.Request, base string) error {
	suffix := strings.Trim(strings.TrimPrefix(r.URL.Path, base), "/")
	parts := strings.Split(suffix, "/")
	if r.Method == "POST" {
		var err error
		switch {
		case suffix == "clear":
			_, err = i.database.Write.ExecContext(r.Context(), `DELETE FROM inbox_messages`)
		case len(parts) == 2 && parts[1] == "delete":
			id, e := strconv.ParseInt(parts[0], 10, 64)
			if e != nil || id < 1 {
				http.NotFound(w, r)
				return nil
			}
			_, err = i.database.Write.ExecContext(r.Context(), `DELETE FROM inbox_messages WHERE id=$1`, id)
		default:
			http.NotFound(w, r)
			return nil
		}
		if err != nil {
			return err
		}
		http.Redirect(w, r, base, http.StatusSeeOther)
		return nil
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	if strings.HasPrefix(suffix, "assets/") {
		http.StripPrefix(base, inboxAssets.Handler()).ServeHTTP(w, r)
		return nil
	}
	if suffix == "updates" {
		return i.updates(w, r, base)
	}
	// Read the revision before the snapshot: a racing write is seen on the next poll.
	revision, err := i.revision(r.Context())
	if err != nil {
		return err
	}
	messages, err := i.Messages(r.Context())
	if err != nil {
		return err
	}
	var id int64
	if suffix != "" {
		id, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id < 1 || len(parts) > 2 {
			http.NotFound(w, r)
			return nil
		}
	}
	p := inboxSnapshot(base, r.URL.Query().Get("q"), r.URL.Query().Get("view"), id, messages)
	if id != 0 && (p.Selected == nil || p.Selected.ID != id) {
		http.NotFound(w, r)
		return nil
	}
	p.Detail = id != 0
	p.Revision = revision
	p.Script = base + inboxAssets.Path("inbox.js")
	if len(parts) == 2 && p.Selected != nil {
		switch parts[1] {
		case "html":
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; sandbox")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			body, err := inboxHTML(p.Selected.HTML)
			if err != nil {
				return err
			}
			_, err = w.Write(body)
			return err
		case "raw":
			var raw bytes.Buffer
			if err := p.Selected.Message.WriteTo(&raw, p.Selected.ReceivedAt); err != nil {
				return err
			}
			w.Header().Set("Content-Type", "message/rfc822")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="message-%d.eml"`, p.Selected.ID))
			_, err = w.Write(raw.Bytes())
			return err
		default:
			http.NotFound(w, r)
			return nil
		}
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return inboxView.Execute(w, p)
}

// revision uses Gantry's SQLite change tracking, including other processes' writes.
// Its epoch changes on restart or a failed read because a reopened version
// connection can start counting from the same number again.
func (i *Inbox) revision(ctx context.Context) (string, error) {
	i.revisionMu.Lock()
	defer i.revisionMu.Unlock()
	version, err := i.database.Version(ctx)
	if err != nil {
		i.epoch = rand.Text()
		return "", err
	}
	return fmt.Sprintf("%s:%d", i.epoch, version), nil
}

func inboxSnapshot(base, query, view string, selectedID int64, messages []Captured) inboxPage {
	p := inboxPage{Base: base, Query: strings.TrimSpace(query), Total: len(messages)}
	for _, m := range messages {
		if m.ID == selectedID {
			selected := m
			p.Selected = &selected
		}
		if p.Query == "" || strings.Contains(strings.ToLower(m.From+"\n"+m.To+"\n"+m.Subject+"\n"+m.Text+"\n"+m.HTML), strings.ToLower(p.Query)) {
			p.Messages = append(p.Messages, m)
		}
	}
	if p.Selected == nil && len(p.Messages) > 0 {
		selected := p.Messages[0]
		p.Selected = &selected
	}
	if p.Selected != nil {
		p.SelectedURL = fmt.Sprintf("%s/%d", base, p.Selected.ID)
		p.HTML = p.Selected.HTML != "" && (view == "html" || (view == "" && p.Selected.Text == ""))
	}
	return p
}

type inboxUpdate struct {
	Revision   string  `json:"revision"`
	Total      int     `json:"total"`
	List       string  `json:"list"`
	Reader     *string `json:"reader,omitempty"`
	SelectedID string  `json:"selectedID"`
}

func (i *Inbox) updates(w http.ResponseWriter, r *http.Request, base string) error {
	query := r.URL.Query()
	var selectedID int64
	if value := query.Get("selected"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 0 {
			http.Error(w, "Invalid selected message.", http.StatusBadRequest)
			return nil
		}
		selectedID = id
	}
	revision, err := i.revision(r.Context())
	if err != nil {
		return err
	}
	if query.Get("revision") == revision {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	messages, err := i.Messages(r.Context())
	if err != nil {
		return err
	}
	p := inboxSnapshot(base, query.Get("q"), query.Get("view"), selectedID, messages)
	update := inboxUpdate{Revision: revision, Total: p.Total, SelectedID: "0"}
	if p.Selected != nil {
		update.SelectedID = strconv.FormatInt(p.Selected.ID, 10)
	}
	var list bytes.Buffer
	if err := inboxView.ExecuteTemplate(&list, "list", p); err != nil {
		return err
	}
	update.List = list.String()
	// Leave the reader (including its HTML iframe) intact unless selection disappeared.
	if update.SelectedID != strconv.FormatInt(selectedID, 10) {
		var reader bytes.Buffer
		if err := inboxView.ExecuteTemplate(&reader, "reader", p); err != nil {
			return err
		}
		value := reader.String()
		update.Reader = &value
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(update)
}

//go:embed js
var inboxFiles embed.FS
var inboxAssets = assets.MustNew(inboxFiles)

// Remote resources, navigation, scripts and form controls have no place in a
// captured-email preview. The iframe and its CSP provide separate containment.
func inboxHTML(source string) ([]byte, error) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil, err
	}
	var clean func(*html.Node)
	clean = func(n *html.Node) {
		for child := n.FirstChild; child != nil; {
			next := child.NextSibling
			if child.Type == html.ElementNode && strings.Contains("|script|iframe|frame|frameset|object|embed|base|meta|link|form|input|button|textarea|select|audio|video|source|track|animate|set|", "|"+child.Data+"|") {
				n.RemoveChild(child)
			} else {
				clean(child)
			}
			child = next
		}
		attrs := n.Attr[:0]
		for _, a := range n.Attr {
			k := strings.ToLower(a.Key)
			if strings.HasPrefix(k, "on") || k == "href" || k == "srcdoc" || k == "action" || k == "formaction" || k == "srcset" || k == "background" {
				continue
			}
			if k == "src" && !(n.Data == "img" && strings.HasPrefix(strings.ToLower(a.Val), "data:image/")) {
				continue
			}
			attrs = append(attrs, a)
		}
		n.Attr = attrs
	}
	clean(doc)
	var b bytes.Buffer
	err = html.Render(&b, doc)
	return b.Bytes(), err
}

//go:embed inbox.html
var inboxMarkup string

var inboxView = template.Must(template.New("inbox").Funcs(template.FuncMap{
	"snippet": func(s string) string {
		r := []rune(strings.Join(strings.Fields(s), " "))
		if len(r) > 100 {
			return string(r[:100]) + "…"
		}
		return string(r)
	},
	"clock": func(t time.Time) string { return t.Format("15:04") },
	"date":  func(t time.Time) string { return t.Format("Jan 2, 2006 · 15:04:05 UTC") },
}).Parse(inboxMarkup))
