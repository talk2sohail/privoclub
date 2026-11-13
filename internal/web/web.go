// Package web is Privo's admin website: sign-in with GitHub (ADR 0016), the
// invite and upstream connect pages (ADR 0017), and the admin app with its
// bridge to the admin API (ADR 0019).
//
// Everything here is built to fail closed. A session is a random secret in an
// HttpOnly cookie that the database knows only as a hash. Every state-changing
// request is checked for its origin and a per-session CSRF token. The
// server-rendered pages are served with a Content-Security-Policy that allows
// no script; the app's allows only its own, and it renders what an attacker
// controls (tool names, arguments, GitHub profile names) only as text.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

//go:embed templates/*.html assets/app.css
var assets embed.FS

// Defaults.
const (
	// StateTTL is how long a person has to finish signing in at GitHub, or
	// connecting an account at an upstream's authorization server.
	StateTTL = 10 * time.Minute

	defaultIdle   = 2 * time.Hour
	defaultMaxAge = 12 * time.Hour

	maxFormBytes = 8 << 10
	maxCodeLen   = 512

	// kindSession marks a principal that signed in on the website, not a token.
	kindSession = "session"
)

// Store is the store subset the website needs.
type Store interface {
	SetupLink(ctx context.Context, org store.OrgID, secretHash string, now time.Time) (store.SetupLink, error)
	ClaimSetup(ctx context.Context, org store.OrgID, c store.Claim, ev store.AuditEvent) (store.User, error)
	UserByIdentity(ctx context.Context, org store.OrgID, provider, subject string) (store.User, error)
	CreateSession(ctx context.Context, org store.OrgID, s store.Session, ev store.AuditEvent) error
	SessionByHash(ctx context.Context, org store.OrgID, hash string, now time.Time, idle time.Duration) (store.Session, store.User, error)
	TouchSession(ctx context.Context, org store.OrgID, hash string, now time.Time) error
	DeleteSession(ctx context.Context, org store.OrgID, hash string, ev store.AuditEvent) error
	PruneSignIn(ctx context.Context, org store.OrgID, now time.Time, idle time.Duration) error

	// Invites and upstream connections (ADR 0017).
	Invite(ctx context.Context, org store.OrgID, secretHash string, now time.Time) (store.Invite, error)
	ClaimInvite(ctx context.Context, org store.OrgID, c store.Claim, ev store.AuditEvent) (store.User, error)
	ListServers(ctx context.Context, org store.OrgID) ([]store.Server, error)
	ServerByAlias(ctx context.Context, org store.OrgID, alias string) (store.Server, error)
	ServerByID(ctx context.Context, org store.OrgID, id string) (store.Server, error)
	ListGrants(ctx context.Context, org store.OrgID, userID string) ([]store.Grant, error)
	// A connection in flight, written only for a signed-in member. A sign-in
	// in flight is not stored: see signinstate.go.
	CreateConnectState(ctx context.Context, org store.OrgID, st store.ConnectState, now time.Time) error
	UseConnectState(ctx context.Context, org store.OrgID, hash string, now time.Time) (store.ConnectState, error)
	// Teams (ADR 0020): which servers a person reaches.
	AccessFor(ctx context.Context, org store.OrgID, userID string) (store.Access, error)
}

// Auditor writes admin audit events.
type Auditor interface {
	Admin(ctx context.Context, org store.OrgID, ev store.AuditEvent) error
}

// API is the in-process admin API (control.API.Serve, ADR 0018 point 2).
type API interface {
	Serve(ctx context.Context, p auth.Principal, method, path string, body any) (int, []byte)
}

// Options configure a Server.
type Options struct {
	Store     Store
	Org       store.OrgID
	Audit     Auditor
	Logger    *slog.Logger
	Now       func() time.Time
	GitHub    *githubauth.Client
	PublicURL *url.URL
	// IdleTimeout and MaxAge bound a session (defaults 2h and 12h).
	IdleTimeout, MaxAge time.Duration
	// Keys seals the cookie of a sign-in in flight (ADR 0016 amendment 1).
	Keys Keys
	// Missing names what is not configured. A Server with anything missing
	// answers every request with 503 and says what to set.
	Missing []string

	// Upstream connections (ADR 0017); without all three the connect pages
	// are not served.
	Grants   Grants
	OAuth    *upstreamauth.Client
	Upstream Upstreams

	// API is the admin API in process and App the admin app's build
	// (ADR 0019). The site needs both.
	API API
	App fs.FS
}

