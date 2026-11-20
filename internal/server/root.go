package server

import (
	"crypto/sha256"
	"encoding/base64"
	"html"
	"net/http"
	"strconv"
)

// Home is what the root page shows. Both come from configuration, never from
// the request.
type Home struct {
	// SignIn is true when the admin site is set up: the page offers Sign in.
	SignIn bool
	// MCPURL is the gateway's MCP endpoint (PRIVO_PUBLIC_URL + /mcp), shown
	// for setting up AI clients; empty when no public URL is configured.
	MCPURL string
}

// The root page is static for a given configuration: no request data is read,
// and it shows no build, tenant or secret detail. Its one inline style is
// allowed by hash, so the policy needs no 'unsafe-inline'.
const rootStyle = `:root{color-scheme:light dark;--bg:#fff;--fg:#0a2540;--muted:#425466;--accent:#635bff;--card:#f6f9fc;--line:#e3e8ee}
@media (prefers-color-scheme:dark){:root{--bg:#0b1020;--fg:#e6ebf4;--muted:#9aa7bd;--accent:#8f89ff;--card:#131a2e;--line:#243049}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px 16px;background:var(--bg);color:var(--fg);font:16px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{width:100%;max-width:36rem}
.mark{margin:0 0 .75rem;font-weight:700;letter-spacing:.02em;color:var(--accent)}
h1{margin:0 0 1rem;font-size:clamp(1.6rem,4vw,2.1rem);line-height:1.2;letter-spacing:-.01em}
h2{margin:0 0 .5rem;font-size:1.05rem}
p{margin:0 0 1rem;color:var(--muted)}
a{color:var(--accent);font-weight:600}
.button{display:inline-block;padding:.65rem 1.4rem;border-radius:8px;background:var(--accent);color:#fff;text-decoration:none}
.button:focus-visible{outline:3px solid var(--fg);outline-offset:2px}
.card{margin:1.5rem 0;padding:1rem 1.25rem;border:1px solid var(--line);border-radius:10px;background:var(--card)}
.card p:last-child{margin-bottom:0}
code{font:.95em ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--fg);overflow-wrap:anywhere}
.foot{font-size:.9rem}`

// rootHTML renders the page for h. MCPURL is escaped although it is
// configuration.
func rootHTML(h Home) string {
	var signIn string
	if h.SignIn {
		signIn = `<p><a class="button" href="/admin/">Sign in</a></p>
<p>Sign in with the GitHub account you were invited with. Invited for the first time? Open the link in your invite instead.</p>`
	} else {
		signIn = `<p>Administration is not set up on this gateway yet: sign-in needs the GitHub settings described in the deploy guide.</p>`
	}
	endpoint := "the <code>/mcp</code> path of this address"
	if h.MCPURL != "" {
		endpoint = "<code>" + html.EscapeString(h.MCPURL) + "</code>"
	}
	return `<!doctype html>
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
<h1>Your team's gateway to AI tools.</h1>
<p>Privo sits between your team's AI clients and the MCP servers they use, with rules, approvals and an audit trail.</p>
` + signIn + `
<section class="card">
<h2>Connect an AI client</h2>
<p>AI clients use ` + endpoint + `. After you sign in, the Start page makes your token and shows the setup for Claude Code, Cursor, VS Code, Codex and others.</p>
</section>
<p class="foot"><a href="https://privo.club">About Privo</a></p>
</main>
</body>
</html>
`
}

var rootCSP = func() string {
	sum := sha256.Sum256([]byte(rootStyle))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

// rootHandler serves the page at exactly "/".
func rootHandler(h Home) http.Handler {
	body := []byte(rootHTML(h))
	length := strconv.Itoa(len(body))
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hdr := w.Header()
		hdr.Set("Content-Type", "text/html; charset=utf-8")
		hdr.Set("Content-Length", length)
		hdr.Set("Content-Security-Policy", rootCSP)
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("X-Robots-Tag", "noindex")
		hdr.Set("Cache-Control", "no-cache")
		_, _ = w.Write(body) // net/http drops the body for HEAD
	})
}
