package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
	s.render(w, http.StatusOK, "setup", pageData{Title: "Set up Privo", SetupToken: token, Expires: l.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"),
		Providers: s.withButtons(w, r)})
}

// begin sends the browser to a provider to sign in, claim a setup link or an
// invite, or link another sign-in to the signed-in member (ADR 0034 point
// 7). It is a POST so a page cannot start sign-ins for people by embedding a
// link; the origin check keeps other sites from posting it. Neither stops a
// script, so begin writes nothing: the sign-in in flight goes into a sealed
// cookie (ADR 0016 amendment 1). Linking also needs the session and its CSRF
// token, and the cookie names that session.
func (s *Server) begin(w http.ResponseWriter, r *http.Request, kind string) {
	if !s.form(w, r) {
		return
	}
	in := signIn{Purpose: purposeSignIn, Provider: kind}
	if _, link := r.PostForm["link"]; link {
		sess, u, ok := s.guarded(w, r)
		if !ok {
			return
		}
		in.Purpose, in.User, in.Session = purposeLink, u.ID, sess.ID
	} else if _, setup := r.PostForm["setup_token"]; setup {
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
		in.Purpose, in.Link = purposeSetup, l.ID
	} else if _, inv := r.PostForm["invite_token"]; inv {
		token := r.PostForm.Get("invite_token")
		if !validInviteToken(token) {
			s.notFoundInvite(w)
			return
		}
		inv, err := s.o.Store.Invite(r.Context(), s.o.Org, HashSecret(token), s.now())
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("web: look up invite", "err", err)
			}
			s.notFoundInvite(w)
			return
		}
		in.Purpose, in.Link = purposeInvite, inv.ID
	} else if next := r.PostForm.Get("return_to"); s.returnOK(next) {
		in.ReturnTo = next
	}

	state, err1 := randomSecret()
	verifier, err2 := githubauth.NewVerifier()
	nonce, err3 := randomSecret()
	if err := errors.Join(err1, err2, err3); err != nil {
		s.log.Error("web: random", "err", err)
		s.message(w, http.StatusInternalServerError, "Could not start sign-in", "Try again.")
		return
	}
	in.Verifier, in.Expires = verifier, s.now().Add(StateTTL).Unix()
	var to string
	switch kind {
	case kindOIDC:
		in.Nonce = nonce
		u, err := s.o.OIDC.AuthorizeURL(r.Context(), s.oidcCallbackURL, state, nonce, verifier)
		if err != nil {
			s.providerDown(r.Context(), kindOIDC, err)
			s.signInFailed(w, http.StatusBadGateway, "Privo could not reach "+s.label(kindOIDC)+". Try again in a moment.")
			return
		}
		to = u
	default:
		to = s.o.GitHub.AuthorizeURL(s.callback, state, githubauth.Challenge(verifier))
	}
	cookie, err := s.sealSignIn(r.Context(), state, in)
	if err != nil {
		s.log.Error("web: seal the sign-in cookie", "err", err)
		s.message(w, http.StatusInternalServerError, "Could not start sign-in", "Try again.")
		return
	}
	s.setCookie(w, s.signInCookie, cookie, int(StateTTL.Seconds()))
	redirect(w, to)
}

func (s *Server) signInFailed(w http.ResponseWriter, status int, lines ...string) {
	s.messageLink(w, status, "Sign-in did not complete", "/admin/signin", "Start again", lines...)
}

// callbackStart opens a callback's sealed sign-in for the provider kind:
// the browser must bring the state it was sent with and the cookie sealed
// for that state, made for this provider. The purpose, the link being
// claimed, the verifier and the nonce come from the cookie alone, never from
// the request. It answers the request itself when it returns false.
func (s *Server) callbackStart(w http.ResponseWriter, r *http.Request, kind string) (signIn, string, bool) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	const notHere = "This sign-in was not started in this browser, or it has expired."
	var (
		in signIn
		ok bool
	)
	if c, err := r.Cookie(s.signInCookie); err == nil {
		in, ok = s.openSignIn(r.Context(), state, c.Value, kind)
	}
	if !ok {
		// The cookie is left as it is: a request that does not belong to the
		// sign-in in this browser (a link planted on another site, say) must
		// not be able to cancel it (F05 R-03).
		s.signInFailed(w, http.StatusBadRequest, notHere)
		return signIn{}, "", false
	}
	s.clearCookie(w, s.signInCookie)
	if !s.finished.first(state, s.now(), StateTTL) {
		s.signInFailed(w, http.StatusBadRequest, notHere)
		return signIn{}, "", false
	}
	if q.Get("error") != "" || code == "" || len(code) > maxCodeLen {
		s.signInFailed(w, http.StatusBadRequest, s.label(kind)+" did not complete the sign-in.")
		return signIn{}, "", false
	}
	return in, code, true
}

// callbackHandler finishes a GitHub sign-in.
func (s *Server) callbackHandler(w http.ResponseWriter, r *http.Request) {
	in, code, ok := s.callbackStart(w, r, kindGitHub)
	if !ok {
		return
	}
	token, err := s.o.GitHub.Exchange(r.Context(), code, s.callback, in.Verifier)
	if err != nil {
		s.githubFailed(w, "exchange the code", err)
		return
	}
	prof, err := s.o.GitHub.User(r.Context(), token)
	if err != nil {
		s.githubFailed(w, "read the profile", err)
		return
	}
	// F05 A: only members of the configured organisations sign in, also
	// with a valid invite or setup link, and also to link.
	if len(s.o.GitHub.OrgIDs) > 0 {
		orgs, err := s.o.GitHub.Orgs(r.Context(), token)
		if errors.Is(err, githubauth.ErrOrgsHidden) {
			s.notInOrg(w, r, prof) // not shown is not a member, and it is audited
			return
		}
		if err != nil {
			s.githubFailed(w, "read the organisations", err)
			return
		}
		if !s.o.GitHub.InOrgs(orgs) {
			s.notInOrg(w, r, prof)
			return
		}
	}
	s.finish(w, r, in, githubIdentity(prof))
}