// Server is the admin website.
type Server struct {
	o       Options
	enabled bool
	log     *slog.Logger
	now     func() time.Time
	tmpl    map[string]*template.Template
	app     *app

	origin          string // the public origin, as browsers send it in Origin
	secure          bool
	csp             string
	callback        string
	connectCallback string
	missing         []string
	// One cookie per flow, so two flows in one browser cannot end each other
	// and a callback only ever reads its own (F07 R-07): the session, the
	// sealed sign-in in flight, and the nonce of a connection in flight.
	sessionCookie, signInCookie, connectCookie string
	finished                                   finished
	denials                                    denials
}

// New builds a Server. It never fails: a missing setting disables the site and
// the disabled site says what is missing.
func New(o Options) *Server {
	s := &Server{o: o, log: o.Logger, now: o.Now, tmpl: map[string]*template.Template{}, missing: append([]string(nil), o.Missing...)}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.o.IdleTimeout <= 0 {
		s.o.IdleTimeout = defaultIdle
	}
	if s.o.MaxAge <= 0 {
		s.o.MaxAge = defaultMaxAge
	}
	for _, name := range []string{"signin", "setup", "message", "invite", "connect"} {
		s.tmpl[name] = template.Must(template.ParseFS(assets, "templates/base.html", "templates/"+name+".html"))
	}

	s.csp = "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	if u := o.PublicURL; u != nil {
		if u.Path != "" {
			s.missing = append(s.missing, "PRIVO_PUBLIC_URL (the admin site needs it without a path)")
		}
		s.origin = originOf(u)
		s.secure = u.Scheme == "https"
		s.callback = s.origin + "/auth/github/callback"
		s.connectCallback = upstreamauth.CallbackURL(&url.URL{Scheme: u.Scheme, Host: u.Host})
	}
	if o.GitHub != nil && o.GitHub.BaseURL != nil {
		// Chrome applies form-action to the redirect that follows a form post,
		// so the sign-in form may only go on to GitHub itself.
		s.csp = strings.Replace(s.csp, "form-action 'self'", "form-action 'self' "+originOf(o.GitHub.BaseURL), 1)
	}
	s.sessionCookie, s.signInCookie, s.connectCookie = "privo_session", "privo_signin", "privo_oauth"
	if s.secure {
		// The __Host- prefix makes browsers refuse a cookie that a sibling
		// subdomain or a plain-http page tries to plant.
		s.sessionCookie, s.signInCookie, s.connectCookie = "__Host-"+s.sessionCookie, "__Host-"+s.signInCookie, "__Host-"+s.connectCookie
	}
	if o.App != nil {
		a, err := loadApp(o.App)
		if err != nil {
			s.missing = append(s.missing, "the admin app's build ("+err.Error()+"; run make ui)")
		}
		s.app = a
	}
	s.enabled = len(s.missing) == 0 && o.Store != nil && o.Audit != nil && o.GitHub != nil && o.PublicURL != nil && o.API != nil && s.app != nil && o.Keys != nil
	if !s.enabled && len(s.missing) == 0 {
		s.missing = []string{"the admin site is not wired up"}
	}
	return s
}

// originOf is scheme://host as a browser writes it in an Origin header: a
// default port is left out.
func originOf(u *url.URL) string {
	host := strings.ToLower(u.Host)
	switch u.Scheme {
	case "https":
		host = strings.TrimSuffix(host, ":443")
	case "http":
		host = strings.TrimSuffix(host, ":80")
	}
	return u.Scheme + "://" + host
}

// Routes registers the website on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	if !s.enabled {
		off := s.wrap(s.disabled)
		mux.Handle("/admin/", off)
		mux.Handle("/auth/", off)
		mux.Handle("/connect/", off)
		return
	}
	mux.Handle("GET /admin/assets/app.css", s.wrap(s.stylesheet))
	mux.Handle("GET /admin/{$}", s.wrap(s.home))
	mux.Handle("GET /admin/signin", s.wrap(s.signInPage))
	mux.Handle("GET /admin/setup/{token}", s.wrap(s.setupPage))
	mux.Handle("POST /auth/github/start", s.wrap(s.start))
	mux.Handle("GET /auth/github/callback", s.wrap(s.callbackHandler))
	mux.Handle("POST /admin/signout", s.wrap(s.signOut))
	mux.Handle("GET /admin/invite/{token}", s.wrap(s.invitePage))
	s.appRoutes(mux)
	if s.connections() {
		mux.Handle("GET /connect/callback", s.wrap(s.connectCallbackHandler))
		mux.Handle("GET /connect/{alias}", s.wrap(s.connectPage))
		mux.Handle("POST /connect/{alias}/start", s.wrap(s.connectStart))
		mux.Handle("GET /oauth/client-metadata.json", http.HandlerFunc(s.clientMetadata))
	}
}

