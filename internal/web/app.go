package web

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// The admin app (ADR 0019): a Preact single-page app, built into web/dist and
// embedded. This file serves it and is its only door to the admin API.

const (
	// appCSP allows the app's own script and nothing else: no inline script or
	// style, no eval, no other origin.
	appCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; " +
		"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	// csrfHeader carries the session's CSRF token on every bridge request. A
	// form on another site cannot set it.
	csrfHeader = "X-Privo-CSRF"
	// maxAPIBody bounds a request body through the bridge, as the API does.
	maxAPIBody = 64 << 10
	// immutable is the cache policy of content-hashed asset files.
	immutable = "public, max-age=31536000, immutable"
)

// appPages are the paths the app answers. Navigation inside the app never
// loads a page; these are for a first visit, a reload or a link.
var appPages = []string{
	"/admin/start", "/admin/servers", "/admin/servers/{alias}", "/admin/rules", "/admin/people", "/admin/teams",
	"/admin/tokens", "/admin/clients", "/admin/activity", "/admin/approvals", "/admin/connections",
}

var assetNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type appAsset struct {
	body, gz []byte
	ctype    string
}

// app is the loaded build: the shell and the hashed assets, gzipped once.
type app struct {
	index  []byte
	assets map[string]appAsset
}

// loadApp reads a build. It needs index.html and at least one script.
func loadApp(fsys fs.FS) (*app, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, err
	}
	a := &app{index: index, assets: map[string]appAsset{}}
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		return nil, err
	}
	script := false
	for _, e := range entries {
		if e.IsDir() || !assetNameRe.MatchString(e.Name()) {
			continue
		}
		body, err := fs.ReadFile(fsys, "assets/"+e.Name())
		if err != nil {
			return nil, err
		}
		ext := path.Ext(e.Name())
		ctype := mime.TypeByExtension(ext)
		switch ext {
		case ".js":
			ctype, script = "text/javascript; charset=utf-8", true
		case ".css":
			ctype = "text/css; charset=utf-8"
		}
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		as := appAsset{body: body, ctype: ctype}
		if ext == ".js" || ext == ".css" || ext == ".svg" {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = zw.Write(body)
			_ = zw.Close()
			if buf.Len() < len(body) {
				as.gz = buf.Bytes()
			}
		}
		a.assets[e.Name()] = as
	}
	if !script {
		return nil, errors.New("no script in assets/")
	}
	return a, nil
}

func (s *Server) appRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/app/assets/{file}", http.HandlerFunc(s.asset))
	for _, p := range appPages {
		mux.Handle("GET "+p, s.appWrap(s.appPage))
	}
	mux.Handle("GET /admin/api/session", s.appWrap(s.sessionInfo))
	mux.Handle("/admin/api/v1/{path...}", s.appWrap(s.bridge))
	if s.connections() {
		mux.Handle("GET /admin/api/me/connections", s.appWrap(s.myConnections))
		mux.Handle("DELETE /admin/api/me/connections/{alias}", s.appWrap(s.disconnectJSON))
	}
}

// appWrap is wrap with the app's CSP.
func (s *Server) appWrap(h http.HandlerFunc) http.Handler {
	return s.wrap(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", appCSP)
		h(w, r)
	})
}

// asset serves one hashed build file. It needs no session: the app holds no
// data, only code.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	a, ok := s.app.assets[r.PathValue("file")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("Cache-Control", immutable)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Vary", "Accept-Encoding")
	body := a.body
	if a.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	h.Set("Content-Length", fmt.Sprint(len(body)))
	_, _ = w.Write(body)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if enc, _, _ := strings.Cut(strings.TrimSpace(part), ";"); strings.EqualFold(enc, "gzip") {
			return true
		}
	}
	return false
}

// appPage answers the app's shell to a signed-in person.
func (s *Server) appPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.session(r); !ok {
		redirect(w, "/admin/signin")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.app.index)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// appAuth checks a request from the app: a session, and for every request the
// CSRF header; for a change also the origin.
func (s *Server) appAuth(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	sess, u, ok := s.session(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "not signed in")
		return u, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
		apiError(w, http.StatusForbidden, "this request did not come from Privo's own pages")
		return u, false
	}
	// Required on reads too: a link from another site can make a browser send
	// the Lax session cookie on a top-level GET, but not this header.
	got := r.Header.Get(csrfHeader)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
		apiError(w, http.StatusForbidden, "the page is out of date; reload it")
		return u, false
	}
	return u, true
}

type sessionAnswer struct {
	User struct {
		Name   string `json:"name"`
		Person string `json:"person"`
		Role   string `json:"role"`
	} `json:"user"`
	Can         map[string]bool `json:"can"`
	CSRF        string          `json:"csrf"`
	MCPURL      string          `json:"mcp_url"`
	InviteRoles []string        `json:"invite_roles"`
	ChangeRoles []string        `json:"change_roles"`
	TokenRoles  []string        `json:"token_roles"`
	Landing     string          `json:"landing"`
}

