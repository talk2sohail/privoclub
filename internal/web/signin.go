package web

import (
	"errors"
	"net/http"
	"strings"

	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// notFoundSetup is the one answer for every setup link that cannot be used:
// unknown, malformed, expired, used or replaced look the same.
func (s *Server) notFoundSetup(w http.ResponseWriter) {
	s.message(w, http.StatusNotFound, "This setup link does not work",
		"It may have been used already, it may have expired, or a newer one may have replaced it.",
		"Ask for a new one by running “privo admin setup-link” on the server.")
}

// setupPage shows the page behind a setup link. It never writes anything.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !validSetupToken(token) {
		s.notFoundSetup(w)
		return
	}
	l, err := s.o.Store.SetupLink(r.Context(), s.o.Org, HashSecret(token), s.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: look up setup link", "err", err)
		}
		s.notFoundSetup(w)
		return
	}
	s.render(w, http.StatusOK, "setup", pageData{Title: "Set up Privo", SetupToken: token, Expires: l.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")})
}

// start sends the browser to GitHub. It is a POST so a page cannot start
// sign-ins for people by embedding a link; the origin check keeps other sites
// from posting it.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if !s.form(w, r) {
		return
	}
	st := store.OAuthState{Purpose: store.PurposeSignIn}
	if _, setup := r.PostForm["setup_token"]; setup {
		token := r.PostForm.Get("setup_token")
		if !validSetupToken(token) {
			s.notFoundSetup(w)
			return
		}
		l, err := s.o.Store.SetupLink(r.Context(), s.o.Org, HashSecret(token), s.now())
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("web: look up setup link", "err", err)
			}
			s.notFoundSetup(w)
			return
		}
		st.Purpose, st.SetupLinkID = store.PurposeSetup, l.ID
	} else if _, inv := r.PostForm["invite_token"]; inv {
		token := r.PostForm.Get("invite_token")
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
		st.Purpose, st.InviteID = store.PurposeInvite, in.ID
	} else if next := r.PostForm.Get("return_to"); returnToRe.MatchString(next) && s.connections() {
		st.ReturnTo = next
	}

	state, err1 := randomSecret()
	nonce, err2 := randomSecret()
	verifier, err3 := githubauth.NewVerifier()
	if err := errors.Join(err1, err2, err3); err != nil {
		s.log.Error("web: random", "err", err)
		s.message(w, http.StatusInternalServerError, "Could not start sign-in", "Try again.")
		return
	}
	now := s.now()
	st.Hash, st.Verifier, st.ExpiresAt = stateHash(state, nonce), verifier, now.Add(StateTTL)
	switch err := s.o.Store.CreateOAuthState(r.Context(), s.o.Org, st, now, s.o.StateCap); {
	case errors.Is(err, store.ErrTooManyStates):
		s.message(w, http.StatusServiceUnavailable, "Too many sign-ins in progress", "Wait a few minutes and try again.")
		return
	case err != nil:
		s.log.Error("web: store sign-in state", "err", err)
		s.message(w, http.StatusInternalServerError, "Could not start sign-in", "Try again.")
		return
	}
	s.setCookie(w, s.stateCookie, nonce, int(StateTTL.Seconds()))
	redirect(w, s.o.GitHub.AuthorizeURL(s.callback, state, githubauth.Challenge(verifier)))
}

func (s *Server) signInFailed(w http.ResponseWriter, status int, lines ...string) {
	s.messageLink(w, status, "Sign-in did not complete", "/admin/signin", "Start again", lines...)
}

// callbackHandler finishes a sign-in. The browser must bring the state it was
// given and the cookie that was set with it; the state is used up first, so a
// callback works once.
func (s *Server) callbackHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	c, err := r.Cookie(s.stateCookie)
	if err != nil || !validSecret(c.Value) || !validSecret(state) {
		s.clearCookie(w, s.stateCookie)
		s.signInFailed(w, http.StatusBadRequest, "This sign-in was not started in this browser, or it has expired.")
		return
	}
	s.clearCookie(w, s.stateCookie)
	st, err := s.o.Store.UseOAuthState(r.Context(), s.o.Org, stateHash(state, c.Value), s.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: use sign-in state", "err", err)
		}
		s.signInFailed(w, http.StatusBadRequest, "This sign-in was not started in this browser, or it has expired.")
		return
	}
	if q.Get("error") != "" || code == "" || len(code) > maxCodeLen {
		s.signInFailed(w, http.StatusBadRequest, "GitHub did not complete the sign-in.")
		return
	}

	token, err := s.o.GitHub.Exchange(r.Context(), code, s.callback, st.Verifier)
	if err != nil {
		s.githubFailed(w, "exchange the code", err)
		return
	}
	prof, err := s.o.GitHub.User(r.Context(), token)
	if err != nil {
		s.githubFailed(w, "read the profile", err)
		return
	}

	switch st.Purpose {
	case store.PurposeSetup:
		s.finishSetup(w, r, st, prof)
	case store.PurposeInvite:
		s.finishInvite(w, r, st, prof)
	case store.PurposeSignIn:
		s.finishSignIn(w, r, st, prof)
	default:
		s.signInFailed(w, http.StatusBadRequest, "This sign-in was not started in this browser, or it has expired.")
	}
}

