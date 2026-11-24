package testkit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"github.com/scttymn/gantry/web"
)

type fakeCDPScenario struct {
	jsError        string
	consoleError   string
	failedAsset    string
	loadingFailure string
	selectorFound  *bool
	docStatus      int
}

type fakeCDP struct {
	mu           sync.Mutex
	cookies      []string
	extraHeaders []map[string]string
	viewports    []string
	navigated    []string
	scenario     fakeCDPScenario
}

func startFakeCDP(t *testing.T, sc fakeCDPScenario) (*httptest.Server, *fakeCDP) {
	t.Helper()
	fake := &fakeCDP{scenario: sc}
	pngBytes := []byte("\x89PNG\r\n\x1a\nfake-png")
	pngB64 := base64.StdEncoding.EncodeToString(pngBytes)

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools/browser"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": wsURL})
	})
	mux.Handle("/devtools/browser", websocket.Handler(func(ws *websocket.Conn) {
		var sendMu sync.Mutex
		send := func(msg any) {
			b, _ := json.Marshal(msg)
			sendMu.Lock()
			_ = websocket.Message.Send(ws, string(b))
			sendMu.Unlock()
		}
		var targetSeq int
		for {
			var raw []byte
			if err := websocket.Message.Receive(ws, &raw); err != nil {
				return
			}
			var req struct {
				ID        int64           `json:"id"`
				SessionID string          `json:"sessionId"`
				Method    string          `json:"method"`
				Params    json.RawMessage `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil {
				continue
			}
			switch req.Method {
			case "Target.createBrowserContext":
				targetSeq++
				send(map[string]any{"id": req.ID, "result": map[string]string{"browserContextId": fmt.Sprintf("ctx-%d", targetSeq)}})
			case "Target.createTarget":
				targetSeq++
				send(map[string]any{"id": req.ID, "result": map[string]string{"targetId": fmt.Sprintf("tgt-%d", targetSeq)}})
			case "Target.attachToTarget":
				send(map[string]any{"id": req.ID, "result": map[string]string{"sessionId": fmt.Sprintf("sess-%d", targetSeq)}})
			case "Emulation.setDeviceMetricsOverride":
				var p struct {
					Width  int  `json:"width"`
					Height int  `json:"height"`
					Mobile bool `json:"mobile"`
				}
				json.Unmarshal(req.Params, &p)
				fake.mu.Lock()
				fake.viewports = append(fake.viewports, fmt.Sprintf("%dx%d:%v", p.Width, p.Height, p.Mobile))
				fake.mu.Unlock()
				send(map[string]any{"id": req.ID, "result": map[string]any{}})
			case "Network.setCookie":
				var p struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				}
				json.Unmarshal(req.Params, &p)
				fake.mu.Lock()
				fake.cookies = append(fake.cookies, p.Name+"="+p.Value)
				fake.mu.Unlock()
				send(map[string]any{"id": req.ID, "result": map[string]any{"success": true}})
			case "Network.setExtraHTTPHeaders":
				var p struct {
					Headers map[string]string `json:"headers"`
				}
				json.Unmarshal(req.Params, &p)
				fake.mu.Lock()
				fake.extraHeaders = append(fake.extraHeaders, p.Headers)
				fake.mu.Unlock()
				send(map[string]any{"id": req.ID, "result": map[string]any{}})
			case "Page.navigate":
				var p struct {
					URL string `json:"url"`
				}
				json.Unmarshal(req.Params, &p)
				fake.mu.Lock()
				fake.navigated = append(fake.navigated, p.URL)
				s := fake.scenario
				fake.mu.Unlock()
				send(map[string]any{"id": req.ID, "result": map[string]string{"frameId": "f1"}})
				status := s.docStatus
				if status == 0 {
					status = http.StatusOK
				}
				send(map[string]any{
					"sessionId": req.SessionID,
					"method":    "Network.responseReceived",
					"params": map[string]any{
						"type":     "Document",
						"response": map[string]any{"url": p.URL, "status": status},
					},
				})
				if s.failedAsset != "" {
					send(map[string]any{
						"sessionId": req.SessionID,
						"method":    "Network.responseReceived",
						"params": map[string]any{
							"type":     "Image",
							"response": map[string]any{"url": s.failedAsset, "status": 404},
						},
					})
				}
				if s.loadingFailure != "" {
					send(map[string]any{
						"sessionId": req.SessionID,
						"method":    "Network.requestWillBeSent",
						"params": map[string]any{
							"requestId": "req-9",
							"request":   map[string]any{"url": s.loadingFailure},
						},
					})
					send(map[string]any{
						"sessionId": req.SessionID,
						"method":    "Network.loadingFailed",
						"params": map[string]any{
							"requestId": "req-9",
							"errorText": "net::ERR_CONNECTION_REFUSED",
							"canceled":  false,
						},
					})
				}
				if s.jsError != "" {
					send(map[string]any{
						"sessionId": req.SessionID,
						"method":    "Runtime.exceptionThrown",
						"params": map[string]any{
							"exceptionDetails": map[string]any{
								"text":      "Uncaught",
								"exception": map[string]any{"description": s.jsError},
							},
						},
					})
				}
				if s.consoleError != "" {
					send(map[string]any{
						"sessionId": req.SessionID,
						"method":    "Runtime.consoleAPICalled",
						"params": map[string]any{
							"type": "error",
							"args": []map[string]any{{"value": s.consoleError}},
						},
					})
				}
				send(map[string]any{"sessionId": req.SessionID, "method": "Page.loadEventFired", "params": map[string]any{}})
			case "Runtime.evaluate":
				var p struct {
					Expression string `json:"expression"`
				}
				json.Unmarshal(req.Params, &p)
				val := true
				if strings.HasPrefix(p.Expression, "Boolean(document.querySelector(") {
					fake.mu.Lock()
					if fake.scenario.selectorFound != nil {
						val = *fake.scenario.selectorFound
					}
					fake.mu.Unlock()
				}
				send(map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"value": val}}})
			case "Page.captureScreenshot":
				send(map[string]any{"id": req.ID, "result": map[string]string{"data": pngB64}})
			default:
				send(map[string]any{"id": req.ID, "result": map[string]any{}})
			}
		}
	}))
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fake
}

func TestVerifyBrowserPassingAndArtifacts(t *testing.T) {
	cdpSrv, fake := startFakeCDP(t, fakeCDPScenario{})
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	rt := club(&web.PageCache{})
	fmap := features(t, `
features:
  - name: home
    routes: ["GET /{$}", GET /up, GET /login, /news]
    role: public
    visit:
      - path: /
        selector: h1
  - name: account
    routes: [/account/, /admin/]
    role: signed-in
    visit:
      - path: /account/
        selector: main h1
`)
	signIn := Roles{
		SignedIn: func(v *Visitor, n int) string {
			v.Header.Set("X-Role", "member")
			v.Get("/login?as=member0")
			return "member0"
		},
	}
	manifest := VerifyBrowser(t, rt.Handler(), fmap, signIn, BrowserOptions{
		CDPURL:       cdpSrv.URL,
		ArtifactsDir: artifacts,
	})
	if !manifest.Passed || manifest.Failures != 0 {
		t.Fatalf("manifest failed: %+v", manifest)
	}
	if len(manifest.Checks) != 6 {
		t.Fatalf("checks = %d, want 6 (home as public+signed-in, account as signed-in, x 2 viewports)", len(manifest.Checks))
	}
	for _, c := range manifest.Checks {
		if !c.Passed || c.Screenshot == "" {
			t.Errorf("check: %+v", c)
		}
		b, err := os.ReadFile(filepath.Join(artifacts, filepath.FromSlash(c.Screenshot)))
		if err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) {
			t.Errorf("screenshot %s: %v", c.Screenshot, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(artifacts, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var decoded BrowserManifest
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.Passed || len(decoded.Checks) != 6 {
		t.Errorf("decoded manifest: %+v, err %v", decoded, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cookies) == 0 || fake.cookies[0] != "session=member0" {
		t.Errorf("cookies transferred to CDP: %q", fake.cookies)
	}
	if len(fake.extraHeaders) == 0 || fake.extraHeaders[0]["X-Role"] != "member" {
		t.Errorf("extra headers transferred to CDP: %+v", fake.extraHeaders)
	}
}

func TestVerifyBrowserCatchesFailures(t *testing.T) {
	missing := false
	for name, tc := range map[string]struct {
		sc   fakeCDPScenario
		want string
	}{
		"JS exception":     {fakeCDPScenario{jsError: "ReferenceError: boom"}, "JS exception on /: ReferenceError: boom"},
		"console.error":    {fakeCDPScenario{consoleError: "bad state"}, "console.error on /: bad state"},
		"broken asset 404": {fakeCDPScenario{failedAsset: "http://127.0.0.1/missing.png"}, "failed request on /: 404 http://127.0.0.1/missing.png"},
		"loading failed":   {fakeCDPScenario{loadingFailure: "http://127.0.0.1/app.js"}, "failed request on /: http://127.0.0.1/app.js (net::ERR_CONNECTION_REFUSED)"},
		"missing selector": {fakeCDPScenario{selectorFound: &missing}, `GET / has no "h1"`},
		"wrong status":     {fakeCDPScenario{docStatus: 500}, "GET / = 500, want 200"},
	} {
		t.Run(name, func(t *testing.T) {
			cdpSrv, _ := startFakeCDP(t, tc.sc)
			fmap := features(t, "features:\n  - name: home\n    routes: [\"GET /{$}\"]\n    role: public\n    visit: [{path: /, selector: h1}]\n")
			rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
			rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error { return nil })
			var manifest BrowserManifest
			r := failures(t, func(tb testing.TB) {
				manifest = VerifyBrowser(tb, rt.Handler(), fmap, Roles{}, BrowserOptions{
					CDPURL:    cdpSrv.URL,
					Viewports: []Viewport{{Name: "desktop", Width: 1280, Height: 800}},
				})
			})
			got := strings.Join(r.errors, "\n")
			if manifest.Passed || manifest.Failures != 1 || !strings.Contains(got, tc.want) {
				t.Errorf("manifest=%+v, errors=%q, want %q", manifest, got, tc.want)
			}
		})
	}

	t.Run("refuses GANTRY_ENV=development", func(t *testing.T) {
		t.Setenv("GANTRY_ENV", "development")
		fmap := features(t, "features:\n  - name: home\n    routes: [\"GET /{$}\"]\n    role: public\n")
		rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
		r := failures(t, func(tb testing.TB) {
			VerifyBrowser(tb, rt.Handler(), fmap, Roles{}, BrowserOptions{})
		})
		if !r.fatal || !strings.Contains(strings.Join(r.errors, "\n"), "GANTRY_ENV=development") {
			t.Errorf("expected fatal refusal under development: %+v", r)
		}
	})
}

func TestVerifyWithBrowserEnv(t *testing.T) {
	cdpSrv, _ := startFakeCDP(t, fakeCDPScenario{})
	artifacts := filepath.Join(t.TempDir(), "verify-out")
	var buf bytes.Buffer
	was := verifyJSONOut
	verifyJSONOut = &buf
	t.Cleanup(func() { verifyJSONOut = was })

	t.Setenv("GANTRY_VERIFY_JSON", "1")
	t.Setenv("GANTRY_VERIFY_BROWSER", "1")
	t.Setenv("GANTRY_VERIFY_ARTIFACTS", artifacts)
	t.Setenv("GANTRY_CDP_URL", cdpSrv.URL)

	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!DOCTYPE html><h1>Shop</h1>")
		return nil
	})
	fmap := features(t, "features:\n  - name: home\n    routes: [\"GET /{$}\", GET /up]\n    role: public\n    visit: [{path: /, selector: h1}]\n")
	report := Verify(t, rt.Handler(), rt.Routes(), fmap, Roles{})
	if !report.Passed || report.Browser == nil || !report.Browser.Passed || len(report.Browser.Checks) != 2 {
		t.Fatalf("report: %+v", report)
	}
	line, ok := strings.CutPrefix(strings.TrimSpace(buf.String()), VerifyJSONPrefix)
	if !ok {
		t.Fatalf("missing JSON prefix in %q", buf.String())
	}
	var decoded VerifyReport
	if err := json.Unmarshal([]byte(line), &decoded); err != nil || decoded.Browser == nil || len(decoded.Browser.Checks) != 2 {
		t.Errorf("decoded %+v, err %v", decoded, err)
	}
}

func TestVerifyBrowserRealChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Chrome test under -short")
	}
	chromeBin, err := findChrome("")
	if err != nil {
		t.Skip("no local Chrome/Chromium installed:", err)
	}
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Shop</h1><script>document.body.setAttribute("data-js", "ok")</script></body></html>`)
		return nil
	})
	rt.Handle("GET /boom", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Boom</h1><script>throw new Error("deliberate boom")</script></body></html>`)
		return nil
	})
	rt.Handle("GET /broken-asset", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Broken</h1><img src="/nope.png"></body></html>`)
		return nil
	})

	h := rt.Handler()
	artifacts := filepath.Join(t.TempDir(), "chrome-artifacts")
	okMap := features(t, "features:\n  - name: home\n    routes: [\"GET /{$}\"]\n    role: public\n    visit: [{path: /, selector: \"body[data-js='ok'] h1\"}]\n")
	manifest := VerifyBrowser(t, h, okMap, Roles{}, BrowserOptions{
		ChromePath:   chromeBin,
		ArtifactsDir: artifacts,
	})
	if !manifest.Passed || len(manifest.Checks) != 2 {
		t.Fatalf("real Chrome passing run: %+v", manifest)
	}
	for _, c := range manifest.Checks {
		b, err := os.ReadFile(filepath.Join(artifacts, filepath.FromSlash(c.Screenshot)))
		if err != nil || !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
			t.Errorf("real Chrome screenshot %s: err=%v len=%d", c.Screenshot, err, len(b))
		}
	}

	badMap := features(t, "features:\n  - name: broken\n    routes: [GET /boom, GET /broken-asset]\n    role: public\n    visit:\n      - path: /boom\n        selector: h1\n      - path: /broken-asset\n        selector: h1\n      - path: /\n        selector: .missing-el\n")
	var badManifest BrowserManifest
	r := failures(t, func(tb testing.TB) {
		badManifest = VerifyBrowser(tb, h, badMap, Roles{}, BrowserOptions{
			ChromePath: chromeBin,
			Viewports:  []Viewport{{Name: "desktop", Width: 1280, Height: 800}},
		})
	})
	got := strings.Join(r.errors, "\n")
	for _, want := range []string{"deliberate boom", "/nope.png", `has no ".missing-el"`} {
		if badManifest.Passed || !strings.Contains(got, want) {
			t.Errorf("real Chrome did not catch %q:\n%s", want, got)
		}
	}
}