// sessionInfo tells the app who is signed in and what their role may do. It is
// the one call the app makes without the CSRF header: it is how it learns it.
// Another site cannot read the answer (no CORS).
func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.session(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	p := s.principal(u)
	var a sessionAnswer
	a.User.Name, a.User.Person, a.User.Role = u.DisplayName, u.Person, u.Role
	a.Can = map[string]bool{
		"decide":        p.Can(auth.PermDecide),
		"servers":       p.Can(auth.PermManageServers),
		"rules":         p.Can(auth.PermManagePolicy),
		"people":        p.Can(auth.PermManageMembers),
		"teams":         p.Can(auth.PermManageMembers),
		"manage_tokens": p.Can(auth.PermManageTokens),
		"use_gateway":   p.Can(auth.PermUseGateway),
		"activity":      p.Can(auth.PermReadAudit),
		"connections":   s.connections() && p.Can(auth.PermUseGateway),
		"oauth_servers": s.connections(),
	}
	a.CSRF, a.MCPURL = sess.CSRF, s.origin+"/mcp"
	a.InviteRoles, a.ChangeRoles = inviteRoles(u), changeRoles(u)
	a.TokenRoles = []string{store.RoleOwner, store.RoleAdmin, store.RoleAuditor, store.RoleApprover}
	if u.Role != store.RoleOwner {
		a.TokenRoles = a.TokenRoles[1:]
	}
	a.Landing = s.landing(u)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

// inviteRoles are the roles u may invite with; changeRoles the ones u may set.
// The API enforces both; the app only offers them.
func inviteRoles(u store.User) []string {
	if u.Role == store.RoleOwner {
		return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor, store.RoleAdmin}
	}
	return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor}
}

func changeRoles(u store.User) []string {
	if u.Role == store.RoleOwner {
		return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor, store.RoleAdmin, store.RoleOwner}
	}
	return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor}
}

// bridge runs /admin/api/v1/... as /api/v1/... in process, as the signed-in
// person (ADR 0019 point 4). The API does every check, rule and audit; this
// only authenticates the browser and passes the request on.
func (s *Server) bridge(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete:
	default:
		w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u, ok := s.appAuth(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAPIBody))
	if err != nil {
		apiError(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}
	var body any
	if len(bytes.TrimSpace(raw)) > 0 {
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			apiError(w, http.StatusUnsupportedMediaType, "send JSON")
			return
		}
		if !json.Valid(raw) {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		body = json.RawMessage(raw)
	}
	target := strings.TrimPrefix(r.URL.EscapedPath(), "/admin")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	code, out := s.o.API.Serve(r.Context(), s.principal(u), r.Method, target, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(out) //nolint:gosec // G705: the API's own JSON, served as application/json with nosniff and the app's CSP

}

// ---- one's own upstream connections (ADR 0017), for the app

type myConnection struct {
	Alias       string `json:"alias"`
	Issuer      string `json:"issuer"`
	Connected   bool   `json:"connected"`
	Scopes      string `json:"scopes,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
}

func (s *Server) myConnections(w http.ResponseWriter, r *http.Request) {
	u, ok := s.appAuth(w, r)
	if !ok {
		return
	}
	servers, err := s.oauthServers(r.Context(), u)
	var grants []store.Grant
	if err == nil {
		grants, err = s.o.Store.ListGrants(r.Context(), s.o.Org, u.ID)
	}
	if err != nil {
		s.log.Error("web: list connections", "err", err)
		apiError(w, http.StatusInternalServerError, "your connections could not be read; try again")
		return
	}
	byServer := map[string]store.Grant{}
	for _, g := range grants {
		byServer[g.ServerID] = g
	}
	out := []myConnection{}
	for _, srv := range servers {
		c := myConnection{Alias: srv.Alias, Issuer: hostOf(srv.Config.OAuth.Issuer)}
		if g, ok := byServer[srv.ID]; ok {
			c.Connected, c.Scopes = true, g.Scopes
			c.ConnectedAt, c.LastUsedAt = g.ConnectedAt.UTC().Format(timeJSON), g.LastUsedAt.UTC().Format(timeJSON)
		}
		out = append(out, c)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connections": out, "can_connect": s.principal(u).Can(auth.PermUseGateway)})
}

const timeJSON = "2006-01-02T15:04:05Z07:00"

func (s *Server) disconnectJSON(w http.ResponseWriter, r *http.Request) {
	u, ok := s.appAuth(w, r)
	if !ok {
		return
	}
	alias := r.PathValue("alias")
	if !aliasRe.MatchString(alias) {
		apiError(w, http.StatusNotFound, "that connection no longer exists")
		return
	}
	switch err := s.disconnectGrant(r, u, alias); {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "that connection no longer exists")
	case err != nil:
		s.log.Error("web: disconnect", "server", alias, "err", err)
		apiError(w, http.StatusInternalServerError, "something went wrong; try again")
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"disconnected": alias})
	}
}

// disconnectGrant deletes u's grant for a server and closes their pooled
// upstream session.
func (s *Server) disconnectGrant(r *http.Request, u store.User, alias string) error {
	srv, err := s.o.Store.ServerByAlias(r.Context(), s.o.Org, alias)
	if err != nil {
		return err
	}
	ev := s.event("upstream.disconnect:"+srv.Alias, u.Person, "user:"+u.ID, "allow", "ok")
	ev.Server = srv.Alias
	if err := s.o.Grants.Disconnect(r.Context(), s.o.Org, srv, u.ID, ev); err != nil {
		return err
	}
	s.o.Upstream.ClosePerson(srv.ID, u.ID)
	return nil
}
