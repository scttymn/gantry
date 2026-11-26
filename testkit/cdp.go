package testkit

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/scttymn/gantry/internal/env"
)

// Viewport is a browser window size used by VerifyBrowser.
type Viewport struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mobile bool   `json:"mobile"`
}

// DefaultViewports are the desktop and mobile sizes VerifyBrowser checks when
// BrowserOptions.Viewports is empty.
var DefaultViewports = []Viewport{
	{Name: "desktop", Width: 1280, Height: 800, Mobile: false},
	{Name: "mobile", Width: 390, Height: 844, Mobile: true},
}

// BrowserOptions configures VerifyBrowser. Zero values use DefaultViewports,
// GANTRY_CDP_URL or a local Chrome/Chromium binary (GANTRY_CHROME_BIN or PATH),
// and GANTRY_VERIFY_ARTIFACTS when set.
type BrowserOptions struct {
	CDPURL       string
	ChromePath   string
	ArtifactsDir string
	Viewports    []Viewport
	Features     []string
}

// BrowserManifest is the manifest VerifyBrowser writes to
// <artifacts>/manifest.json and attaches to VerifyReport when
// GANTRY_VERIFY_BROWSER=1.
type BrowserManifest struct {
	Passed    bool                 `json:"passed"`
	Failures  int                  `json:"failures"`
	Viewports []Viewport           `json:"viewports"`
	Checks    []BrowserCheckResult `json:"checks"`
}

// BrowserCheckResult is one feature visit rendered in headless Chrome at one
// viewport for one role.
type BrowserCheckResult struct {
	Feature        string   `json:"feature"`
	Role           Role     `json:"role"`
	Path           string   `json:"path"`
	Viewport       string   `json:"viewport"`
	Status         int      `json:"status"`
	Passed         bool     `json:"passed"`
	Screenshot     string   `json:"screenshot,omitempty"`
	JSErrors       []string `json:"js_errors"`
	ConsoleErrors  []string `json:"console_errors"`
	FailedRequests []string `json:"failed_requests"`
	Errors         []string `json:"errors"`
}

// VerifyBrowser renders each permitted feature visit in test/features.yaml
// through headless Chrome via CDP at each viewport, failing on uncaught JS
// exceptions, console.error calls, failed subresource requests, unexpected
// document status, or missing selectors, and writes PNG screenshots plus
// manifest.json into opts.ArtifactsDir when configured.
func VerifyBrowser(t testing.TB, h http.Handler, path string, roles Roles, opts BrowserOptions) BrowserManifest {
	t.Helper()
	if env.Development() {
		t.Fatalf("testkit: VerifyBrowser refuses GANTRY_ENV=development: run it as GANTRY_ENV=test, as bin/gantry verify --browser does")
	}
	all := Features(t, path)
	selected, fromEnv := opts.Features, false
	if len(selected) == 0 {
		selected = parseVerifyFeatures(os.Getenv("GANTRY_VERIFY_FEATURES"))
		fromEnv = len(selected) > 0
	}
	features := filterFeatures(t, path, all, selected)
	if fromEnv {
		t.Logf("testkit: GANTRY_VERIFY_FEATURES chose %s, of the %d features in %s", strings.Join(selected, ", "), len(all), path)
	}
	viewports := opts.Viewports
	if len(viewports) == 0 {
		viewports = DefaultViewports
	}
	artifactsDir := opts.ArtifactsDir
	if artifactsDir == "" {
		artifactsDir = os.Getenv("GANTRY_VERIFY_ARTIFACTS")
	}
	if artifactsDir != "" {
		if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
			t.Fatalf("testkit: create artifacts dir %s: %v", artifactsDir, err)
		}
	}
	manifest := BrowserManifest{
		Passed:    true,
		Viewports: viewports,
		Checks:    []BrowserCheckResult{},
	}

	wsURL, stopChrome, err := resolveCDP(t, opts)
	if err != nil {
		t.Fatalf("testkit: browser verify: %v", err)
	}
	defer stopChrome()

	client, err := dialCDP(wsURL)
	if err != nil {
		t.Fatalf("testkit: connect CDP %s: %v", wsURL, err)
	}
	defer client.close()

	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, f := range features {
		for vi, visit := range f.Visit {
			for _, role := range allRoles {
				if rank(role) < rank(f.Role) {
					continue
				}
				if role != Public && roles[role] == nil {
					continue
				}
				var cookies []*http.Cookie
				var extraHeaders http.Header
				if roles[role] != nil {
					v := Browser(t, h)
					roles[role](v, 0)
					u, _ := url.Parse(origin)
					cookies = v.jar.Cookies(u)
					extraHeaders = v.Header.Clone()
				}
				for _, vp := range viewports {
					res := runBrowserCheck(t, client, srv.URL, f, vi, visit, role, cookies, extraHeaders, vp, artifactsDir)
					manifest.Checks = append(manifest.Checks, res)
					if !res.Passed {
						manifest.Passed = false
						manifest.Failures++
					}
				}
			}
		}
	}

	if artifactsDir != "" {
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Fatalf("testkit: marshal browser manifest: %v", err)
		}
		if err := os.WriteFile(filepath.Join(artifactsDir, "manifest.json"), append(data, '\n'), 0o644); err != nil {
			t.Fatalf("testkit: write browser manifest: %v", err)
		}
	}
	return manifest
}