// Prune deletes sessions, connections in flight, setup links and invites that
// can no longer be used. Run it from time to time; correctness never depends on it.
func (s *Server) Prune(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	return s.o.Store.PruneSignIn(ctx, s.o.Org, s.now(), s.o.IdleTimeout)
}

// Run prunes now and then every interval until ctx is done. A failed prune is
// logged and tried again; nothing else depends on it.
func (s *Server) Run(ctx context.Context, every time.Duration) {
	if !s.enabled {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if err := s.Prune(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("web: prune", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// referrerPolicy is same-origin, not no-referrer. Under no-referrer a browser
// sends "Origin: null" on every form post, so the origin check (and with it
// every button) would refuse the site's own pages; a real Chromium run found
// this, the Go clients in the tests do not behave like that. same-origin still
// sends nothing to GitHub or any other site.
const referrerPolicy = "same-origin"

// wrap adds the security headers to every response of a handler.
func (s *Server) wrap(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", s.csp)
		hd.Set("Cache-Control", "no-store")
		hd.Set("Referrer-Policy", referrerPolicy)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		h(w, r)
	})
}

// ---- pages

type pageData struct {
	Title string
	User  *userView
	CSRF  string
	Flash *flash

	// message pages
	Heading string
	Lines   []string
	List    []string
	Link    *link

	// setup page
	SetupToken string
	Expires    string

	// sign-in, invite and connect pages
	ReturnTo         string
	InviteToken      string
	Person, Role     string
	Server, AuthHost string
	Scopes           string
}

type userView struct {
	Name, Person, Role string
}

type flash struct{ Class, Text string }

type link struct{ Href, Text string }

func (s *Server) render(w http.ResponseWriter, status int, name string, d pageData) {
	var buf bytes.Buffer
	if err := s.tmpl[name].ExecuteTemplate(&buf, "base", d); err != nil {
		s.log.Error("web: render", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) message(w http.ResponseWriter, status int, heading string, lines ...string) {
	s.render(w, status, "message", pageData{Title: heading, Heading: heading, Lines: lines})
}

func (s *Server) messageLink(w http.ResponseWriter, status int, heading, href, text string, lines ...string) {
	s.render(w, status, "message", pageData{Title: heading, Heading: heading, Lines: lines, Link: &link{Href: href, Text: text}})
}

func redirect(w http.ResponseWriter, to string) {
	w.Header().Set("Location", to)
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) disabled(w http.ResponseWriter, _ *http.Request) {
	s.render(w, http.StatusServiceUnavailable, "message", pageData{Title: "Sign-in is not set up", Heading: "Sign-in is not set up",
		Lines: []string{"The admin site needs these settings before it can run. Set them and restart Privo."}, List: s.missing})
}

func (s *Server) stylesheet(w http.ResponseWriter, _ *http.Request) {
	b, err := assets.ReadFile("assets/app.css")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	// Not content-hashed, so cached for a while rather than forever.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := s.session(r); ok {
		redirect(w, s.landing(u))
		return
	}
	redirect(w, "/admin/signin")
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if !returnToRe.MatchString(next) || !s.connections() {
		next = ""
	}
	if _, u, ok := s.session(r); ok {
		if next == "" {
			next = s.landing(u)
		}
		redirect(w, next)
		return
	}
	s.render(w, http.StatusOK, "signin", pageData{Title: "Sign in", ReturnTo: next})
}

// ---- cookies, sessions and request checks

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	// Secure follows the scheme of the public URL: it is plain http only for
	// loopback, where browsers still accept the cookie (ADR 0016).
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}) //nolint:gosec // see above
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) { s.setCookie(w, name, "", -1) }

