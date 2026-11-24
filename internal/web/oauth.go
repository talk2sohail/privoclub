package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// The authorization endpoint of Privo's own authorization server (F06, ADR
// 0022 points 6 and 7). The server itself is internal/oauthserver; this is
// the part that needs the person: their session, the consent page and the
// two buttons.
//
// Nothing is written for someone who is not signed in. A request without a
// session is checked, sealed into a cookie and the person is sent to sign in;
// they come back to a fixed address and the request is read from the cookie.
// The first write is the code, when a signed-in member presses Allow.

// AuthorizationServer is what the authorization endpoint needs of
// internal/oauthserver.
type AuthorizationServer interface {
	ParseAuthorize(ctx context.Context, q url.Values) (*oauthserver.AuthRequest, *oauthserver.AuthError)
	Allow(w http.ResponseWriter, req *oauthserver.AuthRequest, u store.User)
	Deny(w http.ResponseWriter, req *oauthserver.AuthRequest, u store.User)
}

const (
	authorizePath = "/oauth/authorize"
	// maxAuthorizeQuery bounds an authorization request. A real one is a few
	// hundred bytes; the bound keeps the sealed cookie inside what browsers
	// store.
	maxAuthorizeQuery   = 2000
	maxScopeReturnQuery = 2048
)

// authorizeRequest is an authorization request on its way through sign-in
// (in the cookie) or through the consent page (in the form).
type authorizeRequest struct {
	Query   string `json:"q"`
	Expires int64  `json:"e"`
}

// returnOK reports whether a path is one a person may be sent back to after
// signing in. There are two fixed shapes and no other: /oauth/authorize, or
// /connect/<alias> with at most a validated scope query parameter.
func (s *Server) returnOK(next string) bool {
	_, ok := s.safeReturnTo(next)
	return ok
}