// githubFailed answers a failed call to GitHub. What GitHub said is never
// repeated: a refusal is a refusal, and anything else is "could not reach".
func (s *Server) githubFailed(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, githubauth.ErrRejected) {
		s.signInFailed(w, http.StatusBadRequest, "GitHub did not accept the sign-in.")
		return
	}
	s.log.Warn("web: GitHub failed", "step", what, "err", err)
	s.signInFailed(w, http.StatusBadGateway, "Privo could not reach GitHub. Try again in a moment.")
}

func displayName(p githubauth.Profile) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Login
}

// denied is the single answer for an account that is not a member: a stranger
// and a disabled member see the same page.
func (s *Server) denied(w http.ResponseWriter, r *http.Request, p githubauth.Profile) {
	ev := s.event("signin.denied", p.Login, "github:"+p.Subject(), "deny", "denied")
	if err := s.o.Audit.Admin(r.Context(), s.o.Org, ev); err != nil {
		s.log.Error("web: audit", "action", "signin.denied", "err", err)
	}
	s.message(w, http.StatusForbidden, "This GitHub account cannot sign in",
		"It is not a member of this Privo. Ask an owner to add you.")
}

func (s *Server) finishSignIn(w http.ResponseWriter, r *http.Request, st store.OAuthState, p githubauth.Profile) {
	u, err := s.o.Store.UserByIdentity(r.Context(), s.o.Org, store.ProviderGitHub, p.Subject())
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		s.denied(w, r, p)
		return
	}
	if err != nil {
		s.log.Error("web: look up member", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	secret, sess, err := s.newSession(u.ID)
	if err == nil {
		err = s.o.Store.CreateSession(r.Context(), s.o.Org, sess, s.event("signin", u.Person, "user:"+u.ID, "allow", "ok"))
	}
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.sessionCookie, secret, int(s.o.MaxAge.Seconds()))
	to := s.landing(u)
	if returnToRe.MatchString(st.ReturnTo) && s.connections() {
		to = st.ReturnTo
	}
	redirect(w, to)
}

func (s *Server) finishSetup(w http.ResponseWriter, r *http.Request, st store.OAuthState, p githubauth.Profile) {
	secret, sess, err := s.newSession("")
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	userID := ids.New()
	sess.UserID = userID
	u, err := s.o.Store.ClaimSetup(r.Context(), s.o.Org, store.Claim{
		LinkID: st.SetupLinkID, UserID: userID, Person: strings.ToLower(p.Login), Subject: p.Subject(), Login: p.Login,
		DisplayName: displayName(p), Session: sess, Now: s.now(),
	}, s.event("setup.claim", strings.ToLower(p.Login), "github:"+p.Subject(), "allow", "ok"))
	switch {
	case errors.Is(err, store.ErrSetupUnavailable):
		s.notFoundSetup(w)
		return
	case errors.Is(err, store.ErrIdentityExists):
		s.messageLink(w, http.StatusConflict, "This GitHub account is already a member", "/admin/signin", "Sign in instead",
			"The setup link has not been used up. It still works for another account.")
		return
	case errors.Is(err, store.ErrConflict):
		s.message(w, http.StatusConflict, "That person name is already taken",
			"Another member already uses the name “"+strings.ToLower(p.Login)+"”. The setup link has not been used up.",
			"Ask for a new link that names the person: privo admin setup-link --person NAME")
		return
	case err != nil:
		s.log.Error("web: claim setup link", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.sessionCookie, secret, int(s.o.MaxAge.Seconds()))
	redirect(w, s.landing(u))
}
