package main

import (
	"io"
	"net/http"
	"strconv"
)

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>tiny</title>
<style>
html{color-scheme:light dark;font:16px/1.5 system-ui,sans-serif}body{display:grid;min-height:100vh;margin:0;place-items:center}main{text-align:center}h1{font-size:1.5rem;margin:0 0 .5rem}p{margin:0;opacity:.7}
</style>
<script src="/assets/js/main.js" defer></script>
</head>
<body>
<main>
<h1>tiny</h1>
<p id="status">Checking service…</p>
</main>
</body>
</html>
`

const mainJavaScript = `(() => {
  const status = document.querySelector("#status");
  fetch("/healthz", { cache: "no-store" })
    .then((response) => {
      if (!response.ok) throw new Error("unhealthy");
      status.textContent = "Service online";
    })
    .catch(() => {
      status.textContent = "Service unavailable";
    });
})();
`

func serveIndex(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", indexHTML)
}

func serveMainJavaScript(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", mainJavaScript)
}

func serveStatic(w http.ResponseWriter, r *http.Request, contentType, body string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}