func (s *Server) safeReturnTo(next string) (string, bool) {
	if next == authorizePath {
		return authorizePath, s.o.AuthServer != nil
	}
	if !s.connections() {
		return "", false
	}
	path, rawQuery, hasQuery := strings.Cut(next, "?")
	if !returnToRe.MatchString(path) {
		return "", false
	}
	if !hasQuery {
		return path, true
	}
	if rawQuery == "" || len(rawQuery) > maxScopeReturnQuery || strings.ContainsAny(rawQuery, "#\\\r\n\t;") {
		return "", false
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil || len(q) != 1 {
		return "", false
	}
	vals, ok := q["scope"]
	if !ok || len(vals) != 1 {
		return "", false
	}
	scopes, ok := upstreamauth.ValidateScopes(vals[0])
	if !ok {
		return "", false
	}
	return path + "?scope=" + encodeScopeQuery(scopes), true
}

func encodeScopeQuery(scopes []string) string {
	return strings.ReplaceAll(url.QueryEscape(strings.Join(scopes, " ")), "+", "%20")
}

func (s *Server) oauthOff(w http.ResponseWriter, _ *http.Request) {
	s.message(w, http.StatusServiceUnavailable, "Signing in with OAuth is not switched on",
		"This Privo accepts personal tokens only. An administrator can switch OAuth on with PRIVO_OAUTH_SERVER.")
}

func (s *Server) authorizeFailed(w http.ResponseWriter, e *oauthserver.AuthError) {
	if !e.Page {
		e.Redirect(w)
		return
	}
	lines := []string{e.Detail}
	if e.Host != "" {
		lines = append(lines, "The client is "+e.Host+".")
	}
	s.message(w, e.Status, e.Title, lines...)
}

// authorizePage is GET /oauth/authorize: with parameters it is a client's
// request; without, it is the person coming back from signing in.
func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request) {
	raw, fromCookie := r.URL.RawQuery, false
	if raw == "" {
		c, err := r.Cookie(s.authzCookie)
		var saved authorizeRequest
		id, sealed, found := "", "", false
		if err == nil {
			id, sealed, found = strings.Cut(c.Value, ".")
		}
		if !found || !s.open(r.Context(), "authorize", id, sealed, maxSealedRequest, &saved) || s.now().Unix() >= saved.Expires {
			s.message(w, http.StatusBadRequest, "Nothing to allow", "This page is opened by an AI client that wants to connect to Privo. Start again from the client.")
			return
		}
		raw, fromCookie = saved.Query, true
	}
	if len(raw) > maxAuthorizeQuery {
		s.message(w, http.StatusBadRequest, "This request cannot be used", "It is longer than Privo accepts.")
		return
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		s.message(w, http.StatusBadRequest, "This request cannot be used", "Its parameters could not be read.")
		return
	}
	req, aerr := s.o.AuthServer.ParseAuthorize(r.Context(), q)
	if aerr != nil {
		if fromCookie {
			s.clearCookie(w, s.authzCookie)
		}
		s.authorizeFailed(w, aerr)
		return
	}
	sess, u, ok := s.session(r)
	if !ok {
		// Checked, and still nothing is written: the request waits in the
		// browser while the person signs in.
		id, err := randomSecret()
		var sealed string
		if err == nil {
			sealed, err = s.seal(r.Context(), "authorize", id, authorizeRequest{Query: raw, Expires: s.now().Add(StateTTL).Unix()})
		}
		if err != nil {
			s.log.Error("web: seal the authorization request", "err", err)
			s.message(w, http.StatusInternalServerError, "Something went wrong", "Try again from your AI client.")
			return
		}
		s.setCookie(w, s.authzCookie, id+"."+sealed, int(StateTTL.Seconds()))
		redirect(w, "/admin/signin?next="+url.QueryEscape(authorizePath))
		return
	}
	if fromCookie {
		s.clearCookie(w, s.authzCookie)
	}
	if !slices.ContainsFunc(u.Roles, store.GatewayRole) {
		s.pageFor(w, http.StatusForbidden, sess, u, "Your roles cannot connect an AI client",
			"Only an owner, an administrator or a member uses servers through Privo, so only they can let a client do it for them. Nothing was sent to "+req.Client.Name+".")
		return
	}
	// The request travels through the page sealed, bound to this session, so
	// the answer is for exactly what was shown.
	sealed, err := s.seal(r.Context(), "consent", sess.ID, authorizeRequest{Query: raw, Expires: s.now().Add(StateTTL).Unix()})
	if err != nil {
		s.log.Error("web: seal the consent", "err", err)
		s.pageFor(w, http.StatusInternalServerError, sess, u, "Something went wrong", "Try again from your AI client.")
		return
	}
	var warn []string
	switch req.Client.Kind {
	case store.ClientRegistered:
		warn = append(warn, "This client registered itself with this Privo. Its name is its own claim and nobody has checked it.")
	case store.ClientDocument:
		warn = append(warn, "This client is not on Privo’s list of known clients and no administrator has approved it. Its name is its own claim.")
	}
	if req.Client.LoopbackOnly() {
		warn = append(warn, "It runs on your computer. Any program on this computer can claim to be it, so allow it only if you just started it yourself.")
	} else if h := req.RedirectHost(); h != "" {
		// A hosted client serves many accounts: anyone with an account there
		// can start this same request and send you the link, and the page
		// looks the same (review of 2026-11-15, S-07).
		warn = append(warn, "Allow only if you started this just now from your own account at "+h+". Anyone with an account there can start the same request and send it to you.")
	}
	// The form posts to Privo, which redirects to the client: Chrome checks
	// that redirect against form-action.
	w.Header().Set("Content-Security-Policy", formActionRe.ReplaceAllLiteralString(s.csp, "form-action 'self' "+req.RedirectOrigin()+";"))
	s.render(w, http.StatusOK, "consent", pageData{Title: "Allow " + req.Client.Name, User: view(u), CSRF: sess.CSRF, Lines: warn,
		ClientName: req.Client.Name, ClientHost: req.Client.Host(), RedirectHost: req.RedirectHost(), Sealed: sealed})
}

// authorizeDecision is POST /oauth/authorize: Allow or Deny from the consent
// page, with the session's CSRF token and the sealed request that was shown.
func (s *Server) authorizeDecision(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.guarded(w, r)
	if !ok {
		return
	}
	var shown authorizeRequest
	if !s.open(r.Context(), "consent", sess.ID, r.PostForm.Get("request"), maxSealedRequest, &shown) || s.now().Unix() >= shown.Expires {
		s.pageFor(w, http.StatusBadRequest, sess, u, "This page is out of date", "Start again from your AI client.")
		return
	}
	q, err := url.ParseQuery(shown.Query)
	if err != nil {
		s.pageFor(w, http.StatusBadRequest, sess, u, "This page is out of date", "Start again from your AI client.")
		return
	}
	// Checked again: the client may have been removed since the page was shown.
	req, aerr := s.o.AuthServer.ParseAuthorize(r.Context(), q)
	if aerr != nil {
		s.authorizeFailed(w, aerr)
		return
	}
	switch r.PostForm.Get("decision") {
	case "allow":
		if !slices.ContainsFunc(u.Roles, store.GatewayRole) {
			s.pageFor(w, http.StatusForbidden, sess, u, "Your roles cannot connect an AI client", "Nothing was sent to "+req.Client.Name+".")
			return
		}
		s.o.AuthServer.Allow(w, req, u)
	case "deny":
		s.o.AuthServer.Deny(w, req, u)
	default:
		s.pageFor(w, http.StatusBadRequest, sess, u, "Request refused", "Choose Allow or Deny.")
	}
}