func runBrowserCheck(
	t testing.TB,
	client *cdpClient,
	baseURL string,
	f Feature,
	visitIndex int,
	visit Visit,
	role Role,
	cookies []*http.Cookie,
	extraHeaders http.Header,
	vp Viewport,
	artifactsDir string,
) BrowserCheckResult {
	t.Helper()
	res := BrowserCheckResult{
		Feature:        f.Name,
		Role:           role,
		Path:           visit.Path,
		Viewport:       vp.Name,
		Passed:         true,
		JSErrors:       []string{},
		ConsoleErrors:  []string{},
		FailedRequests: []string{},
		Errors:         []string{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var browserCtxID string
	var bcResp struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := client.call(ctx, "", "Target.createBrowserContext", nil, &bcResp); err == nil {
		browserCtxID = bcResp.BrowserContextID
		defer func() {
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanCancel()
			_ = client.call(cleanCtx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": browserCtxID}, nil)
		}()
	}

	createParams := map[string]any{"url": "about:blank"}
	if browserCtxID != "" {
		createParams["browserContextId"] = browserCtxID
	}
	var targetResp struct {
		TargetID string `json:"targetId"`
	}
	if err := client.call(ctx, "", "Target.createTarget", createParams, &targetResp); err != nil {
		return failBrowserCheck(t, res, fmt.Sprintf("create target: %v", err))
	}
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		_ = client.call(cleanCtx, "", "Target.closeTarget", map[string]any{"targetId": targetResp.TargetID}, nil)
	}()

	var attachResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetResp.TargetID, "flatten": true}, &attachResp); err != nil {
		return failBrowserCheck(t, res, fmt.Sprintf("attach target: %v", err))
	}
	sid := attachResp.SessionID

	targetURL := baseURL + visit.Path
	var (
		mu             sync.Mutex
		docStatus      int
		jsErrors       []string
		consoleErrors  []string
		failedRequests []string
		requestURLs    = map[string]string{}
		loadFired      = make(chan struct{}, 1)
	)

	unsub := client.subscribe(sid, func(method string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "Page.loadEventFired":
			select {
			case loadFired <- struct{}{}:
			default:
			}
		case "Runtime.exceptionThrown":
			var ev struct {
				ExceptionDetails struct {
					Text      string `json:"text"`
					Exception struct {
						Description string `json:"description"`
					} `json:"exception"`
				} `json:"exceptionDetails"`
			}
			if json.Unmarshal(params, &ev) == nil {
				msg := strings.TrimSpace(ev.ExceptionDetails.Exception.Description)
				if msg == "" {
					msg = strings.TrimSpace(ev.ExceptionDetails.Text)
				}
				if msg == "" {
					msg = "uncaught JavaScript exception"
				}
				jsErrors = append(jsErrors, msg)
			}
		case "Runtime.consoleAPICalled":
			var ev struct {
				Type string `json:"type"`
				Args []struct {
					Value       any    `json:"value"`
					Description string `json:"description"`
				} `json:"args"`
			}
			if json.Unmarshal(params, &ev) == nil && ev.Type == "error" {
				var parts []string
				for _, a := range ev.Args {
					if a.Description != "" {
						parts = append(parts, a.Description)
					} else if a.Value != nil {
						parts = append(parts, fmt.Sprint(a.Value))
					}
				}
				msg := strings.Join(parts, " ")
				if msg == "" {
					msg = "console.error"
				}
				consoleErrors = append(consoleErrors, msg)
			}
		case "Network.requestWillBeSent":
			var ev struct {
				RequestID string `json:"requestId"`
				Request   struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if json.Unmarshal(params, &ev) == nil && ev.RequestID != "" {
				requestURLs[ev.RequestID] = ev.Request.URL
			}
		case "Network.responseReceived":
			var ev struct {
				Type     string `json:"type"`
				Response struct {
					URL    string `json:"url"`
					Status int    `json:"status"`
				} `json:"response"`
			}
			if json.Unmarshal(params, &ev) == nil {
				if ev.Type == "Document" && docStatus == 0 && strings.HasPrefix(ev.Response.URL, baseURL) {
					docStatus = ev.Response.Status
					return
				}
				if ev.Type != "Document" && ev.Response.Status >= 400 {
					if ev.Response.Status == http.StatusNotFound && strings.HasSuffix(ev.Response.URL, "/favicon.ico") {
						return
					}
					failedRequests = append(failedRequests, fmt.Sprintf("%d %s", ev.Response.Status, ev.Response.URL))
				}
			}
		case "Network.loadingFailed":
			var ev struct {
				RequestID string `json:"requestId"`
				ErrorText string `json:"errorText"`
				Canceled  bool   `json:"canceled"`
			}
			if json.Unmarshal(params, &ev) == nil && !ev.Canceled {
				u := requestURLs[ev.RequestID]
				if u == "" {
					u = ev.RequestID
				}
				failedRequests = append(failedRequests, fmt.Sprintf("%s (%s)", u, ev.ErrorText))
			}
		}
	})
	defer unsub()

	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if err := client.call(ctx, sid, method, nil, nil); err != nil {
			return failBrowserCheck(t, res, fmt.Sprintf("%s: %v", method, err))
		}
	}
	if err := client.call(ctx, sid, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             vp.Width,
		"height":            vp.Height,
		"deviceScaleFactor": 1,
		"mobile":            vp.Mobile,
	}, nil); err != nil {
		return failBrowserCheck(t, res, fmt.Sprintf("viewport %s: %v", vp.Name, err))
	}

	if len(cookies) > 0 {
		for _, c := range cookies {
			pathVal := c.Path
			if pathVal == "" {
				pathVal = "/"
			}
			_ = client.call(ctx, sid, "Network.setCookie", map[string]any{
				"name":  c.Name,
				"value": c.Value,
				"url":   baseURL + pathVal,
				"path":  pathVal,
			}, nil)
		}
	}
	if len(extraHeaders) > 0 {
		headers := map[string]string{}
		for k, vals := range extraHeaders {
			if len(vals) > 0 {
				headers[k] = strings.Join(vals, ", ")
			}
		}
		if len(headers) > 0 {
			_ = client.call(ctx, sid, "Network.setExtraHTTPHeaders", map[string]any{"headers": headers}, nil)
		}
	}

	if err := client.call(ctx, sid, "Page.navigate", map[string]any{"url": targetURL}, nil); err != nil {
		return failBrowserCheck(t, res, fmt.Sprintf("navigate %s: %v", visit.Path, err))
	}

	select {
	case <-loadFired:
	case <-ctx.Done():
		return failBrowserCheck(t, res, fmt.Sprintf("timed out waiting for %s to load", visit.Path))
	}

	_ = client.call(ctx, sid, "Runtime.evaluate", map[string]any{
		"expression":    `new Promise(r => setTimeout(r, 25))`,
		"awaitPromise":  true,
		"returnByValue": true,
	}, nil)

	if visit.Selector != "" {
		selJSON, _ := json.Marshal(visit.Selector)
		var evalResp struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		err := client.call(ctx, sid, "Runtime.evaluate", map[string]any{
			"expression":    fmt.Sprintf("Boolean(document.querySelector(%s))", selJSON),
			"returnByValue": true,
		}, &evalResp)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("selector %q check failed: %v", visit.Selector, err))
		} else if !evalResp.Result.Value {
			res.Errors = append(res.Errors, fmt.Sprintf("%s (%s, %s): GET %s has no %q", f.Name, role, vp.Name, visit.Path, visit.Selector))
		}
	}

	if artifactsDir != "" {
		var shotResp struct {
			Data string `json:"data"`
		}
		if err := client.call(ctx, sid, "Page.captureScreenshot", map[string]any{"format": "png"}, &shotResp); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("screenshot: %v", err))
		} else if raw, err := base64.StdEncoding.DecodeString(shotResp.Data); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("decode screenshot: %v", err))
		} else {
			rel := filepath.ToSlash(filepath.Join(artifactSlug(f.Name), fmt.Sprintf("%s-%s-%d.png", role, vp.Name, visitIndex)))
			dst := filepath.Join(artifactsDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("mkdir screenshot: %v", err))
			} else if err := os.WriteFile(dst, raw, 0o644); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("write screenshot: %v", err))
			} else {
				res.Screenshot = rel
			}
		}
	}

	mu.Lock()
	res.Status = docStatus
	if len(jsErrors) > 0 {
		res.JSErrors = jsErrors
	}
	if len(consoleErrors) > 0 {
		res.ConsoleErrors = consoleErrors
	}
	if len(failedRequests) > 0 {
		res.FailedRequests = failedRequests
	}
	mu.Unlock()

	wantStatus := visit.Status
	if wantStatus == 0 {
		wantStatus = http.StatusOK
	}
	if res.Status != 0 && res.Status != wantStatus {
		res.Errors = append(res.Errors, fmt.Sprintf("%s (%s, %s): GET %s = %d, want %d", f.Name, role, vp.Name, visit.Path, res.Status, wantStatus))
	}
	for _, msg := range res.JSErrors {
		res.Errors = append(res.Errors, fmt.Sprintf("%s (%s, %s): JS exception on %s: %s", f.Name, role, vp.Name, visit.Path, msg))
	}
	for _, msg := range res.ConsoleErrors {
		res.Errors = append(res.Errors, fmt.Sprintf("%s (%s, %s): console.error on %s: %s", f.Name, role, vp.Name, visit.Path, msg))
	}
	for _, msg := range res.FailedRequests {
		res.Errors = append(res.Errors, fmt.Sprintf("%s (%s, %s): failed request on %s: %s", f.Name, role, vp.Name, visit.Path, msg))
	}
	if len(res.Errors) > 0 {
		res.Passed = false
		for _, msg := range res.Errors {
			t.Errorf("%s", msg)
		}
	}
	return res
}