// session returns the signed-in session and user for a request. An unknown,
// expired, idle or malformed cookie, and a disabled user, all give false.
func (s *Server) session(r *http.Request) (store.Session, store.User, bool) {
	c, err := r.Cookie(s.sessionCookie)
	if err != nil || !validSecret(c.Value) {
		return store.Session{}, store.User{}, false
	}
	hash := HashSecret(c.Value)
	now := s.now()
	sess, u, err := s.o.Store.SessionByHash(r.Context(), s.o.Org, hash, now, s.o.IdleTimeout)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: read session", "err", err)
		}
		return store.Session{}, store.User{}, false
	}
	if err := s.o.Store.TouchSession(r.Context(), s.o.Org, hash, now); err != nil {
		s.log.Warn("web: touch session", "err", err)
	}
	return sess, u, true
}

// principal is who a signed-in person is, in the terms the permission table uses.
func (s *Server) principal(u store.User) auth.Principal {
	return auth.Principal{OrgID: s.o.Org, Name: u.Person, Role: u.Role, Kind: kindSession, Person: u.Person, UserID: u.ID}
}

func view(u store.User) *userView {
	return &userView{Name: u.DisplayName, Person: u.Person, Role: u.Role}
}

// sameOrigin accepts a request that comes from this site's own pages. The
// expected origin is the configured public URL, never the Host header.
func (s *Server) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return o == s.origin
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// form runs the checks every POST needs before it reads anything: the origin
// and a bounded, parsed body. It answers and returns false when one fails.
func (s *Server) form(w http.ResponseWriter, r *http.Request) bool {
	if !s.sameOrigin(r) {
		s.message(w, http.StatusForbidden, "Request refused", "This request did not come from Privo’s own pages.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.message(w, http.StatusBadRequest, "Request refused", "The form could not be read.")
		return false
	}
	return true
}

// guarded is form plus a session and its CSRF token. Without a session the
// person is sent to sign in; with a wrong token the request is refused.
func (s *Server) guarded(w http.ResponseWriter, r *http.Request) (store.Session, store.User, bool) {
	if !s.form(w, r) {
		return store.Session{}, store.User{}, false
	}
	sess, u, ok := s.session(r)
	if !ok {
		redirect(w, "/admin/signin")
		return store.Session{}, store.User{}, false
	}
	// Only the body counts: a token in the URL would end up in logs and history.
	got := r.PostForm.Get("csrf")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
		s.pageFor(w, http.StatusForbidden, sess, u, "Request refused", "The page you used is out of date. Go back, reload it and try again.")
		return store.Session{}, store.User{}, false
	}
	return sess, u, true
}

// pageFor renders a message page for a signed-in person (with the sign-out form).
func (s *Server) pageFor(w http.ResponseWriter, status int, sess store.Session, u store.User, heading string, lines ...string) {
	s.render(w, status, "message", pageData{Title: heading, Heading: heading, Lines: lines, User: view(u), CSRF: sess.CSRF})
}

func (s *Server) event(action, actor, actorID, decision, outcome string) store.AuditEvent {
	return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: s.now(), Actor: actor, ActorID: actorID, Client: "web", Action: action, Decision: decision, Outcome: outcome}
}

// audit writes an admin event for a signed-in person. A failure is logged, as
// the control API does; the state changes it describes are audited in their
// own transaction where they can be.
func (s *Server) audit(ctx context.Context, u store.User, action string) {
	ev := s.event(action, u.Person, "user:"+u.ID, "allow", "ok")
	if err := s.o.Audit.Admin(ctx, s.o.Org, ev); err != nil {
		s.log.Error("web: audit", "action", action, "err", err)
	}
}

func (s *Server) newSession(userID string) (secret string, sess store.Session, err error) {
	if secret, err = randomSecret(); err != nil {
		return "", store.Session{}, err
	}
	csrf, err := randomSecret()
	if err != nil {
		return "", store.Session{}, err
	}
	now := s.now()
	return secret, store.Session{ID: HashSecret(secret), UserID: userID, CSRF: csrf, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.o.MaxAge)}, nil
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.guarded(w, r)
	if !ok {
		return
	}
	ev := s.event("signout", u.Person, "user:"+u.ID, "allow", "ok")
	if err := s.o.Store.DeleteSession(r.Context(), s.o.Org, sess.ID, ev); err != nil {
		s.log.Error("web: sign out", "err", err)
		s.pageFor(w, http.StatusInternalServerError, sess, u, "Could not sign out", "Try again.")
		return
	}
	s.clearCookie(w, s.sessionCookie)
	redirect(w, "/admin/signin")
}