// finish completes a sign-in in flight by its purpose.
func (s *Server) finish(w http.ResponseWriter, r *http.Request, in signIn, id identity) {
	switch in.Purpose {
	case purposeSetup:
		s.finishSetup(w, r, in, id)
	case purposeInvite:
		s.finishInvite(w, r, in, id)
	case purposeLink:
		s.finishLink(w, r, in, id)
	case purposeSignIn:
		s.finishSignIn(w, r, in, id)
	default: // openSignIn accepts no other
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

// notInOrg answers a GitHub account outside the organisations sign-in is
// limited to (F05 A). It is audited like a refused stranger, with the same
// bound on how many rows a stranger can make Privo write.
func (s *Server) notInOrg(w http.ResponseWriter, r *http.Request, p githubauth.Profile) {
	record, skipped := s.denials.record(kindGitHub+":"+p.Subject(), false, s.now())
	s.deniedSummary(r.Context(), skipped)
	if record {
		ev := s.event("signin.refused:not_in_org", p.Login, "github:"+p.Subject(), "deny", "denied")
		if err := s.o.Audit.Admin(r.Context(), s.o.Org, ev); err != nil {
			s.log.Error("web: audit", "action", "signin.refused:not_in_org", "err", err)
		}
	}
	s.message(w, http.StatusForbidden, "This GitHub account cannot sign in",
		"This Privo accepts only members of its GitHub organisation. If you are one, check that the organisation lets you share your membership with Privo's GitHub app.")
}

// denied is the single answer for an account that is not a member: a stranger
// and a disabled member see the same page.
//
// A refusal is the one thing a person who is not a member can make Privo
// write, and anyone with a GitHub account can be refused as often as they
// like. So the audit gets one row per account in ten minutes and at most
// maxDenials rows in ten minutes over all strangers; a disabled member is
// always recorded. What is left out is counted, and the count is written to
// the audit as one row when the window ends (F05 R-02; review of 2026-11-15,
// S-06).
func (s *Server) denied(w http.ResponseWriter, r *http.Request, id identity, member bool) {
	record, skipped := s.denials.record(id.ref(), member, s.now())
	s.deniedSummary(r.Context(), skipped)
	if record {
		ev := s.event("signin.denied", id.Login, id.ref(), "deny", "denied")
		if err := s.o.Audit.Admin(r.Context(), s.o.Org, ev); err != nil {
			s.log.Error("web: audit", "action", "signin.denied", "err", err)
		}
	}
	s.message(w, http.StatusForbidden, "This "+s.label(id.Kind)+" account cannot sign in",
		"It is not a member of this Privo. Ask an owner to add you.")
}

// deniedSummary writes how many refused sign-ins a window left out of the
// audit, as one row, when there were any.
func (s *Server) deniedSummary(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	s.log.Warn("web: refused sign-ins left out of the audit in the last ten minutes", "count", n)
	ev := s.event(fmt.Sprintf("signin.denied.skipped:%d", n), "system", "system", "deny", "denied")
	if err := s.o.Audit.Admin(context.WithoutCancel(ctx), s.o.Org, ev); err != nil {
		s.log.Error("web: audit", "action", "signin.denied.skipped", "err", err)
	}
}

func (s *Server) finishSignIn(w http.ResponseWriter, r *http.Request, in signIn, id identity) {
	u, err := s.o.Store.UserByIdentity(r.Context(), s.o.Org, id.Provider, id.Subject)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		s.denied(w, r, id, err == nil)
		return
	}
	if err != nil {
		s.log.Error("web: look up member", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	secret, sess, err := s.newSession(u.ID)
	if err == nil {
		ev := s.event("signin", u.Person, "user:"+u.ID, "allow", "ok")
		ev.Item = id.item() // which sign-in, by provider and subject (ADR 0034)
		err = s.o.Store.CreateSession(r.Context(), s.o.Org, sess, ev)
	}
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.sessionCookie, secret, int(s.o.MaxAge.Seconds()))
	to := s.landing(u)
	if s.returnOK(in.ReturnTo) {
		to = in.ReturnTo
	}
	redirect(w, to)
}

func (s *Server) finishSetup(w http.ResponseWriter, r *http.Request, in signIn, id identity) {
	secret, sess, err := s.newSession("")
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	userID := ids.New()
	sess.UserID = userID
	u, err := s.o.Store.ClaimSetup(r.Context(), s.o.Org, store.Claim{
		LinkID: in.Link, UserID: userID, Person: id.person, Provider: id.Provider, Subject: id.Subject, Login: id.Login,
		DisplayName: id.display(), Session: sess, Now: s.now(),
	}, s.event("setup.claim", id.person, id.ref(), "allow", "ok"))
	switch {
	case errors.Is(err, store.ErrSetupUnavailable):
		s.notFoundSetup(w)
		return
	case errors.Is(err, store.ErrIdentityExists):
		s.messageLink(w, http.StatusConflict, "This "+s.label(id.Kind)+" account is already a member", "/admin/signin", "Sign in instead",
			"The setup link has not been used up. It still works for another account.")
		return
	case errors.Is(err, store.ErrConflict):
		s.message(w, http.StatusConflict, "That person name is already taken",
			"Another member already uses the name “"+id.person+"”. The setup link has not been used up.",
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