func failBrowserCheck(t testing.TB, res BrowserCheckResult, msg string) BrowserCheckResult {
	t.Helper()
	res.Passed = false
	res.Errors = append(res.Errors, msg)
	t.Errorf("%s (%s, %s): %s", res.Feature, res.Role, res.Viewport, msg)
	return res
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func artifactSlug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "feature"
	}
	return s
}

func resolveCDP(t testing.TB, opts BrowserOptions) (wsURL string, cleanup func(), err error) {
	t.Helper()
	cdpURL := strings.TrimSpace(opts.CDPURL)
	if cdpURL == "" {
		cdpURL = strings.TrimSpace(os.Getenv("GANTRY_CDP_URL"))
	}
	if cdpURL != "" {
		if strings.HasPrefix(cdpURL, "ws://") || strings.HasPrefix(cdpURL, "wss://") {
			return cdpURL, func() {}, nil
		}
		ws, err := discoverCDPWebSocket(cdpURL)
		if err != nil {
			return "", nil, err
		}
		return ws, func() {}, nil
	}
	bin, err := findChrome(opts.ChromePath)
	if err != nil {
		return "", nil, err
	}
	return startHeadlessChrome(t, bin)
}

func discoverCDPWebSocket(httpURL string) (string, error) {
	endpoint := strings.TrimRight(httpURL, "/") + "/json/version"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %d", endpoint, resp.StatusCode)
	}
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode %s: %w", endpoint, err)
	}
	if info.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("%s had no webSocketDebuggerUrl", endpoint)
	}
	return info.WebSocketDebuggerURL, nil
}

