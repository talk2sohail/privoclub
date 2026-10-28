package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
)

// The root page is fully static: no request data is read, and it shows no
// build, tenant or configuration detail. Its one inline style is allowed by
// hash, so the policy needs no 'unsafe-inline'.
const rootStyle = `:root{color-scheme:light dark;--bg:#fff;--fg:#0a2540;--muted:#425466;--accent:#635bff}
@media (prefers-color-scheme:dark){:root{--bg:#0b1020;--fg:#e6ebf4;--muted:#9aa7bd;--accent:#8f89ff}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--fg);font:16px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:34rem}
.mark{margin:0 0 .75rem;font-weight:700;letter-spacing:.02em;color:var(--accent)}
h1{margin:0 0 1rem;font-size:clamp(1.6rem,4vw,2.1rem);line-height:1.2;letter-spacing:-.01em}
p{margin:0 0 1rem;color:var(--muted)}
a{color:var(--accent);font-weight:600}`

const rootHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex">
<title>Privo</title>
<style>` + rootStyle + `</style>
</head>
<body>
<main>
<p class="mark">Privo</p>
<h1>The team gateway to AI work and the MCP world.</h1>
<p>This address is a Privo gateway. It is meant for MCP clients and administrators, so there is nothing to see here.</p>
<p><a href="https://privo.club">Learn about Privo</a></p>
</main>
</body>
</html>
`

var rootCSP = func() string {
	sum := sha256.Sum256([]byte(rootStyle))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

// rootHandler serves the static page at exactly "/".
func rootHandler() http.Handler {
	body := []byte(rootHTML)
	length := strconv.Itoa(len(body))
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", length)
		h.Set("Content-Security-Policy", rootCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(body) // net/http drops the body for HEAD
	})
}
