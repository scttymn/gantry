"""The site, served from localhost: /<variant>.html is that variant of its home
page; anything else is fetched from the site and passed on, so fonts, photos and
scripts come from the same origin as the page, as they do live."""
import http.server, os, sys, urllib.request

SITE = sys.argv[1].rstrip("/")
PAGES = sys.argv[2]

class Mirror(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        page = os.path.join(PAGES, self.path.lstrip("/").split("?")[0])
        if self.path.endswith(".html") and os.path.isfile(page):
            body, ctype = open(page, "rb").read(), "text/html; charset=utf-8"
            self.send_response(200)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        req = urllib.request.Request(SITE + self.path, headers={"Accept": self.headers.get("Accept", "*/*"), "User-Agent": "lighthouse-mirror"})
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                body = r.read()
                self.send_response(r.status)
                for h in ("Content-Type", "Cache-Control"):
                    if r.headers.get(h):
                        self.send_header(h, r.headers[h])
        except urllib.error.HTTPError as e:
            body = e.read()
            self.send_response(e.code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

http.server.ThreadingHTTPServer(("127.0.0.1", 8080), Mirror).serve_forever()