func findChrome(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if envBin := strings.TrimSpace(os.Getenv("GANTRY_CHROME_BIN")); envBin != "" {
		return envBin, nil
	}
	candidates := []string{"google-chrome", "chromium", "chromium-browser", "chrome"}
	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "darwin" {
		mac := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(mac); err == nil {
			return mac, nil
		}
	}
	return "", errors.New("no Chrome or Chromium found on PATH (set GANTRY_CHROME_BIN or GANTRY_CDP_URL)")
}

var devToolsListening = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

func startHeadlessChrome(t testing.TB, bin string) (string, func(), error) {
	t.Helper()
	profile := t.TempDir()
	cmd := exec.Command(
		bin,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--remote-debugging-port=0",
		"--remote-allow-origins=*",
		"--user-data-dir="+profile,
		"about:blank",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start %s: %w", bin, err)
	}
	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
	found := make(chan string, 1)
	var lines []string
	var linesMu sync.Mutex
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			linesMu.Lock()
			lines = append(lines, line)
			linesMu.Unlock()
			if m := devToolsListening.FindStringSubmatch(line); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
		close(found)
	}()
	select {
	case ws, ok := <-found:
		if !ok || ws == "" {
			stop()
			linesMu.Lock()
			out := strings.Join(lines, "\n")
			linesMu.Unlock()
			return "", nil, fmt.Errorf("chrome exited before reporting DevTools URL:\n%s", out)
		}
		return ws, stop, nil
	case <-time.After(15 * time.Second):
		stop()
		return "", nil, errors.New("timed out waiting for Chrome DevTools endpoint")
	}
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cdpClient struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan cdpMessage
	subs    map[string]func(method string, params json.RawMessage)
	closed  chan struct{}
}

