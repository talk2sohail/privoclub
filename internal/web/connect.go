package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// Invites and each person's upstream connections (ADR 0017).

// Grants is what the website needs from the grant service.
type Grants interface {
	Endpoint(ctx context.Context, org store.OrgID, srv store.Server) (upstreamauth.Endpoint, error)
	Save(ctx context.Context, org store.OrgID, srv store.Server, user store.User, tok upstreamauth.Token, catalogOK bool, ev store.AuditEvent) (store.Grant, error)
	Disconnect(ctx context.Context, org store.OrgID, srv store.Server, userID string, ev store.AuditEvent) error
	Reregister(ctx context.Context, org store.OrgID, srv store.Server) error
}

// Upstreams is what the website tells the upstream manager.
type Upstreams interface {
	Kick(serverID string)
	ClosePerson(serverID, userID string)
}

var (
	aliasRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,15}$`)
	returnToRe = regexp.MustCompile(`^/connect/[a-z][a-z0-9-]{1,15}$`)
	// formActionRe is the form-action directive of the site's CSP.
	formActionRe = regexp.MustCompile(`form-action [^;]*;`)
)

func (s *Server) connections() bool {
	return s.o.Grants != nil && s.o.OAuth != nil && s.o.Upstream != nil
}

// landing is the app page a person starts on after signing in: owners and
// admins on Servers, approvers on Approvals, members on Tokens (a member's
// first step is a token for their AI client), auditors on Activity.
func (s *Server) landing(u store.User) string {
	p := s.principal(u)
	switch {
	case p.Can(auth.PermManageServers):
		return "/admin/servers"
	case p.Can(auth.PermDecide):
		return "/admin/approvals"
	case p.Can(auth.PermUseGateway):
		return "/admin/start" // their teams, their servers, their AI client's setup (ADR 0020)
	case p.Can(auth.PermReadAudit):
		return "/admin/activity"
	}
	return "/admin/start"
}

// ---- invites

func (s *Server) notFoundInvite(w http.ResponseWriter) {
	s.message(w, http.StatusNotFound, "This invite does not work",
		"It may have been used already, it may have expired, or a newer one may have replaced it.",
		"Ask the person who invited you for a new one.")
}

func (s *Server) invitePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !validInviteToken(token) {
		s.notFoundInvite(w)
		return
	}
	in, err := s.o.Store.Invite(r.Context(), s.o.Org, HashSecret(token), s.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: look up invite", "err", err)
		}
		s.notFoundInvite(w)
		return
	}
	s.render(w, http.StatusOK, "invite", pageData{Title: "Join Privo", InviteToken: token, Person: in.Person, Role: in.Role,
		Expires: in.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")})
}

func (s *Server) finishInvite(w http.ResponseWriter, r *http.Request, st store.OAuthState, p githubauth.Profile) {
	secret, sess, err := s.newSession("")
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	userID := ids.New()
	sess.UserID = userID
	u, err := s.o.Store.ClaimInvite(r.Context(), s.o.Org, store.Claim{
		LinkID: st.InviteID, UserID: userID, Subject: p.Subject(), Login: p.Login,
		DisplayName: displayName(p), Session: sess, Now: s.now(),
	}, s.event("invite.claim", strings.ToLower(p.Login), "github:"+p.Subject(), "allow", "ok"))
	switch {
	case errors.Is(err, store.ErrSetupUnavailable):
		s.notFoundInvite(w)
		return
	case errors.Is(err, store.ErrIdentityExists):
		s.messageLink(w, http.StatusConflict, "This GitHub account is already a member", "/admin/signin", "Sign in instead",
			"The invite has not been used up.")
		return
	case errors.Is(err, store.ErrConflict):
		s.message(w, http.StatusConflict, "That person name is already taken",
			"Another member already uses the name on this invite. The invite has not been used up; ask for a new one.")
		return
	case err != nil:
		s.log.Error("web: claim invite", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.sessionCookie, secret, int(s.o.MaxAge.Seconds()))
	redirect(w, s.landing(u))
}

// ---- connections

func (s *Server) oauthServers(ctx context.Context, u store.User) ([]store.Server, error) {
	all, err := s.o.Store.ListServers(ctx, s.o.Org)
	if err != nil {
		return nil, err
	}
	var out []store.Server
	for _, srv := range all {
		if srv.Config.Auth != store.AuthOAuth || srv.Config.OAuth == nil {
			continue
		}
		ok, err := s.mayConnect(ctx, u, srv)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, srv)
		}
	}
	return out, nil
}

// mayConnect reports whether u may connect their account to srv: a server
// their teams reach (ADR 0020), or any server for those who manage servers,
// since the first admin to connect provides the server's tool list. Anyone
// else is told the server does not exist, so teams cannot be probed, and
// holds no grant for a server they cannot use.
func (s *Server) mayConnect(ctx context.Context, u store.User, srv store.Server) (bool, error) {
	if s.principal(u).Can(auth.PermManageServers) {
		return true, nil
	}
	acc, err := s.o.Store.AccessFor(ctx, s.o.Org, u.ID)
	if err != nil {
		return false, err
	}
	return acc.Reaches(srv.ID), nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// ---- connect flow

func (s *Server) connectFailed(w http.ResponseWriter, status int, lines ...string) {
	s.messageLink(w, status, "The connection did not complete", "/admin/connections", "Back to your connections", lines...)
}

// connectTarget finds the oauth server named in the path for a signed-in
// person who may use the gateway, or answers and returns false.
func (s *Server) connectTarget(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User) (store.Server, bool) {
	if !s.principal(u).Can(auth.PermUseGateway) {
		s.pageFor(w, http.StatusForbidden, sess, u, "Not allowed", "Your role does not use tools, so it has no accounts to connect.")
		return store.Server{}, false
	}
	alias := r.PathValue("alias")
	if !aliasRe.MatchString(alias) {
		s.pageFor(w, http.StatusNotFound, sess, u, "No such server", "There is no server with that name.")
		return store.Server{}, false
	}
	srv, err := s.o.Store.ServerByAlias(r.Context(), s.o.Org, alias)
	if err == nil && (srv.Config.Auth != store.AuthOAuth || srv.Config.OAuth == nil) {
		err = store.ErrNotFound
	}
	if err == nil {
		var ok bool
		if ok, err = s.mayConnect(r.Context(), u, srv); err == nil && !ok {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: look up server", "err", err)
		}
		s.pageFor(w, http.StatusNotFound, sess, u, "No such server", "There is no server with that name that uses your own account.")
		return store.Server{}, false
	}
	return srv, true
}

// connectPage explains what connecting does. Its form posts to Privo, which
// then redirects to the authorization server, so this page's CSP names that
// server's origin in form-action (Chrome checks the redirect after a post).
func (s *Server) connectPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.session(r)
	if !ok {
		redirect(w, "/admin/signin?next="+url.QueryEscape("/connect/"+r.PathValue("alias")))
		return
	}
	srv, ok := s.connectTarget(w, r, sess, u)
	if !ok {
		return
	}
	o := srv.Config.OAuth
	if au, err := url.Parse(o.AuthorizationEndpoint); err == nil && au.Host != "" {
		w.Header().Set("Content-Security-Policy", formActionRe.ReplaceAllString(s.csp, "form-action 'self' "+originOf(au)+";"))
	}
	s.render(w, http.StatusOK, "connect", pageData{Title: "Connect " + srv.Alias, User: view(u), CSRF: sess.CSRF,
		Server: srv.Alias, AuthHost: hostOf(o.AuthorizationEndpoint), Scopes: strings.Join(o.Scopes, " ")})
}

func (s *Server) connectStart(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.guarded(w, r)
	if !ok {
		return
	}
	srv, ok := s.connectTarget(w, r, sess, u)
	if !ok {
		return
	}
	e, err := s.o.Grants.Endpoint(r.Context(), s.o.Org, srv)
	state, err1 := randomSecret()
	nonce, err2 := randomSecret()
	verifier, err3 := upstreamauth.NewVerifier()
	if err := errors.Join(err, err1, err2, err3); err != nil {
		s.log.Error("web: start connection", "server", srv.Alias, "err", err)
		s.connectFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	authURL, err := upstreamauth.AuthorizeURL(e, s.connectCallback, state, upstreamauth.Challenge(verifier), srv.Config.OAuth.Scopes)
	if err != nil {
		s.log.Error("web: authorization URL", "server", srv.Alias, "err", err)
		s.connectFailed(w, http.StatusInternalServerError, "The server's sign-in address is not usable. An administrator has to add it again.")
		return
	}
	now := s.now()
	st := store.OAuthState{Hash: stateHash(state, nonce), Purpose: store.PurposeConnect, Verifier: verifier, UserID: u.ID,
		SessionID: sess.ID, ServerID: srv.ID, Issuer: srv.Config.OAuth.Issuer, ExpiresAt: now.Add(StateTTL)}
	switch err := s.o.Store.CreateOAuthState(r.Context(), s.o.Org, st, now, s.o.StateCap); {
	case errors.Is(err, store.ErrTooManyStates):
		s.pageFor(w, http.StatusServiceUnavailable, sess, u, "Too many sign-ins in progress", "Wait a few minutes and try again.")
		return
	case err != nil:
		s.log.Error("web: store connect state", "err", err)
		s.connectFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.stateCookie, nonce, int(StateTTL.Seconds()))
	redirect(w, authURL)
}

// connectCallbackHandler finishes a connection. It needs the browser that
// started it (the cookie nonce) and the same signed-in session, checks the
// authorization server's identity, and stores nothing on any failure.
func (s *Server) connectCallbackHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	c, err := r.Cookie(s.stateCookie)
	s.clearCookie(w, s.stateCookie)
	if err != nil || !validSecret(c.Value) || !validSecret(state) {
		s.connectFailed(w, http.StatusBadRequest, "This connection was not started in this browser, or it has expired.")
		return
	}
	st, err := s.o.Store.UseOAuthState(r.Context(), s.o.Org, stateHash(state, c.Value), s.now())
	if err != nil || st.Purpose != store.PurposeConnect {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: use connect state", "err", err)
		}
		s.connectFailed(w, http.StatusBadRequest, "This connection was not started in this browser, or it has expired.")
		return
	}
	sess, u, ok := s.session(r)
	if !ok || sess.ID != st.SessionID || u.ID != st.UserID {
		s.connectFailed(w, http.StatusForbidden, "This connection was started by another sign-in. Sign in and start it again.")
		return
	}
	srv, err := s.o.Store.ServerByID(r.Context(), s.o.Org, st.ServerID)
	may := false
	if err == nil {
		may, err = s.mayConnect(r.Context(), u, srv) // teams may have changed since the start
	}
	if err != nil || !may || srv.Config.Auth != store.AuthOAuth || srv.Config.OAuth == nil || srv.Config.OAuth.Issuer != st.Issuer {
		s.connectFailed(w, http.StatusConflict, "The server changed while you were signing in. Start again.")
		return
	}
	// Mix-up defence (RFC 9207): an iss that differs is always refused; a
	// missing one only when the authorization server promised to send it.
	iss, hasIss := q["iss"]
	if (hasIss && (len(iss) != 1 || iss[0] != st.Issuer)) || (!hasIss && srv.Config.OAuth.IssParameter) {
		s.log.Warn("web: connect callback from an unexpected issuer", "server", srv.Alias)
		s.connectFailed(w, http.StatusBadRequest, "The answer did not come from the server's own sign-in.")
		return
	}
	if q.Get("error") != "" || code == "" || len(code) > maxCodeLen {
		s.connectFailed(w, http.StatusBadRequest, "The sign-in at "+hostOf(srv.Config.OAuth.AuthorizationEndpoint)+" did not complete.")
		return
	}
	e, err := s.o.Grants.Endpoint(r.Context(), s.o.Org, srv)
	if err != nil {
		s.log.Error("web: endpoint", "server", srv.Alias, "err", err)
		s.connectFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	tok, err := s.o.OAuth.Exchange(r.Context(), e, code, st.Verifier, s.connectCallback, s.now())
	if err != nil {
		if errors.Is(err, upstreamauth.ErrInvalidClient) && srv.Config.OAuth.Registration == store.RegistrationDynamic {
			// The authorization server forgot Privo's registration: register
			// again, so the next attempt can work.
			if rerr := s.o.Grants.Reregister(r.Context(), s.o.Org, srv); rerr != nil {
				s.log.Warn("web: registering again failed", "server", srv.Alias, "err", rerr)
				s.connectFailed(w, http.StatusBadGateway, "The sign-in server no longer knows Privo, and registering again failed. An administrator can add the server again.")
				return
			}
			s.audit(r.Context(), u, "server.oauth_register:"+srv.Alias+":dynamic")
			s.connectFailed(w, http.StatusConflict, "Privo had to register itself again with "+hostOf(e.TokenEndpoint)+". Start the connection again.")
			return
		}
		if errors.Is(err, upstreamauth.ErrInvalidGrant) || errors.Is(err, upstreamauth.ErrInvalidClient) {
			s.connectFailed(w, http.StatusBadRequest, "The sign-in was not accepted. Start again.")
			return
		}
		s.log.Warn("web: token exchange failed", "server", srv.Alias, "err", err)
		s.connectFailed(w, http.StatusBadGateway, "Privo could not reach "+hostOf(e.TokenEndpoint)+". Try again in a moment.")
		return
	}
	ev := s.event("upstream.connect:"+srv.Alias, u.Person, "user:"+u.ID, "allow", "ok")
	ev.Server = srv.Alias
	g, err := s.o.Grants.Save(r.Context(), s.o.Org, srv, u, tok, s.principal(u).Can(auth.PermManageServers), ev)
	if err != nil {
		s.log.Error("web: save grant", "server", srv.Alias, "err", err)
		s.connectFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.o.Upstream.ClosePerson(srv.ID, u.ID)
	if g.Catalog {
		s.o.Upstream.Kick(srv.ID)
	}
	redirect(w, "/admin/connections?result=connected")
}

// clientMetadata serves Privo's client ID metadata document. It holds nothing
// secret: authorization servers that support CIMD fetch it.
func (s *Server) clientMetadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(upstreamauth.ClientMetadata(s.o.PublicURL, true))
}