func dialCDP(wsURL string) (*cdpClient, error) {
	cfg, err := websocket.NewConfig(wsURL, "http://127.0.0.1/")
	if err != nil {
		return nil, err
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	ws.MaxPayloadBytes = 64 << 20
	c := &cdpClient{
		ws:      ws,
		pending: map[int64]chan cdpMessage{},
		subs:    map[string]func(string, json.RawMessage){},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *cdpClient) close() {
	_ = c.ws.Close()
}

func (c *cdpClient) subscribe(sessionID string, fn func(method string, params json.RawMessage)) func() {
	c.mu.Lock()
	c.subs[sessionID] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.subs, sessionID)
		c.mu.Unlock()
	}
}

func (c *cdpClient) readLoop() {
	defer close(c.closed)
	for {
		var raw []byte
		if err := websocket.Message.Receive(c.ws, &raw); err != nil {
			c.mu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var msg cdpMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method != "" {
			c.mu.Lock()
			fn := c.subs[msg.SessionID]
			c.mu.Unlock()
			if fn != nil {
				fn(msg.Method, msg.Params)
			}
		}
	}
}

func (c *cdpClient) call(ctx context.Context, sessionID, method string, params any, result any) error {
	id := c.nextID.Add(1)
	req := map[string]any{"id": id, "method": method}
	if sessionID != "" {
		req["sessionId"] = sessionID
	}
	if params != nil {
		req["params"] = params
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ch := make(chan cdpMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	c.writeMu.Lock()
	err = websocket.Message.Send(c.ws, string(payload))
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return errors.New("CDP connection closed")
		}
		if msg.Error != nil {
			return fmt.Errorf("CDP %s (%d): %s", method, msg.Error.Code, msg.Error.Message)
		}
		if result != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.closed:
		return errors.New("CDP connection closed")
	}
}
